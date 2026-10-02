// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package configrefs

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestPodSpecRefs(t *testing.T) {
	tests := []struct {
		name string
		spec corev1.PodSpec
		want []Ref
	}{
		{
			name: "empty",
			spec: corev1.PodSpec{},
			want: nil,
		},
		{
			name: "volumes",
			spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{Name: "cm", VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cm-vol"}},
					}},
					{Name: "secret", VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{SecretName: "secret-vol"},
					}},
					{Name: "projected", VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
							{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "cm-proj"}}},
							{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "secret-proj"}}},
							{DownwardAPI: &corev1.DownwardAPIProjection{}},
						}},
					}},
					{Name: "empty", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				},
			},
			want: []Ref{ConfigMap("cm-proj"), ConfigMap("cm-vol"), Secret("secret-proj"), Secret("secret-vol")},
		},
		{
			name: "containers and init containers",
			spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{
					EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "init-secret"}}}},
				}},
				Containers: []corev1.Container{{
					EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "main-cm"}}}},
				}},
			},
			want: []Ref{ConfigMap("main-cm"), Secret("init-secret")},
		},
		{
			name: "duplicates and empty names",
			spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{Name: "a", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "shared"}}},
					{Name: "b", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: ""}}},
				},
				Containers: []corev1.Container{{
					Env: []corev1.EnvVar{{Name: "X", ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "shared"}, Key: "x"},
					}}},
				}},
			},
			want: []Ref{Secret("shared")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, PodSpecRefs(&tt.spec))
		})
	}
}

func TestContainerRefs(t *testing.T) {
	container := corev1.Container{
		EnvFrom: []corev1.EnvFromSource{
			{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "envfrom-cm"}}},
			{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "envfrom-secret"}}},
		},
		Env: []corev1.EnvVar{
			{Name: "PLAIN", Value: "x"},
			{Name: "FIELD", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
			{Name: "CM", ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "env-cm"}, Key: "k"},
			}},
			{Name: "SECRET", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "env-secret"}, Key: "k"},
			}},
		},
	}
	want := []Ref{ConfigMap("env-cm"), ConfigMap("envfrom-cm"), Secret("env-secret"), Secret("envfrom-secret")}
	require.Equal(t, want, ContainerRefs(&container))
}

func TestMerge(t *testing.T) {
	got := Merge(
		[]Ref{Secret("b"), ConfigMap("a")},
		[]Ref{Secret("b"), ConfigMap(""), ConfigMap("b"), Secret("a")},
	)
	require.Equal(t, []Ref{ConfigMap("a"), ConfigMap("b"), Secret("a"), Secret("b")}, got)
}

func TestHash(t *testing.T) {
	ns := corev1.NamespaceDefault
	newCM := func(data map[string]string, binary map[string][]byte) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "cm"},
			Data:       data,
			BinaryData: binary,
		}
	}
	newSecret := func(data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "secret"},
			Data:       data,
		}
	}
	refs := []Ref{ConfigMap("cm"), Secret("secret")}
	hash := func(t *testing.T, objs ...client.Object) string {
		t.Helper()
		c := fake.NewClientBuilder().WithObjects(objs...).Build()
		got, err := Hash(context.TODO(), c, ns, refs)
		require.NoError(t, err)
		return got
	}

	base := hash(t, newCM(map[string]string{"a": "1"}, nil), newSecret(map[string][]byte{"s": []byte("x")}))

	t.Run("stable", func(t *testing.T) {
		require.Equal(t, base, hash(t, newCM(map[string]string{"a": "1"}, nil), newSecret(map[string][]byte{"s": []byte("x")})))
	})
	t.Run("configmap value changes", func(t *testing.T) {
		require.NotEqual(t, base, hash(t, newCM(map[string]string{"a": "2"}, nil), newSecret(map[string][]byte{"s": []byte("x")})))
	})
	t.Run("configmap key renamed", func(t *testing.T) {
		require.NotEqual(t, base, hash(t, newCM(map[string]string{"b": "1"}, nil), newSecret(map[string][]byte{"s": []byte("x")})))
	})
	t.Run("configmap binaryData added", func(t *testing.T) {
		require.NotEqual(t, base, hash(t, newCM(map[string]string{"a": "1"}, map[string][]byte{"bin": {0x1}}), newSecret(map[string][]byte{"s": []byte("x")})))
	})
	t.Run("secret value changes", func(t *testing.T) {
		require.NotEqual(t, base, hash(t, newCM(map[string]string{"a": "1"}, nil), newSecret(map[string][]byte{"s": []byte("y")})))
	})
	t.Run("absent and empty differ", func(t *testing.T) {
		absent := hash(t, newCM(map[string]string{"a": "1"}, nil))
		empty := hash(t, newCM(map[string]string{"a": "1"}, nil), newSecret(nil))
		require.NotEqual(t, base, absent)
		require.NotEqual(t, absent, empty)
	})
	t.Run("get error", func(t *testing.T) {
		c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				return errors.New("boom")
			},
		}).Build()
		_, err := Hash(context.TODO(), c, ns, refs)
		require.ErrorContains(t, err, "boom")
	})
}
