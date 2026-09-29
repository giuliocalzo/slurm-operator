// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admissionpolicy

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	slinkyv1beta1 "github.com/SlinkyProject/slurm-operator/api/v1beta1"
)

func TestPodsBindingPolicy(t *testing.T) {
	ctx := context.Background()
	const topologySpec = "topo-switch:s1,topo-block:b1"

	for name, annotations := range map[string]map[string]string{
		"topology-node": {slinkyv1beta1.AnnotationNodeTopologySpec: topologySpec},
		"plain-node":    nil,
	} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations}}
		if _, err := kubeClient.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create Node %s: %v", name, err)
		}
	}

	// The policy takes effect asynchronously: bind fresh Pods until one is
	// annotated, so the cases below run against an enforced policy.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if got, ok := bindPod(t, ctx, testNamespace, "topology-node"); ok && got == topologySpec {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pod bound to topology-node never received the %s annotation", slinkyv1beta1.AnnotationNodeTopologySpec)
		}
		time.Sleep(250 * time.Millisecond)
	}

	t.Run("node without topology annotation leaves the pod unchanged", func(t *testing.T) {
		if got, ok := bindPod(t, ctx, testNamespace, "plain-node"); ok {
			t.Fatalf("unexpected annotation %q", got)
		}
	})

	t.Run("release namespace is excluded", func(t *testing.T) {
		if got, ok := bindPod(t, ctx, releaseNamespace, "topology-node"); ok {
			t.Fatalf("unexpected annotation %q", got)
		}
	})
}

// bindPod creates a Pod, binds it to node, and returns its topology annotation.
func bindPod(t *testing.T, ctx context.Context, namespace, node string) (string, bool) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "worker-", Namespace: namespace},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "slurmd", Image: "slurmd"}}},
	}
	pod, err := kubeClient.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create Pod: %v", err)
	}
	binding := &corev1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: namespace},
		Target:     corev1.ObjectReference{Kind: "Node", Name: node},
	}
	if err := kubeClient.CoreV1().Pods(namespace).Bind(ctx, binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("bind Pod %s to %s: %v", pod.Name, node, err)
	}
	pod, err = kubeClient.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Pod: %v", err)
	}
	if pod.Spec.NodeName != node {
		t.Fatalf("pod %s bound to %q, want %q", pod.Name, pod.Spec.NodeName, node)
	}
	got, ok := pod.Annotations[slinkyv1beta1.AnnotationNodeTopologySpec]
	return got, ok
}
