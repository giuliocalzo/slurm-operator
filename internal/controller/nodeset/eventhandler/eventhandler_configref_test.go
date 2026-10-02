// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package eventhandler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	slinkyv1beta1 "github.com/SlinkyProject/slurm-operator/api/v1beta1"
	"github.com/SlinkyProject/slurm-operator/internal/controller/nodeset/indexes"
)

func newConfigRefNodeSet(name string, rollout bool) *slinkyv1beta1.NodeSet {
	nodeset := newNodeSet(name, "slurm", 1)
	if rollout {
		nodeset.Annotations = map[string]string{
			slinkyv1beta1.AnnotationRolloutOnConfigChange: "true",
		}
	}
	nodeset.Spec.Template.PodSpecWrapper.Volumes = []corev1.Volume{
		{Name: "config", VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "my-config"}},
		}},
	}
	nodeset.Spec.Slurmd.EnvFrom = []corev1.EnvFromSource{
		{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "my-secret"}}},
	}
	return nodeset
}

func newConfigMap(name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: corev1.NamespaceDefault, Name: name},
		Data:       data,
	}
}

func newSecret(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: corev1.NamespaceDefault, Name: name},
		Data:       data,
	}
}

func newConfigRefReader() client.Reader {
	return indexes.NewFakeClientBuilderWithIndexes(
		newConfigRefNodeSet("opted-in", true),
		newConfigRefNodeSet("not-opted-in", false),
	).Build()
}

func Test_ConfigRefEventHandler_Create(t *testing.T) {
	tests := []struct {
		name string
		obj  client.Object
		want int
	}{
		{
			name: "referenced ConfigMap",
			obj:  newConfigMap("my-config", nil),
			want: 1,
		},
		{
			name: "referenced Secret",
			obj:  newSecret("my-secret", nil),
			want: 1,
		},
		{
			name: "Secret with the name of a referenced ConfigMap",
			obj:  newSecret("my-config", nil),
			want: 0,
		},
		{
			name: "unreferenced ConfigMap",
			obj:  newConfigMap("other", nil),
			want: 0,
		},
		{
			name: "referenced ConfigMap in another namespace",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "my-config"},
			},
			want: 0,
		},
		{
			name: "unsupported type",
			obj:  &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: corev1.NamespaceDefault, Name: "my-config"}},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newQueue()
			h := NewConfigRefEventHandler(newConfigRefReader())
			h.Create(context.TODO(), event.CreateEvent{Object: tt.obj}, q)
			require.Equal(t, tt.want, q.Len())
		})
	}
}

func Test_ConfigRefEventHandler_Update(t *testing.T) {
	tests := []struct {
		name   string
		oldObj client.Object
		newObj client.Object
		want   int
	}{
		{
			name:   "ConfigMap data changed",
			oldObj: newConfigMap("my-config", map[string]string{"a": "1"}),
			newObj: newConfigMap("my-config", map[string]string{"a": "2"}),
			want:   1,
		},
		{
			name:   "ConfigMap binaryData changed",
			oldObj: newConfigMap("my-config", nil),
			newObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: corev1.NamespaceDefault, Name: "my-config"},
				BinaryData: map[string][]byte{"bin": {0x1}},
			},
			want: 1,
		},
		{
			name:   "ConfigMap metadata only",
			oldObj: newConfigMap("my-config", map[string]string{"a": "1"}),
			newObj: func() client.Object {
				cm := newConfigMap("my-config", map[string]string{"a": "1"})
				cm.Labels = map[string]string{"foo": "bar"}
				return cm
			}(),
			want: 0,
		},
		{
			name:   "Secret data changed",
			oldObj: newSecret("my-secret", map[string][]byte{"a": []byte("1")}),
			newObj: newSecret("my-secret", map[string][]byte{"a": []byte("2")}),
			want:   1,
		},
		{
			name:   "Secret unchanged",
			oldObj: newSecret("my-secret", map[string][]byte{"a": []byte("1")}),
			newObj: newSecret("my-secret", map[string][]byte{"a": []byte("1")}),
			want:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newQueue()
			h := NewConfigRefEventHandler(newConfigRefReader())
			h.Update(context.TODO(), event.UpdateEvent{ObjectOld: tt.oldObj, ObjectNew: tt.newObj}, q)
			require.Equal(t, tt.want, q.Len())
		})
	}
}

func Test_ConfigRefEventHandler_Delete(t *testing.T) {
	q := newQueue()
	h := NewConfigRefEventHandler(newConfigRefReader())
	h.Delete(context.TODO(), event.DeleteEvent{Object: newSecret("my-secret", nil)}, q)
	require.Equal(t, 1, q.Len())
}

func Test_ConfigRefEventHandler_Generic(t *testing.T) {
	q := newQueue()
	h := NewConfigRefEventHandler(newConfigRefReader())
	h.Generic(context.TODO(), event.GenericEvent{Object: newSecret("my-secret", nil)}, q)
	require.Equal(t, 0, q.Len())
}
