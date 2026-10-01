// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

// Package admissionpolicy tests the ValidatingAdmissionPolicies and
// MutatingAdmissionPolicy rendered by the slurm-operator Helm chart when
// `webhook.admissionPolicies` is true, against a real kube-apiserver.
package admissionpolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/SlinkyProject/slurm-operator/internal/utils/testutils"
)

const (
	releaseNamespace = "slinky"
	testNamespace    = "policy-test"
)

var (
	dynamicClient dynamic.Interface
	kubeClient    kubernetes.Interface
	warnings      = &warningRecorder{}
)

// warningRecorder collects the API warnings returned for each request.
type warningRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warningRecorder) HandleWarningHeader(_ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, text)
}

func (w *warningRecorder) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	msgs := w.msgs
	w.msgs = nil
	return msgs
}

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	rootPath := filepath.Join("..", "..")

	helm, err := helmBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "skipping admission policy tests: %v\n", err)
		return 0
	}

	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(rootPath, "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: testutils.GetEnvTestBinary(rootPath),
	}
	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		return 1
	}
	defer func() { _ = testEnv.Stop() }()

	cfg = rest.CopyConfig(cfg)
	cfg.WarningHandler = warnings
	dynamicClient = dynamic.NewForConfigOrDie(cfg)
	kubeClient = kubernetes.NewForConfigOrDie(cfg)

	serverVersion, err := kubeClient.Discovery().ServerVersion()
	if err != nil {
		fmt.Fprintf(os.Stderr, "get server version: %v\n", err)
		return 1
	}
	policies, err := renderPolicies(helm, filepath.Join(rootPath, "helm", "slurm-operator"), serverVersion.GitVersion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "render admission policies: %v\n", err)
		return 1
	}

	ctx := context.Background()
	for _, ns := range []string{releaseNamespace, testNamespace} {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		if _, err := kubeClient.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{}); err != nil {
			fmt.Fprintf(os.Stderr, "create namespace %s: %v\n", ns, err)
			return 1
		}
	}
	for _, obj := range policies {
		if err := create(ctx, obj); err != nil {
			fmt.Fprintf(os.Stderr, "create %s %s: %v\n", obj.GetKind(), obj.GetName(), err)
			return 1
		}
	}

	return m.Run()
}

// helmBinary returns $HELM (set by `make test`) or helm from $PATH.
func helmBinary() (string, error) {
	if helm := os.Getenv("HELM"); helm != "" {
		return helm, nil
	}
	return exec.LookPath("helm")
}

// renderPolicies renders the chart's admission policy templates for the given
// Kubernetes version. The chart is copied without its CRD sub-chart dependency
// so no `helm dependency build` is needed.
func renderPolicies(helm, chartPath, kubeVersion string) ([]*unstructured.Unstructured, error) {
	tmpDir, err := os.MkdirTemp("", "slurm-operator-chart-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	chartDir := filepath.Join(tmpDir, "slurm-operator")
	if err := os.CopyFS(chartDir, os.DirFS(chartPath)); err != nil {
		return nil, err
	}
	for _, name := range []string{"Chart.lock", "charts"} {
		if err := os.RemoveAll(filepath.Join(chartDir, name)); err != nil {
			return nil, err
		}
	}
	chartFile := filepath.Join(chartDir, "Chart.yaml")
	raw, err := os.ReadFile(chartFile)
	if err != nil {
		return nil, err
	}
	chart := map[string]any{}
	if err := yaml.Unmarshal(raw, &chart); err != nil {
		return nil, err
	}
	delete(chart, "dependencies")
	if raw, err = yaml.Marshal(chart); err != nil {
		return nil, err
	}
	if err := os.WriteFile(chartFile, raw, 0o600); err != nil {
		return nil, err
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(helm, "template", "test", chartDir, //nolint:gosec // test-only helm invocation
		"--namespace", releaseNamespace,
		"--kube-version", kubeVersion,
		"--set", "webhook.admissionPolicies=true",
		"--set", "webhook.podsBinding=true",
		"--show-only", "templates/webhook/validating-admission-policy.yaml",
		"--show-only", "templates/webhook/mutating-admission-policy.yaml",
	)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}

	var objs []*unstructured.Unstructured
	decoder := utilyaml.NewYAMLOrJSONDecoder(&stdout, 4096)
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(&obj.Object); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if len(obj.Object) > 0 {
			objs = append(objs, obj)
		}
	}
	return objs, nil
}

func gvr(obj *unstructured.Unstructured) schema.GroupVersionResource {
	gv := obj.GroupVersionKind().GroupVersion()
	resource := strings.ToLower(obj.GetKind())
	switch {
	case strings.HasSuffix(resource, "y"):
		resource = strings.TrimSuffix(resource, "y") + "ies"
	default:
		resource += "s"
	}
	return gv.WithResource(resource)
}

func resourceClient(obj *unstructured.Unstructured) dynamic.ResourceInterface {
	client := dynamicClient.Resource(gvr(obj))
	if ns := obj.GetNamespace(); ns != "" {
		return client.Namespace(ns)
	}
	return client
}

func create(ctx context.Context, obj *unstructured.Unstructured) error {
	_, err := resourceClient(obj).Create(ctx, obj, metav1.CreateOptions{})
	return err
}

func mustParse(t *testing.T, manifest string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(manifest), &obj.Object); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return obj
}
