package controller

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

var qualifiedBackupDigest = "sha256:" + strings.Repeat("a", 64)

func backupCapabilityFixture(t *testing.T) (*hav1.HomeAssistant, *appsv1.StatefulSet, *corev1.Pod) {
	t.Helper()
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default", UID: "target-uid"}}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
		Name: target.Name, Namespace: target.Namespace, UID: "sts-uid",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: hav1.GroupVersion.String(), Kind: "HomeAssistant", Name: target.Name,
			UID: target.UID, Controller: ptr.To(true),
		}},
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: target.Name + "-0", Namespace: target.Namespace, UID: "pod-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "StatefulSet", Name: sts.Name, UID: sts.UID, Controller: ptr.To(true),
			}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "home-assistant", Image: "ghcr.io/home-assistant/home-assistant@" + qualifiedBackupDigest,
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "home-assistant", Ready: true,
			ImageID: "docker-pullable://ghcr.io/home-assistant/home-assistant@" + qualifiedBackupDigest,
			State:   corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}},
	}
	return target, sts, pod
}

func TestNativeBackupCapabilityGateChecksLiveWorkload(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mutate      func(*hav1.HomeAssistant, *appsv1.StatefulSet, *corev1.Pod)
		qualified   bool
		wantAllowed bool
	}{
		{name: "qualified actual image", qualified: true, wantAllowed: true},
		{name: "empty production catalog"},
		{name: "different actual image", qualified: true,
			mutate: func(_ *hav1.HomeAssistant, _ *appsv1.StatefulSet, p *corev1.Pod) {
				p.Status.ContainerStatuses[0].ImageID = "containerd://sha256:" + strings.Repeat("b", 64)
			}},
		{name: "missing imageID", qualified: true, mutate: func(_ *hav1.HomeAssistant, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].ImageID = ""
		}},
		{name: "tag instead of pin", qualified: true,
			mutate: func(_ *hav1.HomeAssistant, _ *appsv1.StatefulSet, p *corev1.Pod) {
				p.Spec.Containers[0].Image = "ghcr.io/home-assistant/home-assistant:latest"
			}},
		{name: "unready container", qualified: true,
			mutate: func(_ *hav1.HomeAssistant, _ *appsv1.StatefulSet, p *corev1.Pod) {
				p.Status.ContainerStatuses[0].Ready = false
			}},
		{name: "foreign pod", qualified: true, mutate: func(_ *hav1.HomeAssistant, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.OwnerReferences[0].UID = "foreign"
		}},
		{name: "foreign workload", qualified: true,
			mutate: func(_ *hav1.HomeAssistant, s *appsv1.StatefulSet, _ *corev1.Pod) {
				s.OwnerReferences[0].UID = "foreign"
			}},
		{name: "recreated target", qualified: true,
			mutate: func(h *hav1.HomeAssistant, _ *appsv1.StatefulSet, _ *corev1.Pod) {
				h.UID = "replacement"
			}},
		{name: "terminating pod", qualified: true, mutate: func(_ *hav1.HomeAssistant, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.DeletionTimestamp = ptr.To(metav1.Now())
			p.Finalizers = []string{"test.example/retain"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, sts, pod := backupCapabilityFixture(t)
			observed := target.DeepCopy()
			if tc.mutate != nil {
				tc.mutate(target, sts, pod)
			}
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := appsv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := hav1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(target, sts, pod).Build()
			var catalog *haclient.CapabilityCatalog
			if tc.qualified {
				var err error
				catalog, err = haclient.NewCapabilityCatalog([]haclient.QualificationRecord{{
					ImageDigest:  qualifiedBackupDigest,
					Capabilities: map[haclient.BackupCapability]bool{haclient.CapabilityBackup: true},
				}})
				if err != nil {
					t.Fatal(err)
				}
			}
			gate := NativeBackupCapabilityGate{Reader: reader, Catalog: catalog}
			if err := gate.AuthorizeBackup(context.Background(), observed); (err == nil) != tc.wantAllowed {
				t.Fatalf("allowed = %v, want %v", err == nil, tc.wantAllowed)
			}
		})
	}
}

func TestHomeAssistantBackupReconcilerDeniesUnqualifiedImageBeforeNativeCalls(t *testing.T) {
	protection := staticBackupKeyProtection{value: BackupKeyProtection{Password: "synthetic-key", Verified: true}}
	r, operation, calls := newBackupOperationReconciler(t, protection)
	r.CapabilityGate = nil
	if _, err := r.Reconcile(context.Background(), requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated := getBackupOperation(t, r, operation)
	if backupConditionReason(updated) != "UnsupportedCapability" {
		t.Fatalf("reason = %s", backupConditionReason(updated))
	}
	if calls.config.Load() != 0 || calls.agents.Load() != 0 || calls.generate.Load() != 0 {
		t.Fatal("unqualified image reached native API")
	}
	root := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: operation.Namespace, Name: "target-uid-ha-coordination"}
	if err := r.Get(context.Background(), key, root); err == nil {
		t.Fatal("unqualified image created operation evidence")
	}
}
