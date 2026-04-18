// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	PartitionKind = "Partition"
)

var (
	PartitionGVK        = GroupVersion.WithKind(PartitionKind)
	PartitionAPIVersion = GroupVersion.String()
)

// PartitionSpec defines the desired state of a Slurm partition.
type PartitionSpec struct {
	// controllerRef is a reference to the Controller CR to which this partition belongs.
	// +required
	ControllerRef ObjectReference `json:"controllerRef"`

	// nodes is the value for the Nodes= parameter in the partition line.
	// Can be NodeSet names, hostlists, or "ALL".
	// Ref: https://slurm.schedmd.com/slurm.conf.html#OPT_Nodes_1
	// +required
	Nodes string `json:"nodes"`

	// default controls whether this is the default partition.
	// Ref: https://slurm.schedmd.com/slurm.conf.html#OPT_Default
	// +optional
	// +default:=false
	Default bool `json:"default,omitzero"`

	// maxTime sets the maximum time limit for jobs in this partition.
	// Ref: https://slurm.schedmd.com/slurm.conf.html#OPT_MaxTime_1
	// +optional
	MaxTime string `json:"maxTime,omitzero"`

	// state sets the partition state.
	// Ref: https://slurm.schedmd.com/slurm.conf.html#OPT_State
	// +optional
	// +kubebuilder:validation:Enum=UP;DOWN;DRAIN;INACTIVE
	State string `json:"state,omitzero"`

	// config is raw extra parameters appended to the partition line.
	// Ref: https://slurm.schedmd.com/slurm.conf.html#SECTION_PARTITION-CONFIGURATION
	// +optional
	Config string `json:"config,omitzero"`
}

// PartitionStatus defines the observed state of Partition.
type PartitionStatus struct {
	// Represents the latest available observations of a Partition's current state.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=slurmpart
// +kubebuilder:printcolumn:name="NODES",type="string",JSONPath=".spec.nodes",description="The nodes assigned to this partition."
// +kubebuilder:printcolumn:name="DEFAULT",type="boolean",JSONPath=".spec.default",description="Whether this is the default partition."
// +kubebuilder:printcolumn:name="STATE",type="string",JSONPath=".spec.state",description="The partition state."
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"

// Partition is the Schema for the partitions API
type Partition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PartitionSpec   `json:"spec,omitempty"`
	Status PartitionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PartitionList contains a list of Partition
type PartitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Partition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Partition{}, &PartitionList{})
}
