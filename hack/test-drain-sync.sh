#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0
#
# Manual test script for bidirectional drain synchronization between
# Kubernetes and Slurm. Covers K8s→Slurm, Slurm→K8s, priority, loop
# prevention, scale-in, partial failure recovery, and idempotency.
#
# Usage:
#   ./hack/test-drain-sync.sh -n <namespace> [-p <pod-name>] [-w <wait-seconds>]

set -uo pipefail

# ---------------------------------------------------------------------------
# Constants (from api/v1beta1/well_known.go)
# ---------------------------------------------------------------------------
readonly ANNOTATION_PREFIX="nodeset.slinky.slurm.net"
readonly ANN_POD_CORDON="${ANNOTATION_PREFIX}/pod-cordon"
readonly ANN_POD_CORDON_SOURCE="${ANNOTATION_PREFIX}/pod-cordon-source"
readonly ANN_POD_CORDON_REASON="${ANNOTATION_PREFIX}/pod-cordon-reason"
readonly ANN_NODE_CORDON_SOURCE="${ANNOTATION_PREFIX}/node-cordon-source"
readonly ANN_NODE_CORDON_REASON="${ANNOTATION_PREFIX}/node-cordon-reason"
readonly OPERATOR_REASON_PREFIX="slurm-operator:"

# Colors
readonly RED='\033[0;31m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[0;33m'
readonly CYAN='\033[0;36m'
readonly BOLD='\033[1m'
readonly NC='\033[0m'

# Counters
PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0
TOTAL_COUNT=0
CURRENT_TEST=""

# Derived globals (set in main)
NAMESPACE=""
POD_NAME=""
WAIT_SECONDS=30
K8S_NODE=""
SLURM_NODE=""
NODESET_NAME=""

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
function drain::log() {
    echo -e "  $(date +%H:%M:%S) $*"
}

function drain::pass() {
    PASS_COUNT=$((PASS_COUNT + 1))
    echo -e "  ${GREEN}PASS${NC}: $*"
}

function drain::fail() {
    FAIL_COUNT=$((FAIL_COUNT + 1))
    echo -e "  ${RED}FAIL${NC}: $*"
}

function drain::skip() {
    SKIP_COUNT=$((SKIP_COUNT + 1))
    echo -e "  ${YELLOW}SKIP${NC}: $*"
}

function drain::section() {
    TOTAL_COUNT=$((TOTAL_COUNT + 1))
    CURRENT_TEST="$1"
    echo ""
    echo -e "${CYAN}${BOLD}[$TOTAL_COUNT] $1${NC}"
}

function drain::help() {
    cat <<EOF
Usage: $(basename "$0") -n <namespace> [-p <pod-name>] [-w <wait-seconds>]

Options:
  -n  Namespace containing the Slurm deployment (required)
  -p  Specific NodeSet pod name (auto-discovers if omitted)
  -w  Max wait time per condition in seconds (default: 30)
  -h  Show this help
EOF
}

