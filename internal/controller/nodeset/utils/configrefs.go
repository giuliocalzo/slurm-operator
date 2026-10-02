// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"strconv"

	slinkyv1beta1 "github.com/SlinkyProject/slurm-operator/api/v1beta1"
	"github.com/SlinkyProject/slurm-operator/internal/utils/configrefs"
)

// RolloutOnConfigChange reports whether the NodeSet opted in to rolling its pods
// when the content of their ConfigMaps and Secrets changes.
func RolloutOnConfigChange(nodeset *slinkyv1beta1.NodeSet) bool {
	enabled, _ := strconv.ParseBool(nodeset.Annotations[slinkyv1beta1.AnnotationRolloutOnConfigChange])
	return enabled
}

// ConfigRefs returns the user-supplied ConfigMaps and Secrets consumed by the
// NodeSet's pods: those in the pod template, the slurmd and logfile containers,
// and the SSSD config Secret when SSH is enabled. Operator-managed objects (the
// slurm.key Secret and the SSH config ConfigMap) are excluded.
func ConfigRefs(nodeset *slinkyv1beta1.NodeSet) []configrefs.Ref {
	refs := [][]configrefs.Ref{
		configrefs.PodSpecRefs(&nodeset.Spec.Template.PodSpecWrapper.PodSpec),
		configrefs.ContainerRefs(&nodeset.Spec.Slurmd.Container),
		configrefs.ContainerRefs(&nodeset.Spec.LogFile.Container),
	}
	if nodeset.Spec.Ssh.Enabled {
		refs = append(refs, []configrefs.Ref{configrefs.Secret(nodeset.SssdSecretKey().Name)})
	}
	return configrefs.Merge(refs...)
}
