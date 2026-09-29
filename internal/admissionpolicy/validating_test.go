// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admissionpolicy

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// policyCase admits object with a dry-run create or, when updated is set,
// creates object and admits updated's spec as a dry-run update of it.
//
// Policies take effect asynchronously, so every case is retried until it
// reaches its expected outcome. Denying cases are listed before the allowing
// cases of the same policy, so an allowing case cannot pass merely because the
// policy is not enforced yet.
type policyCase struct {
	name     string
	object   string
	updated  string
	wantDeny string
	wantWarn string
}

const (
	accounting = `
apiVersion: slinky.slurm.net/v1beta1
kind: Accounting
metadata: {name: %s, namespace: policy-test}
spec:
  slurmKeyRef: {name: slurm-key, key: slurm.key}
  jwtHs256KeyRef: {name: %s, key: jwt.key}
%s`
	controller = `
apiVersion: slinky.slurm.net/v1beta1
kind: Controller
metadata: {name: %s, namespace: policy-test}
spec:
  slurmKeyRef: {name: slurm-key, key: slurm.key}
  jwtHs256KeyRef: {name: jwt-key, key: jwt.key}
%s`
	loginSet = `
apiVersion: slinky.slurm.net/v1beta1
kind: LoginSet
metadata: {name: %s, namespace: policy-test}
spec:
  controllerRef: {name: %s}
  sssdConfRef: {name: sssd, key: sssd.conf}
%s`
	nodeSet = `
apiVersion: slinky.slurm.net/v1beta1
kind: NodeSet
metadata: {name: %s, namespace: policy-test}
spec:
  controllerRef: {name: %s}
%s`
	restAPI = `
apiVersion: slinky.slurm.net/v1beta1
kind: RestApi
metadata: {name: %s, namespace: policy-test}
spec:
  controllerRef: {name: slurm}
%s`
	//nolint:gosec // G101: a Token manifest, not a credential.
	token = `
apiVersion: slinky.slurm.net/v1beta1
kind: Token
metadata: {name: %s, namespace: policy-test}
spec:
  username: slurm
  jwtHs256KeyRef: {name: %s, key: jwt.key}
`
	externalIPs = `
  service:
    spec:
      externalIPs: [192.0.2.10]`
)

