// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

// Prefixes
const (
	SlinkyPrefix = "slinky.slurm.net/"

	NodeSetPrefix  = "nodeset." + SlinkyPrefix
	LoginSetPrefix = "loginset." + SlinkyPrefix
	TopologyPrefix = "topology." + SlinkyPrefix
)

// Well Known Annotations
const (
	// AnnotationPodCordon indicates NodeSet Pods that should be DRAIN[ING|ED] in Slurm.
	AnnotationPodCordon = NodeSetPrefix + "pod-cordon"

	// LabelPodDeletionCost can be used to set to an int32 that represent the cost of deleting a pod compared to other
	// pods belonging to the same ReplicaSet. Pods with lower deletion cost are preferred to be deleted before pods
	// with higher deletion cost.
	// NOTE: this is honored on a best-effort basis, and does not offer guarantees on pod deletion order.
	// The implicit deletion cost for pods that don't set the annotation is 0, negative values are permitted.
	AnnotationPodDeletionCost = NodeSetPrefix + "pod-deletion-cost"

	// AnnotationPodDeadline stores a time.RFC3339 timestamp, indicating when the Slurm node should complete its running
	// workload by. Pods with an earlier deadline are preferred to be deleted before pods with a later deadline.
	// NOTE: this is honored on a best-effort basis, and does not offer guarantees on pod deletion order.
	AnnotationPodDeadline = NodeSetPrefix + "pod-deadline"
)

// Well Known Annotations for drain source tracking
const (
	// AnnotationPodCordonSource indicates the origin of the pod cordon.
	// When set to "slurm", the cordon was initiated from an external Slurm drain
	// and should not cause the operator to re-drain the Slurm node.
	// When set to "operator", the cordon was initiated from the Kubernetes side
	// (node cordon) and takes priority over Slurm drain state.
	AnnotationPodCordonSource = NodeSetPrefix + "pod-cordon-source"

	// AnnotationPodCordonReason stores a human-readable reason associated with
	// the pod cordon. It is set for all cordon sources (Slurm drains, operator
	// node cordons, scale-in) to support deduplication and reason propagation.
	AnnotationPodCordonReason = NodeSetPrefix + "pod-cordon-reason"

	// PodCordonSourceSlurm is the value of AnnotationPodCordonSource when the
	// cordon originates from an external Slurm drain (not managed by the operator).
	PodCordonSourceSlurm = "slurm"

	// PodCordonSourceOperator is the value of AnnotationPodCordonSource when the
	// cordon originates from a Kubernetes node cordon detected by the operator.
	PodCordonSourceOperator = "operator"

	// PodCordonSourceScaleIn is the value of AnnotationPodCordonSource when the
	// cordon originates from scale-in. These pods are fully managed by
	// processCondemned and should be skipped by syncCordon.
	PodCordonSourceScaleIn = "scale-in"
)

// Well Known Annotations for Objects of type corev1.Node
const (
	// AnnotationNodeCordonReason stores the drain reason on the Kubernetes node.
	// In the K8s → Slurm direction it can be set by the user before cordoning to
	// override the default Slurm drain reason.
	// In the Slurm → K8s direction it is set by the operator to reflect the
	// external Slurm drain reason.
	AnnotationNodeCordonReason = NodeSetPrefix + "node-cordon-reason"

	// AnnotationNodeCordonSource indicates who cordoned the Kubernetes node.
	// When set to "slurm", the operator cordoned the node in response to an
	// external Slurm drain.
	AnnotationNodeCordonSource = NodeSetPrefix + "node-cordon-source"

	// NodeCordonSourceSlurm is the value of AnnotationNodeCordonSource when the
	// operator cordoned the Kubernetes node due to an external Slurm drain.
	NodeCordonSourceSlurm = "slurm"

	// AnnotationNodeTopologyLine indicates the Slurm dynamic topology line (e.g. "topo-switch:s2,topo-block:b2").
	// Ref: https://slurm.schedmd.com/topology.html#dynamic_topo
	AnnotationNodeTopologyLine = TopologyPrefix + "line"
)

// Well Known Labels
const (
	// LabelNodeSetPodName indicates the pod name.
	// NOTE: Set by the NodeSet controller.
	LabelNodeSetPodName = NodeSetPrefix + "pod-name"

	// LabelNodeSetPodIndex indicates the pod's ordinal.
	// NOTE: Set by the NodeSet controller.
	LabelNodeSetPodIndex = NodeSetPrefix + "pod-index"

	// LabelNodeSetPodHostname indicates the pod hostname (used as Slurm node name).
	// NOTE: Set by the NodeSet controller.
	LabelNodeSetPodHostname = NodeSetPrefix + "pod-hostname"

	// LabelNodeSetPodProtect indicates whether the pod is protected against eviction using a PodDisruptionBudget
	// NOTE: Set by the NodeSet controller
	LabelNodeSetPodProtect = NodeSetPrefix + "pod-protect"
)
