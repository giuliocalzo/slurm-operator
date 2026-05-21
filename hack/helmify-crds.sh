#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_DIR="${ROOT_DIR}/config/crd/bases"
DST_DIR="${ROOT_DIR}/helm/slurm-operator/templates/crds"

mkdir -p "${DST_DIR}"

for src_file in "${SRC_DIR}"/*.yaml; do
	base="$(basename "${src_file}")"
	dest="${DST_DIR}/${base}"

	crd_name="$(grep '^  name:' "${src_file}" | awk '{print $2}')"
	cg_version="$(grep 'controller-gen.kubebuilder.io/version:' "${src_file}" | awk '{print $2}' || true)"

	{
		cat <<'EOF'
{{- if .Values.crds.install }}
EOF
		grep -E '^(apiVersion|kind):' "${src_file}"
		cat <<'EOF'
metadata:
  annotations:
    {{- if .Values.crds.keep }}
    "helm.sh/resource-policy": keep
    {{- end }}
EOF
		if [[ -n ${cg_version} ]]; then
			echo "    controller-gen.kubebuilder.io/version: ${cg_version}"
		fi
		cat <<'EOF'
    {{- with .Values.crds.annotations }}
      {{- toYaml . | nindent 4 }}
    {{- end }}
  {{- with .Values.crds.additionalLabels }}
  labels:
    {{- toYaml . | nindent 4 }}
  {{- end }}
EOF
		echo "  name: ${crd_name}"
		awk '/^spec:/{found=1} found' "${src_file}"
		cat <<'EOF'
{{- end }}
EOF
	} >"${dest}"

	echo "wrote ${dest}"
done
