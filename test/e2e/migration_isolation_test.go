package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/controller"
	"github.com/przemekhys/homeassistant-operator/test/utils"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

// This checks the candidate policy against a disposable listener or HA, not
// production authorization or ongoing enforcement after policy changes.
func TestMigrationTargetIsolationOnCluster(t *testing.T) {
	if os.Getenv("HA_MIGRATION_BACKUP_CLUSTER") != "true" {
		t.Skip("requires disposable qualification cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := ha.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	kube, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ns := "migration-isolation-" + utils.RandomString(8)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	create := func(obj client.Object) {
		t.Helper()
		if err := kube.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	create(namespace)
	t.Cleanup(func() { backupCleanupNamespace(t, kube, namespace) })
	policy := controller.MigrationTargetIsolationPolicy(ns, "target", "target-id", "worker-id")
	if policy == nil {
		t.Fatal("invalid candidate policy")
	}
	create(policy) // Must exist before the target is started.
	no := false
	image := "ghcr.io/home-assistant/home-assistant:2026.11.0"
	target := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: ns, Labels: map[string]string{"ha.homeassistant.io/migration-target": "target-id"}}, Spec: corev1.PodSpec{
		AutomountServiceAccountToken: &no, Containers: []corev1.Container{{Name: "listener", Image: image, Command: []string{"python", "-m", "http.server", "8123"}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(8123)}}, PeriodSeconds: 1}}},
	}}
	if os.Getenv("HA_MIGRATION_ISOLATION_REAL_HA") == "true" {
		target.Spec.Containers[0].Command = nil
		target.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "config", MountPath: "/config"}}
		target.Spec.Volumes = []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	}
	create(target)
	backupEventually(t, ctx, func() bool {
		if kube.Get(ctx, client.ObjectKeyFromObject(target), target) != nil {
			return false
		}
		for _, c := range target.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, "isolated target ready")
	for _, allowed := range []bool{true, false} {
		name := "untrusted"
		labels := map[string]string{}
		if allowed {
			name = "trusted"
			labels["ha.homeassistant.io/migration-worker"] = "worker-id"
		}
		script := fmt.Sprintf("import socket,sys,time\nallowed=%s\nfor i in range(10):\n try:\n  s=socket.create_connection(('%s',8123),timeout=2); s.close(); ok=True\n except OSError: ok=False\n if allowed and ok: sys.exit(0)\n if not allowed and ok: sys.exit(1)\n time.sleep(1)\nsys.exit(1 if allowed else 0)", map[bool]string{true: "True", false: "False"}[allowed], target.Status.PodIP)
		if allowed && os.Getenv("HA_MIGRATION_ISOLATION_REAL_HA") == "true" {
			script = fmt.Sprintf("import urllib.request,sys,time\nfor i in range(60):\n try:\n  r=urllib.request.urlopen('http://%s:8123/api/onboarding',timeout=2)\n  if r.status==200: sys.exit(0)\n except Exception: pass\n time.sleep(1)\nsys.exit(1)", target.Status.PodIP)
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "probe", Image: image, Command: []string{"python", "-c", script}}}}}
		create(pod)
		backupEventually(t, ctx, func() bool {
			if kube.Get(ctx, client.ObjectKeyFromObject(pod), pod) != nil {
				return false
			}
			if pod.Status.Phase == corev1.PodFailed {
				t.Fatalf("%s network probe failed", name)
			}
			return pod.Status.Phase == corev1.PodSucceeded
		}, "network probe "+name)
	}
	// A NetworkPolicy label is not an identity. A different Pod with the same
	// label must also pass, demonstrating why admission/RBAC controls are needed.
	spoofScript := fmt.Sprintf("import socket,sys,time\nfor i in range(15):\n try:\n  s=socket.create_connection(('%s',8123),timeout=2); s.close(); sys.exit(0)\n except OSError: time.sleep(1)\nsys.exit(1)", target.Status.PodIP)
	spoof := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "spoofed-worker", Namespace: ns, Labels: map[string]string{"ha.homeassistant.io/migration-worker": "worker-id"}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "probe", Image: image, Command: []string{"python", "-c", spoofScript}}}}}
	create(spoof)
	backupEventually(t, ctx, func() bool {
		if kube.Get(ctx, client.ObjectKeyFromObject(spoof), spoof) != nil {
			return false
		}
		if spoof.Status.Phase == corev1.PodFailed {
			t.Fatal("spoofed selector could not connect")
		}
		return spoof.Status.Phase == corev1.PodSucceeded
	}, "spoofed label negative control")
	policies := &networkingv1.NetworkPolicyList{}
	if err := kube.List(ctx, policies, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	if !controller.MigrationPolicySetExclusive(policy, policies.Items, target.Labels) {
		t.Fatal("candidate policy not recognized")
	}
	// NetworkPolicy allows are additive; demonstrate that an existing broad
	// policy invalidates the candidate rather than assuming deny takes priority.
	create(&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "allow-all", Namespace: ns}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{}}}})
	if err := kube.List(ctx, policies, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	if controller.MigrationPolicySetExclusive(policy, policies.Items, target.Labels) {
		t.Fatal("additive allow-all policy was not detected")
	}
	script := fmt.Sprintf("import socket,sys,time\nfor i in range(15):\n try:\n  s=socket.create_connection(('%s',8123),timeout=2); s.close(); sys.exit(0)\n except OSError: time.sleep(1)\nsys.exit(1)", target.Status.PodIP)
	probe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "opened", Namespace: ns}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "probe", Image: image, Command: []string{"python", "-c", script}}}}}
	create(probe)
	backupEventually(t, ctx, func() bool {
		if kube.Get(ctx, client.ObjectKeyFromObject(probe), probe) != nil {
			return false
		}
		if probe.Status.Phase == corev1.PodFailed {
			t.Fatal("allow-all did not open access")
		}
		return probe.Status.Phase == corev1.PodSucceeded
	}, "additive ingress positive control")
}