# ---------------------------------------------------------------------------
# Prerequisite checks
# ---------------------------------------------------------------------------
function drain::check_prerequisites() {
    drain::log "Checking prerequisites..."

    if ! command -v kubectl &>/dev/null; then
        echo "ERROR: kubectl not found in PATH" >&2
        exit 1
    fi

    if ! kubectl get namespace "$NAMESPACE" &>/dev/null; then
        echo "ERROR: namespace '$NAMESPACE' not found" >&2
        exit 1
    fi

    if ! kubectl get pod -n "$NAMESPACE" slurm-controller-0 &>/dev/null; then
        echo "ERROR: slurm-controller-0 not found in namespace '$NAMESPACE'" >&2
        exit 1
    fi

    if [[ -z "$POD_NAME" ]]; then
        POD_NAME=$(kubectl get pods -n "$NAMESPACE" \
            -l "${ANNOTATION_PREFIX}/pod-index=0" \
            --field-selector=status.phase=Running \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
        if [[ -z "$POD_NAME" ]]; then
            echo "ERROR: no running NodeSet pod found. Use -p to specify one." >&2
            exit 1
        fi
    fi

    if ! kubectl get pod -n "$NAMESPACE" "$POD_NAME" &>/dev/null; then
        echo "ERROR: pod '$POD_NAME' not found in namespace '$NAMESPACE'" >&2
        exit 1
    fi

    drain::log "Prerequisites OK"
}

# ---------------------------------------------------------------------------
# Kubernetes / Slurm interaction helpers
# ---------------------------------------------------------------------------
function drain::scontrol() {
    kubectl exec -n "$NAMESPACE" slurm-controller-0 -- scontrol "$@" 2>/dev/null
}

function drain::get_slurm_node_name() {
    local name
    name=$(kubectl get pod -n "$NAMESPACE" "$1" -o jsonpath='{.spec.hostname}' 2>/dev/null)
    if [[ -z "$name" ]]; then
        name=$(kubectl get pod -n "$NAMESPACE" "$1" \
            -o jsonpath="{.metadata.labels['nodeset\.slinky\.slurm\.net/pod-hostname']}" 2>/dev/null)
    fi
    if [[ -z "$name" ]]; then
        name="$1"
    fi
    echo "$name"
}

function drain::get_k8s_node() {
    kubectl get pod -n "$NAMESPACE" "$1" -o jsonpath='{.spec.nodeName}' 2>/dev/null
}

function drain::get_pod_annotation() {
    local pod="$1" ann="$2"
    local escaped
    escaped="${ann//./\\.}"
    kubectl get pod -n "$NAMESPACE" "$pod" \
        -o jsonpath="{.metadata.annotations['${escaped}']}" 2>/dev/null
}

function drain::get_node_annotation() {
    local node="$1" ann="$2"
    local escaped
    escaped="${ann//./\\.}"
    kubectl get node "$node" \
        -o jsonpath="{.metadata.annotations['${escaped}']}" 2>/dev/null
}

function drain::is_node_cordoned() {
    local val
    val=$(kubectl get node "$1" -o jsonpath='{.spec.unschedulable}' 2>/dev/null)
    [[ "$val" == "true" ]]
}

function drain::get_slurm_node_state() {
    drain::scontrol show node "$1" | grep -oE 'State=[^ ]+' | head -1 | sed 's/^State=//'
}

function drain::get_slurm_node_reason() {
    drain::scontrol show node "$1" | sed -n 's/.*Reason=//p' | sed 's/[[:space:]]*$//' | head -1
}

# ---------------------------------------------------------------------------
# Wait / assertion helpers
# ---------------------------------------------------------------------------
function drain::wait_for_condition() {
    local description="$1"
    local check_cmd="$2"
    local max_wait="${3:-$WAIT_SECONDS}"
    local interval="${4:-5}"
    local elapsed=0

    drain::log "Waiting: ${description} (max ${max_wait}s)"
    while (( elapsed < max_wait )); do
        if eval "$check_cmd" &>/dev/null; then
            drain::log "Condition met after ${elapsed}s"
            return 0
        fi
        sleep "$interval"
        elapsed=$((elapsed + interval))
    done
    drain::log "Timed out after ${max_wait}s: ${description}"
    return 1
}

function drain::assert_eq() {
    local label="$1" expected="$2" actual="$3"
    if [[ "$expected" == "$actual" ]]; then
        return 0
    else
        drain::fail "${label}: expected='${expected}', got='${actual}'"
        return 1
    fi
}

function drain::assert_contains() {
    local label="$1" expected="$2" actual="$3"
    if [[ "$actual" == *"$expected"* ]]; then
        return 0
    else
        drain::fail "${label}: expected to contain '${expected}', got='${actual}'"
        return 1
    fi
}

function drain::assert_empty() {
    local label="$1" actual="$2"
    if [[ -z "$actual" ]]; then
        return 0
    else
        drain::fail "${label}: expected empty, got='${actual}'"
        return 1
    fi
}

function drain::assert_not_empty() {
    local label="$1" actual="$2"
    if [[ -n "$actual" ]]; then
        return 0
    else
        drain::fail "${label}: expected non-empty, got empty"
        return 1
    fi
}

# ---------------------------------------------------------------------------
# Cleanup helpers
# ---------------------------------------------------------------------------
function drain::cleanup_pod() {
    kubectl annotate pod -n "$NAMESPACE" "$POD_NAME" \
        "${ANN_POD_CORDON}-" \
        "${ANN_POD_CORDON_SOURCE}-" \
        "${ANN_POD_CORDON_REASON}-" \
        2>/dev/null || true
}

function drain::cleanup_node() {
    kubectl uncordon "$K8S_NODE" 2>/dev/null || true
    kubectl annotate node "$K8S_NODE" \
        "${ANN_NODE_CORDON_SOURCE}-" \
        "${ANN_NODE_CORDON_REASON}-" \
        2>/dev/null || true
}

function drain::cleanup_slurm() {
    drain::scontrol update nodename="$SLURM_NODE" state=resume reason="test cleanup" 2>/dev/null || true
}

function drain::check_idle_state() {
    local unschedulable cordon state
    unschedulable=$(kubectl get node "$K8S_NODE" -o jsonpath='{.spec.unschedulable}' 2>/dev/null)
    [[ "$unschedulable" != "true" ]] || return 1

    cordon=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON")
    [[ "$cordon" != "true" ]] || return 1

    state=$(drain::get_slurm_node_state "$SLURM_NODE")
    [[ "$state" == *"IDLE"* ]] || return 1

    return 0
}

function drain::cleanup_all() {
    drain::log "Cleaning up..."
    drain::cleanup_slurm
    drain::cleanup_node
    drain::cleanup_pod
    drain::wait_for_condition "system returns to idle" "drain::check_idle_state" "$WAIT_SECONDS" 5 || true
}

function drain::cleanup_trap() {
    echo ""
    drain::log "Interrupted — cleaning up..."
    drain::cleanup_all
}

# ---------------------------------------------------------------------------
# Test 1: K8s to Slurm — Basic cordon/uncordon
# ---------------------------------------------------------------------------
function drain::test_k8s_to_slurm_basic() {
    drain::section "K8s to Slurm: basic cordon/uncordon"
    local rc=0

    kubectl cordon "$K8S_NODE" >/dev/null

    drain::wait_for_condition "Slurm node drained" \
        "[[ \$(drain::get_slurm_node_state '$SLURM_NODE') == *DRAIN* ]]" || rc=1

    if (( rc == 0 )); then
        local slurm_reason pod_cordon pod_source
        slurm_reason=$(drain::get_slurm_node_reason "$SLURM_NODE")
        pod_cordon=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON")
        pod_source=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_SOURCE")

        drain::assert_contains "Slurm reason has operator prefix" "$OPERATOR_REASON_PREFIX" "$slurm_reason" || rc=1
        drain::assert_eq "pod-cordon" "true" "$pod_cordon" || rc=1
        drain::assert_eq "pod-cordon-source" "operator" "$pod_source" || rc=1
    fi

    # Uncordon and verify cleanup
    kubectl uncordon "$K8S_NODE" >/dev/null

    drain::wait_for_condition "system returns to idle" "drain::check_idle_state" || rc=1

    if (( rc == 0 )); then
        local pod_cordon_after slurm_state_after
        pod_cordon_after=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON")
        slurm_state_after=$(drain::get_slurm_node_state "$SLURM_NODE")

        drain::assert_empty "pod-cordon removed" "$pod_cordon_after" || rc=1
        drain::assert_contains "Slurm idle" "IDLE" "$slurm_state_after" || rc=1
    fi

    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; else drain::cleanup_all; fi
}

# ---------------------------------------------------------------------------
# Test 2: K8s to Slurm — Custom drain reason
# ---------------------------------------------------------------------------
function drain::test_k8s_to_slurm_custom_reason() {
    drain::section "K8s to Slurm: custom drain reason"
    local rc=0

    kubectl annotate node "$K8S_NODE" "${ANN_NODE_CORDON_REASON}=GPU XID error" >/dev/null
    kubectl cordon "$K8S_NODE" >/dev/null

    drain::wait_for_condition "Slurm node drained" \
        "[[ \$(drain::get_slurm_node_state '$SLURM_NODE') == *DRAIN* ]]" || rc=1

    if (( rc == 0 )); then
        local slurm_reason pod_reason
        slurm_reason=$(drain::get_slurm_node_reason "$SLURM_NODE")
        pod_reason=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_REASON")

        drain::assert_contains "Slurm reason" "${OPERATOR_REASON_PREFIX} GPU XID error" "$slurm_reason" || rc=1
        drain::assert_eq "pod-cordon-reason" "GPU XID error" "$pod_reason" || rc=1
    fi

    drain::cleanup_all
    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; fi
}

# ---------------------------------------------------------------------------
# Test 3: K8s to Slurm — Reason update while cordoned
# ---------------------------------------------------------------------------
function drain::test_k8s_to_slurm_reason_update() {
    drain::section "K8s to Slurm: reason update while cordoned"
    local rc=0

    kubectl cordon "$K8S_NODE" >/dev/null
    drain::wait_for_condition "Slurm node drained" \
        "[[ \$(drain::get_slurm_node_state '$SLURM_NODE') == *DRAIN* ]]" || rc=1

    # Update reason
    kubectl annotate node "$K8S_NODE" "${ANN_NODE_CORDON_REASON}=updated reason" --overwrite >/dev/null

    drain::wait_for_condition "Slurm reason updated" \
        "[[ \$(drain::get_slurm_node_reason '$SLURM_NODE') == *'${OPERATOR_REASON_PREFIX} updated reason'* ]]" || rc=1

    if (( rc == 0 )); then
        local slurm_reason
        slurm_reason=$(drain::get_slurm_node_reason "$SLURM_NODE")
        drain::assert_contains "Slurm reason" "${OPERATOR_REASON_PREFIX} updated reason" "$slurm_reason" || rc=1
    fi

    drain::cleanup_all
    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; fi
}

# ---------------------------------------------------------------------------
# Test 4: Slurm to K8s — External drain/undrain
# ---------------------------------------------------------------------------
function drain::test_slurm_to_k8s_drain() {
    drain::section "Slurm to K8s: external drain/undrain"
    local rc=0

    drain::scontrol update nodename="$SLURM_NODE" state=drain reason="maintenance window"

    drain::wait_for_condition "K8s node cordoned" \
        "drain::is_node_cordoned '$K8S_NODE'" || rc=1

    if (( rc == 0 )); then
        local node_source node_reason pod_cordon pod_source pod_reason
        node_source=$(drain::get_node_annotation "$K8S_NODE" "$ANN_NODE_CORDON_SOURCE")
        node_reason=$(drain::get_node_annotation "$K8S_NODE" "$ANN_NODE_CORDON_REASON")
        pod_cordon=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON")
        pod_source=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_SOURCE")
        pod_reason=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_REASON")

        drain::assert_eq "node-cordon-source" "slurm" "$node_source" || rc=1
        drain::assert_eq "node-cordon-reason" "maintenance window" "$node_reason" || rc=1
        drain::assert_eq "pod-cordon" "true" "$pod_cordon" || rc=1
        drain::assert_eq "pod-cordon-source" "slurm" "$pod_source" || rc=1
        drain::assert_eq "pod-cordon-reason" "maintenance window" "$pod_reason" || rc=1
    fi

    # Undrain and verify cleanup
    drain::scontrol update nodename="$SLURM_NODE" state=resume reason="done"

    drain::wait_for_condition "system returns to idle" "drain::check_idle_state" || rc=1

    if (( rc == 0 )); then
        local node_source_after pod_cordon_after
        node_source_after=$(drain::get_node_annotation "$K8S_NODE" "$ANN_NODE_CORDON_SOURCE")
        pod_cordon_after=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON")

        drain::assert_empty "node-cordon-source removed" "$node_source_after" || rc=1
        drain::assert_empty "pod-cordon removed" "$pod_cordon_after" || rc=1
    fi

    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; else drain::cleanup_all; fi
}

# ---------------------------------------------------------------------------
# Test 5: Slurm to K8s — Reason update while drained
# ---------------------------------------------------------------------------
function drain::test_slurm_to_k8s_reason_update() {
    drain::section "Slurm to K8s: reason update while drained"
    local rc=0

    drain::scontrol update nodename="$SLURM_NODE" state=drain reason="initial reason"
    drain::wait_for_condition "K8s node cordoned" \
        "drain::is_node_cordoned '$K8S_NODE'" || rc=1

    # Update reason
    drain::scontrol update nodename="$SLURM_NODE" state=drain reason="updated reason"

    drain::wait_for_condition "pod reason updated" \
        "[[ \$(drain::get_pod_annotation '$POD_NAME' '$ANN_POD_CORDON_REASON') == 'updated reason' ]]" || rc=1

    if (( rc == 0 )); then
        local node_reason pod_reason
        node_reason=$(drain::get_node_annotation "$K8S_NODE" "$ANN_NODE_CORDON_REASON")
        pod_reason=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_REASON")

        drain::assert_eq "node-cordon-reason" "updated reason" "$node_reason" || rc=1
        drain::assert_eq "pod-cordon-reason" "updated reason" "$pod_reason" || rc=1
    fi

    drain::cleanup_all
    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; fi
}

# ---------------------------------------------------------------------------
# Test 6: Slurm to K8s — scontrol resume cleans up K8s
# ---------------------------------------------------------------------------
function drain::test_slurm_to_k8s_resume_cleanup() {
    drain::section "Slurm to K8s: scontrol resume cleans up K8s"
    local rc=0

    drain::scontrol update nodename="$SLURM_NODE" state=drain reason="cleanup test"
    drain::wait_for_condition "K8s node cordoned" \
        "drain::is_node_cordoned '$K8S_NODE'" || rc=1

    drain::scontrol update nodename="$SLURM_NODE" state=resume reason="done"

    drain::wait_for_condition "system returns to idle" "drain::check_idle_state" || rc=1

    if (( rc == 0 )); then
        local node_source node_reason pod_cordon pod_source pod_reason
        node_source=$(drain::get_node_annotation "$K8S_NODE" "$ANN_NODE_CORDON_SOURCE")
        node_reason=$(drain::get_node_annotation "$K8S_NODE" "$ANN_NODE_CORDON_REASON")
        pod_cordon=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON")
        pod_source=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_SOURCE")
        pod_reason=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_REASON")

        drain::assert_empty "node-cordon-source removed" "$node_source" || rc=1
        drain::assert_empty "node-cordon-reason removed" "$node_reason" || rc=1
        drain::assert_empty "pod-cordon removed" "$pod_cordon" || rc=1
        drain::assert_empty "pod-cordon-source removed" "$pod_source" || rc=1
        drain::assert_empty "pod-cordon-reason removed" "$pod_reason" || rc=1
    fi

    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; else drain::cleanup_all; fi
}

# ---------------------------------------------------------------------------
# Test 7: Priority — K8s cordon wins over scontrol resume
# ---------------------------------------------------------------------------
function drain::test_priority_k8s_wins() {
    drain::section "Priority: K8s cordon wins over scontrol resume"
    local rc=0

    kubectl cordon "$K8S_NODE" >/dev/null
    drain::wait_for_condition "Slurm node drained" \
        "[[ \$(drain::get_slurm_node_state '$SLURM_NODE') == *DRAIN* ]]" || rc=1

    # Try to resume Slurm — operator should re-drain
    drain::scontrol update nodename="$SLURM_NODE" state=resume reason="attempt"

    # Wait for operator to re-drain
    sleep 10
    drain::wait_for_condition "Slurm re-drained by operator" \
        "[[ \$(drain::get_slurm_node_state '$SLURM_NODE') == *DRAIN* ]]" || rc=1

    if (( rc == 0 )); then
        local slurm_reason pod_source
        slurm_reason=$(drain::get_slurm_node_reason "$SLURM_NODE")
        pod_source=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_SOURCE")

        drain::assert_contains "Slurm reason has operator prefix" "$OPERATOR_REASON_PREFIX" "$slurm_reason" || rc=1
        drain::assert_eq "pod-cordon-source stays operator" "operator" "$pod_source" || rc=1
    fi

    drain::cleanup_all
    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; fi
}

# ---------------------------------------------------------------------------
# Test 8: Priority — K8s cordon overrides external Slurm drain
# ---------------------------------------------------------------------------
function drain::test_priority_k8s_overrides_slurm() {
    drain::section "Priority: K8s cordon overrides external Slurm drain"
    local rc=0

    # K8s cordon first
    kubectl cordon "$K8S_NODE" >/dev/null
    drain::wait_for_condition "Slurm node drained" \
        "[[ \$(drain::get_slurm_node_state '$SLURM_NODE') == *DRAIN* ]]" || rc=1

    # External Slurm drain overwrites the reason
    drain::scontrol update nodename="$SLURM_NODE" state=drain reason="external override"

    # Wait for operator to re-drain with its prefix
    drain::wait_for_condition "operator re-drains with prefix" \
        "[[ \$(drain::get_slurm_node_reason '$SLURM_NODE') == ${OPERATOR_REASON_PREFIX}* ]]" || rc=1

    if (( rc == 0 )); then
        local pod_source
        pod_source=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_SOURCE")
        drain::assert_eq "pod-cordon-source stays operator" "operator" "$pod_source" || rc=1
    fi

    drain::cleanup_all
    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; fi
}

# ---------------------------------------------------------------------------
# Test 9: Loop prevention — External Slurm reason not overwritten
# ---------------------------------------------------------------------------
function drain::test_loop_prevention() {
    drain::section "Loop prevention: external Slurm reason not overwritten"
    local rc=0

    drain::scontrol update nodename="$SLURM_NODE" state=drain reason="external maintenance"
    drain::wait_for_condition "K8s node cordoned" \
        "drain::is_node_cordoned '$K8S_NODE'" || rc=1

    if (( rc == 0 )); then
        local slurm_reason
        slurm_reason=$(drain::get_slurm_node_reason "$SLURM_NODE")
        drain::assert_contains "Slurm reason unchanged" "external maintenance" "$slurm_reason" || rc=1
    fi

    # Wait an extra reconcile cycle and re-check
    sleep "$((WAIT_SECONDS))"

    if (( rc == 0 )); then
        local slurm_reason_after
        slurm_reason_after=$(drain::get_slurm_node_reason "$SLURM_NODE")
        drain::assert_contains "Slurm reason still unchanged" "external maintenance" "$slurm_reason_after" || rc=1
    fi

    drain::cleanup_all
    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; fi
}

# ---------------------------------------------------------------------------
# Test 10: Pod-level — Sourceless pod-cordon is cleaned up
# ---------------------------------------------------------------------------
function drain::test_pod_direct_cordon() {
    drain::section "Pod-level: sourceless pod-cordon is cleaned up"
    local rc=0

    # Setting pod-cordon=true without a pod-cordon-source is not a supported
    # cordon path. syncPodUncordon treats sourceless cordons as stale and
    # removes them. Verify the operator cleans it up automatically.
    kubectl annotate pod -n "$NAMESPACE" "$POD_NAME" "${ANN_POD_CORDON}=true" >/dev/null

    drain::wait_for_condition "operator removes sourceless pod-cordon" \
        "[[ \$(drain::get_pod_annotation '$POD_NAME' '$ANN_POD_CORDON') != 'true' ]]" || rc=1

    if (( rc == 0 )); then
        local slurm_state
        slurm_state=$(drain::get_slurm_node_state "$SLURM_NODE")
        drain::assert_contains "Slurm node stays idle" "IDLE" "$slurm_state" || rc=1
    fi

    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; else drain::cleanup_all; fi
}

# ---------------------------------------------------------------------------
# Test 11: Scale-in — Replica reduction uses scale-in source
# ---------------------------------------------------------------------------
function drain::test_scale_in() {
    drain::section "Scale-in: replica reduction uses scale-in source"
    local rc=0

    # Get current replicas
    local replicas
    replicas=$(kubectl get nodeset -n "$NAMESPACE" "$NODESET_NAME" \
        -o jsonpath='{.spec.replicas}' 2>/dev/null)

    if [[ -z "$replicas" ]] || (( replicas < 2 )); then
        drain::skip "requires >= 2 replicas (current: ${replicas:-unknown})"
        return
    fi

    local new_replicas=$((replicas - 1))
    # The highest-ordinal pod will be condemned
    local condemned_index=$((replicas - 1))
    local condemned_pod
    condemned_pod=$(kubectl get pods -n "$NAMESPACE" \
        -l "${ANNOTATION_PREFIX}/pod-index=${condemned_index}" \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

    if [[ -z "$condemned_pod" ]]; then
        drain::skip "could not find pod with index ${condemned_index}"
        return
    fi

    kubectl patch nodeset -n "$NAMESPACE" "$NODESET_NAME" \
        --type merge -p "{\"spec\":{\"replicas\":${new_replicas}}}" >/dev/null

    drain::wait_for_condition "condemned pod gets scale-in source" \
        "[[ \$(drain::get_pod_annotation '$condemned_pod' '$ANN_POD_CORDON_SOURCE') == 'scale-in' ]]" || rc=1

    if (( rc == 0 )); then
        local source
        source=$(drain::get_pod_annotation "$condemned_pod" "$ANN_POD_CORDON_SOURCE")
        drain::assert_eq "pod-cordon-source" "scale-in" "$source" || rc=1
    fi

    # Restore replicas
    kubectl patch nodeset -n "$NAMESPACE" "$NODESET_NAME" \
        --type merge -p "{\"spec\":{\"replicas\":${replicas}}}" >/dev/null

    drain::wait_for_condition "replicas restored" \
        "[[ \$(kubectl get nodeset -n '$NAMESPACE' '$NODESET_NAME' -o jsonpath='{.status.readyReplicas}') == '$replicas' ]]" \
        "$((WAIT_SECONDS * 3))" || rc=1

    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; fi
}

# ---------------------------------------------------------------------------
# Test 12: Partial failure — Manual pod annotation removal
# ---------------------------------------------------------------------------
function drain::test_partial_failure_pod_removal() {
    drain::section "Partial failure: pod annotation removal recovery"
    local rc=0

    drain::scontrol update nodename="$SLURM_NODE" state=drain reason="partial failure test"
    drain::wait_for_condition "pod cordoned with slurm source" \
        "[[ \$(drain::get_pod_annotation '$POD_NAME' '$ANN_POD_CORDON_SOURCE') == 'slurm' ]]" || rc=1

    # Simulate partial failure: remove pod annotations
    drain::cleanup_pod

    # Wait for operator to re-apply (Slurm is still drained)
    drain::wait_for_condition "operator re-applies pod cordon" \
        "[[ \$(drain::get_pod_annotation '$POD_NAME' '$ANN_POD_CORDON') == 'true' ]]" || rc=1

    if (( rc == 0 )); then
        local pod_source
        pod_source=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_SOURCE")
        drain::assert_eq "pod-cordon-source re-applied" "slurm" "$pod_source" || rc=1
    fi

    drain::cleanup_all
    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; fi
}

# ---------------------------------------------------------------------------
# Test 13: Partial failure — Node stuck after partial cleanup
# ---------------------------------------------------------------------------
function drain::test_partial_failure_node_stuck() {
    drain::section "Partial failure: node stuck after partial cleanup"
    local rc=0

    drain::scontrol update nodename="$SLURM_NODE" state=drain reason="stuck node test"
    drain::wait_for_condition "K8s node cordoned" \
        "drain::is_node_cordoned '$K8S_NODE'" || rc=1

    # Simulate partial failure: remove pod annotations but leave node stuck
    drain::cleanup_pod

    # Resume Slurm
    drain::scontrol update nodename="$SLURM_NODE" state=resume reason="done"

    # Node should be uncordoned even though pod annotations were already gone
    drain::wait_for_condition "K8s node uncordoned" \
        "! drain::is_node_cordoned '$K8S_NODE'" || rc=1

    if (( rc == 0 )); then
        local node_source
        node_source=$(drain::get_node_annotation "$K8S_NODE" "$ANN_NODE_CORDON_SOURCE")
        drain::assert_empty "node-cordon-source removed" "$node_source" || rc=1
    fi

    drain::cleanup_all
    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; fi
}

# ---------------------------------------------------------------------------
# Test 14: Idempotency — Multiple reconciles are no-ops
# ---------------------------------------------------------------------------
function drain::test_idempotency() {
    drain::section "Idempotency: multiple reconciles produce no spurious patches"
    local rc=0

    kubectl cordon "$K8S_NODE" >/dev/null
    drain::wait_for_condition "Slurm node drained" \
        "[[ \$(drain::get_slurm_node_state '$SLURM_NODE') == *DRAIN* ]]" || rc=1

    # Let state settle
    sleep 10

    # Snapshot
    local snap_cordon snap_source snap_reason snap_slurm_reason
    snap_cordon=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON")
    snap_source=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_SOURCE")
    snap_reason=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_REASON")
    snap_slurm_reason=$(drain::get_slurm_node_reason "$SLURM_NODE")

    # Trigger reconciles via harmless label changes
    kubectl label pod -n "$NAMESPACE" "$POD_NAME" drain-test-trigger=1 --overwrite >/dev/null 2>&1 || true
    sleep 10
    kubectl label pod -n "$NAMESPACE" "$POD_NAME" drain-test-trigger=2 --overwrite >/dev/null 2>&1 || true
    sleep 10

    # Compare
    local cur_cordon cur_source cur_reason cur_slurm_reason
    cur_cordon=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON")
    cur_source=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_SOURCE")
    cur_reason=$(drain::get_pod_annotation "$POD_NAME" "$ANN_POD_CORDON_REASON")
    cur_slurm_reason=$(drain::get_slurm_node_reason "$SLURM_NODE")

    drain::assert_eq "pod-cordon unchanged" "$snap_cordon" "$cur_cordon" || rc=1
    drain::assert_eq "pod-cordon-source unchanged" "$snap_source" "$cur_source" || rc=1
    drain::assert_eq "pod-cordon-reason unchanged" "$snap_reason" "$cur_reason" || rc=1
    drain::assert_eq "slurm reason unchanged" "$snap_slurm_reason" "$cur_slurm_reason" || rc=1

    # Cleanup labels
    kubectl label pod -n "$NAMESPACE" "$POD_NAME" drain-test-trigger- >/dev/null 2>&1 || true

    drain::cleanup_all
    if (( rc == 0 )); then drain::pass "$CURRENT_TEST"; fi
}

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
function drain::summary() {
    echo ""
    echo "======================================"
    echo "  Drain Sync Test Results"
    echo "======================================"
    echo -e "  ${GREEN}PASSED:  ${PASS_COUNT}${NC}"
    echo -e "  ${RED}FAILED:  ${FAIL_COUNT}${NC}"
    echo -e "  ${YELLOW}SKIPPED: ${SKIP_COUNT}${NC}"
    echo "  TOTAL:   ${TOTAL_COUNT}"
    echo "======================================"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
function drain::main() {
    while getopts "n:p:w:h" opt; do
        case "$opt" in
            n) NAMESPACE="$OPTARG" ;;
            p) POD_NAME="$OPTARG" ;;
            w) WAIT_SECONDS="$OPTARG" ;;
            h) drain::help; exit 0 ;;
            *) drain::help; exit 1 ;;
        esac
    done

    if [[ -z "$NAMESPACE" ]]; then
        echo "ERROR: -n <namespace> is required" >&2
        drain::help
        exit 1
    fi

    drain::check_prerequisites

    K8S_NODE=$(drain::get_k8s_node "$POD_NAME")
    SLURM_NODE=$(drain::get_slurm_node_name "$POD_NAME")
    NODESET_NAME=$(kubectl get pod -n "$NAMESPACE" "$POD_NAME" \
        -o jsonpath='{.metadata.ownerReferences[0].name}' 2>/dev/null)

    echo ""
    echo "======================================"
    echo "  Drain Sync Test Configuration"
    echo "======================================"
    echo "  Namespace:    $NAMESPACE"
    echo "  Pod:          $POD_NAME"
    echo "  Slurm Node:   $SLURM_NODE"
    echo "  K8s Node:     $K8S_NODE"
    echo "  NodeSet:      $NODESET_NAME"
    echo "  Wait (s):     $WAIT_SECONDS"
    echo "======================================"

    trap drain::cleanup_trap EXIT

    # Ensure clean starting state
    drain::cleanup_all

    # K8s to Slurm
    drain::test_k8s_to_slurm_basic
    drain::test_k8s_to_slurm_custom_reason
    drain::test_k8s_to_slurm_reason_update

    # Slurm to K8s
    drain::test_slurm_to_k8s_drain
    drain::test_slurm_to_k8s_reason_update
    drain::test_slurm_to_k8s_resume_cleanup

    # Priority
    drain::test_priority_k8s_wins
    drain::test_priority_k8s_overrides_slurm

    # Loop prevention
    drain::test_loop_prevention

    # Pod-level
    drain::test_pod_direct_cordon

    # Scale-in
    drain::test_scale_in

    # Partial failure recovery
    drain::test_partial_failure_pod_removal
    drain::test_partial_failure_node_stuck

    # Idempotency
    drain::test_idempotency

    trap - EXIT
    drain::cleanup_all
    drain::summary

    if (( FAIL_COUNT > 0 )); then
        exit 1
    fi
    exit 0
}

drain::main "$@"
