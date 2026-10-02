// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package indexes

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	slinkyv1beta1 "github.com/SlinkyProject/slurm-operator/api/v1beta1"
)

func init() {
	utilruntime.Must(slinkyv1beta1.AddToScheme(scheme.Scheme))
}

func Test_getPodNodeName(t *testing.T) {
	tests := []struct {
		name string
		o    client.Object
		want []string
	}{
		{
			name: "Pod",
			o:    &corev1.Pod{},
			want: []string{""},
		},
		{
			name: "Pod, with nodeName",
			o: &corev1.Pod{
				Spec: corev1.PodSpec{
					NodeName: "foo",
				},
			},
			want: []string{"foo"},
		},
		{
			name: "invalid",
			o:    &corev1.Node{},
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getPodNodeName(tt.o)
			require.True(t, apiequality.Semantic.DeepEqual(got, tt.want), "getPodNodeName() = %v, want %v", got, tt.want)
		})
	}
}

func Test_getNodeSetConfigRefs(t *testing.T) {
	newNodeSet := func(annotations map[string]string) *slinkyv1beta1.NodeSet {
		nodeset := &slinkyv1beta1.NodeSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "foo",
				Annotations: annotations,
			},
		}
		nodeset.Spec.Template.PodSpecWrapper.Volumes = []corev1.Volume{
			{Name: "cm", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "my-config"}},
			}},
		}
		nodeset.Spec.Slurmd.Env = []corev1.EnvVar{
			{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "my-secret"}, Key: "token"},
			}},
		}
		return nodeset
	}
	tests := []struct {
		name string
		o    client.Object
		want []string
	}{
		{
			name: "opted in",
			o:    newNodeSet(map[string]string{slinkyv1beta1.AnnotationRolloutOnConfigChange: "true"}),
			want: []string{"ConfigMap/my-config", "Secret/my-secret"},
		},
		{
			name: "not opted in",
			o:    newNodeSet(nil),
			want: []string{},
		},
		{
			name: "opted out",
			o:    newNodeSet(map[string]string{slinkyv1beta1.AnnotationRolloutOnConfigChange: "false"}),
			want: []string{},
		},
		{
			name: "invalid",
			o:    &corev1.Pod{},
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getNodeSetConfigRefs(tt.o)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNewFakeClientBuilderWithIndexes(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			name: "smoke",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewFakeClientBuilderWithIndexes()
			require.NotNil(t, got)
		})
	}
}
