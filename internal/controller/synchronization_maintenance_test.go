package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/coordination"
)

func TestMaintenanceHoldDefersStatefulSetMutationUntilReleased(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, rbacv1.AddToScheme, hav1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Name: "home", Namespace: "default", UID: types.UID("maintenance-instance"),
	}, Spec: hav1.HomeAssistantSpec{Version: "2026.10.0"}}
	decodeSynchronizationFixture(t, []byte(`{"spec":{"alpha":{"coordinatedSynchronization":{"enabled":true}}}}`), ha)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ha).Build()
	store := coordination.Store{Reader: kube, Client: kube}
	if _, err := store.CreateRoot(ctx, ha.Namespace, ha.UID); err != nil {
		t.Fatal(err)
	}
	r := &HomeAssistantReconciler{
		Client: kube, Scheme: scheme, Coordination: CoordinationDependencies{APIReader: kube, Store: store},
	}
	if err := r.reconcileStatefulSet(ctx, ha); err != nil {
		t.Fatalf("initial infrastructure creation = %v", err)
	}
	if err := r.reconcileSynchronizationWriterAccess(ctx, ha); err != nil {
		t.Fatalf("create writer identity = %v", err)
	}
	before := &appsv1.StatefulSet{}
	key := client.ObjectKeyFromObject(ha)
	if err := kube.Get(ctx, key, before); err != nil {
		t.Fatal(err)
	}
	beforeImage := before.Spec.Template.Spec.Containers[0].Image

	if _, err := store.UpdateRoot(ctx, ha.Namespace, ha.UID, func(root *coordination.Root) error {
		root.MaintenanceHold = &coordination.MaintenanceHold{OperationUID: "restore", Reason: "Restoring"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ha.Spec.Version = "2026.11.0"
	if err := r.reconcileStatefulSet(ctx, ha); err != nil {
		t.Fatalf("held reconciliation = %v", err)
	}
	held := &appsv1.StatefulSet{}
	if err := kube.Get(ctx, key, held); err != nil {
		t.Fatal(err)
	}
	if got := held.Spec.Template.Spec.Containers[0].Image; got != beforeImage {
		t.Fatalf("maintenance hold rolled out desired image %q; expected running image %q", got, beforeImage)
	}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: ha.Namespace, Name: synchronizationWriterServiceAccountName(ha)}, &corev1.ServiceAccount{}); err != nil {
		t.Fatalf("maintenance hold removed runtime writer identity: %v", err)
	}

	if _, err := store.UpdateRoot(ctx, ha.Namespace, ha.UID, func(root *coordination.Root) error {
		root.MaintenanceHold = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileStatefulSet(ctx, ha); err != nil {
		t.Fatalf("resumed reconciliation = %v", err)
	}
	resumed := &appsv1.StatefulSet{}
	if err := kube.Get(ctx, key, resumed); err != nil {
		t.Fatal(err)
	}
	if got := resumed.Spec.Template.Spec.Containers[0].Image; got == beforeImage || got != homeAssistantImageRef(ha) {
		t.Fatalf("resume did not apply the latest desired image: got %q, want %q", got, homeAssistantImageRef(ha))
	}
}