func TestValidatingAdmissionPolicies(t *testing.T) {
	ctx := context.Background()
	createParams(t, ctx)

	cases := []policyCase{
		// Accounting
		{
			name:     "accounting jwt ref is immutable",
			object:   fmt.Sprintf(accounting, "acct-jwt", "jwt-key", ""),
			updated:  fmt.Sprintf(accounting, "acct-jwt", "other-jwt-key", ""),
			wantDeny: "the value of JwtKeyRef or JwtHs256KeyRef cannot be modified after deployment",
		},
		{
			name:     "accounting externalIPs warn",
			object:   fmt.Sprintf(accounting, "acct-ips", "jwt-key", externalIPs),
			wantWarn: "ExternalIPs may not be set for accounting service",
		},
		{
			name:    "accounting update keeping jwt ref is allowed",
			object:  fmt.Sprintf(accounting, "acct-ok", "jwt-key", ""),
			updated: fmt.Sprintf(accounting, "acct-ok", "jwt-key", "  extraConf: CommitDelay=1"),
		},

		// Controller
		{
			name:     "controller clusterName longer than 40 characters",
			object:   fmt.Sprintf(controller, "ctrl-long", "  clusterName: "+strings.Repeat("c", 41)),
			wantDeny: "ClusterName exceeds 40 characters (41): " + strings.Repeat("c", 41),
		},
		{
			name:     "controller default clusterName longer than 40 characters",
			object:   fmt.Sprintf(controller, strings.Repeat("c", 30), ""),
			wantDeny: "ClusterName exceeds 40 characters (42): policy-test_" + strings.Repeat("c", 30),
		},
		{
			name:     "controller clusterName is immutable",
			object:   fmt.Sprintf(controller, "ctrl-name", "  clusterName: one"),
			updated:  fmt.Sprintf(controller, "ctrl-name", "  clusterName: two"),
			wantDeny: "cannot change ClusterName after deployment",
		},
		{
			name:     "controller default clusterName cannot be replaced",
			object:   fmt.Sprintf(controller, "ctrl-default-name", ""),
			updated:  fmt.Sprintf(controller, "ctrl-default-name", "  clusterName: other"),
			wantDeny: "cannot change ClusterName after deployment",
		},
		{
			name:     "controller slurmKeyRef is immutable",
			object:   fmt.Sprintf(controller, "ctrl-slurm-key", ""),
			updated:  strings.Replace(fmt.Sprintf(controller, "ctrl-slurm-key", ""), "name: slurm-key", "name: other-key", 1),
			wantDeny: "cannot change SlurmKeyRef after deployment",
		},
		{
			name:     "controller jwt ref is immutable",
			object:   fmt.Sprintf(controller, "ctrl-jwt", ""),
			updated:  strings.Replace(fmt.Sprintf(controller, "ctrl-jwt", ""), "name: jwt-key", "name: other-key", 1),
			wantDeny: "the value of JwtKeyRef or JwtHs256KeyRef cannot be modified after deployment",
		},
		{
			name:     "controller persistence.enabled is immutable",
			object:   fmt.Sprintf(controller, "ctrl-persist", "  persistence: {enabled: true}"),
			updated:  fmt.Sprintf(controller, "ctrl-persist", "  persistence: {enabled: false}"),
			wantDeny: "cannot change persistence.enabled after deployment",
		},
		{
			name:     "controller persistence.existingClaim is immutable",
			object:   fmt.Sprintf(controller, "ctrl-claim", "  persistence: {enabled: true, existingClaim: one}"),
			updated:  fmt.Sprintf(controller, "ctrl-claim", "  persistence: {enabled: true, existingClaim: two}"),
			wantDeny: "cannot change persistence.existingClaim after deployment",
		},
		{
			name:     "controller reserved config file",
			object:   fmt.Sprintf(controller, "ctrl-reserved", "  configFileRefs: [{name: known-files}, {name: reserved-files}]"),
			wantDeny: "the configFile is reserved for slurm-operator use: slurm.conf",
		},
		{
			name:     "controller unknown config file warn",
			object:   fmt.Sprintf(controller, "ctrl-unknown", "  configFileRefs: [{name: unknown-files}]"),
			wantWarn: "the configFile is unknown to Slurm, make sure to include it in another config file otherwise it is ignored: custom.conf",
		},
		{
			name:     "controller HA requires a ReadWriteMany claim",
			object:   fmt.Sprintf(controller, "ctrl-ha-rwo", "  ha: {enabled: true}\n  persistence: {enabled: true, existingClaim: rwo-claim}"),
			wantDeny: "slurm high availability (HA) mode requires given PVC (policy-test/rwo-claim) to have access mode 'ReadWriteMany'",
		},
		{
			name:     "controller externalIPs warn",
			object:   fmt.Sprintf(controller, "ctrl-ips", externalIPs),
			wantWarn: "ExternalIPs may not be set for controller service",
		},
		{
			name:   "controller with known config files and ReadWriteMany HA claim is allowed",
			object: fmt.Sprintf(controller, "ctrl-ok", "  configFileRefs: [{name: known-files}]\n  ha: {enabled: true}\n  persistence: {enabled: true, existingClaim: rwx-claim}"),
		},
		{
			name:   "controller with null configFileRefs is allowed",
			object: fmt.Sprintf(controller, "ctrl-null-refs", "  configFileRefs: null"),
		},
		{
			name:    "controller existingClaim may change when persistence is disabled",
			object:  fmt.Sprintf(controller, "ctrl-no-persist", "  persistence: {enabled: false, existingClaim: one}"),
			updated: fmt.Sprintf(controller, "ctrl-no-persist", "  persistence: {enabled: false, existingClaim: two}"),
		},

		// LoginSet
		{
			name:     "loginset controllerRef.name must be set",
			object:   fmt.Sprintf(loginSet, "login-no-ref", "", ""),
			wantDeny: "controllerRef.name must not be empty",
		},
		{
			name:     "loginset sssdConfRef.name must be set",
			object:   strings.Replace(fmt.Sprintf(loginSet, "login-no-sssd", "slurm", ""), "name: sssd, ", "", 1),
			wantDeny: "sssdConfRef.name must not be empty",
		},
		{
			name:     "loginset controllerRef is immutable",
			object:   fmt.Sprintf(loginSet, "login-ref", "slurm", ""),
			updated:  fmt.Sprintf(loginSet, "login-ref", "other", ""),
			wantDeny: "cannot change controllerRef after deployment",
		},
		{
			name:     "loginset externalIPs warn",
			object:   fmt.Sprintf(loginSet, "login-ips", "slurm", externalIPs),
			wantWarn: "ExternalIPs may not be set for loginset service",
		},
		{
			name:   "loginset is allowed",
			object: fmt.Sprintf(loginSet, "login-ok", "slurm", ""),
		},

		// NodeSet
		{
			name:     "nodeset controllerRef.name must be set",
			object:   fmt.Sprintf(nodeSet, "ns-no-ref", "", ""),
			wantDeny: "controllerRef.name must not be empty",
		},
		{
			name:     "nodeset maxUnavailable must be positive",
			object:   fmt.Sprintf(nodeSet, "ns-mu-int", "slurm", "  updateStrategy: {rollingUpdate: {maxUnavailable: 0}}"),
			wantDeny: "maxUnavailable must be > 0, got 0",
		},
		{
			name:     "nodeset maxUnavailable must not be 0%",
			object:   fmt.Sprintf(nodeSet, "ns-mu-pct", "slurm", `  updateStrategy: {rollingUpdate: {maxUnavailable: "0%"}}`),
			wantDeny: "maxUnavailable must not be 0%",
		},
		{
			name:     "nodeset scheduled update duration of at least a minute",
			object:   fmt.Sprintf(nodeSet, "ns-duration", "slurm", "  updateStrategy: {scheduledUpdate: {duration: 30s}}"),
			wantDeny: "UpdateStrategy.ScheduledUpdate.Duration must be at least 1 minute",
		},
		{
			name:     "nodeset ssh requires sssdConfRef",
			object:   fmt.Sprintf(nodeSet, "ns-ssh", "slurm", "  ssh: {enabled: true, sssdConfRef: {key: sssd.conf}}"),
			wantDeny: "ssh.sssdConfRef.name must not be empty when ssh is enabled",
		},
		{
			name:     "nodeset hostname must be a DNS subdomain",
			object:   fmt.Sprintf(nodeSet, "ns-hostname", "slurm", "  template: {spec: {hostname: Bad_Host}}"),
			wantDeny: "template.spec.hostname: a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters",
		},
		{
			name:     "nodeset extraConf items must be Key=Value",
			object:   fmt.Sprintf(nodeSet, "ns-conf", "slurm", `  extraConf: "Weight=1 bogus Features=a,b"`),
			wantDeny: `invalid extraConf: malformed --conf item "bogus": expected Key=Value`,
		},
		{
			name:     "nodeset controllerRef is immutable",
			object:   fmt.Sprintf(nodeSet, "ns-ref", "slurm", ""),
			updated:  fmt.Sprintf(nodeSet, "ns-ref", "other", ""),
			wantDeny: "cannot change controllerRef after deployment",
		},
		{
			name:     "nodeset volumeClaimTemplates are immutable",
			object:   fmt.Sprintf(nodeSet, "ns-vct", "slurm", ""),
			updated:  fmt.Sprintf(nodeSet, "ns-vct", "slurm", "  volumeClaimTemplates: [{metadata: {name: data}, spec: {accessModes: [ReadWriteOnce]}}]"),
			wantDeny: "cannot change volumeClaimTemplates after deployment",
		},
		{
			name: "nodeset is allowed",
			object: fmt.Sprintf(nodeSet, "ns-ok", "slurm", `
  extraConf: "Weight=1   Features=a,b"
  template: {spec: {hostname: node-}}
  updateStrategy:
    rollingUpdate: {maxUnavailable: 1}
    scheduledUpdate: {duration: 0s}
  ssh: {enabled: true, sssdConfRef: {name: sssd, key: sssd.conf}}`),
		},

		// RestApi
		{
			name:     "restapi externalIPs warn",
			object:   fmt.Sprintf(restAPI, "restapi-ips", externalIPs),
			wantWarn: "ExternalIPs may not be set for restapi services",
		},
		{
			name:   "restapi is allowed",
			object: fmt.Sprintf(restAPI, "restapi-ok", ""),
		},

		// Token
		{
			name:     "token jwt ref is immutable",
			object:   fmt.Sprintf(token, "token-jwt", "jwt-key"),
			updated:  fmt.Sprintf(token, "token-jwt", "other-jwt-key"),
			wantDeny: "the value of JwtKeyRef or JwtHs256KeyRef cannot be modified after deployment",
		},
		{
			name:   "token is allowed",
			object: fmt.Sprintf(token, "token-ok", "jwt-key"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			admitFn := dryRunCreate
			if tc.updated != "" {
				base := mustParse(t, tc.object)
				if err := create(ctx, base); err != nil {
					t.Fatalf("create base object: %v", err)
				}
				admitFn = dryRunUpdate
			}
			manifest := tc.object
			if tc.updated != "" {
				manifest = tc.updated
			}

			var warns []string
			var err error
			deadline := time.Now().Add(30 * time.Second)
			for {
				warns, err = admitFn(ctx, t, manifest)
				if tc.matches(warns, err) || time.Now().After(deadline) {
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			if !tc.matches(warns, err) {
				t.Fatalf("wantDeny=%q wantWarn=%q, got err=%v warnings=%q", tc.wantDeny, tc.wantWarn, err, warns)
			}
		})
	}
}

func (tc policyCase) matches(warns []string, err error) bool {
	if tc.wantDeny != "" {
		return err != nil && strings.Contains(err.Error(), tc.wantDeny)
	}
	if err != nil {
		return false
	}
	if tc.wantWarn == "" {
		return len(warns) == 0
	}
	return slices.ContainsFunc(warns, func(w string) bool { return strings.Contains(w, tc.wantWarn) })
}

func dryRunCreate(ctx context.Context, t *testing.T, manifest string) ([]string, error) {
	obj := mustParse(t, manifest)
	warnings.take()
	_, err := resourceClient(obj).Create(ctx, obj, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	return warnings.take(), err
}

func dryRunUpdate(ctx context.Context, t *testing.T, manifest string) ([]string, error) {
	obj := mustParse(t, manifest)
	current, err := resourceClient(obj).Get(ctx, obj.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get base object: %v", err)
	}
	current.Object["spec"] = obj.Object["spec"]
	warnings.take()
	_, err = resourceClient(obj).Update(ctx, current, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
	return warnings.take(), err
}

// createParams creates the ConfigMaps and PersistentVolumeClaims that the
// Controller policies look up in the Controller's namespace.
func createParams(t *testing.T, ctx context.Context) {
	t.Helper()
	configMaps := map[string]map[string]string{
		"known-files":    {"gres.conf": "", "cgroup.conf": ""},
		"reserved-files": {"slurm.conf": "", "gres.conf": ""},
		"unknown-files":  {"custom.conf": ""},
	}
	for name, data := range configMaps {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace}, Data: data}
		if _, err := kubeClient.CoreV1().ConfigMaps(testNamespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create ConfigMap %s: %v", name, err)
		}
	}
	claims := map[string]corev1.PersistentVolumeAccessMode{
		"rwo-claim": corev1.ReadWriteOnce,
		"rwx-claim": corev1.ReadWriteMany,
	}
	for name, mode := range claims {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{mode},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}
		if _, err := kubeClient.CoreV1().PersistentVolumeClaims(testNamespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create PersistentVolumeClaim %s: %v", name, err)
		}
	}
}
