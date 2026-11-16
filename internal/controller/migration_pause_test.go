package controller

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

func pausedMigrationAnnotations() map[string]string {
	return map[string]string{
		migrationImportedAnnotation: "true", migrationPausedAnnotation: "true",
		migrationSourceIDAnnotation: "source", migrationTargetUIDAnnotation: "target",
	}
}

func TestOwnedImportWithoutMarkerStaysBlockedOnReconcileAndDelete(t *testing.T) {
	controller := true
	floor := &hav1.HomeAssistantFloor{ObjectMeta: metav1.ObjectMeta{
		Name: "floor", Namespace: "default", Finalizers: []string{floorFinalizerName},
		OwnerReferences: []metav1.OwnerReference{
			{
				APIVersion: "ha.homeassistant.io/v1alpha1",
				Kind:       "HomeAssistantMigration",
				UID:        "migration",
				Controller: &controller,
			},
		},
	}, Spec: hav1.HomeAssistantFloorSpec{Name: "Floor", HomeAssistantRef: hav1.HomeAssistantReference{Name: "ha"}}}
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(floor).
		WithObjects(floor).
		Build()
	reconciler := &HomeAssistantFloorReconciler{Client: kube}
	key := client.ObjectKeyFromObject(floor)
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	stored := &hav1.HomeAssistantFloor{}
	if err := kube.Get(context.Background(), key, stored); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
	if condition == nil || condition.Reason != "IncompleteIdentity" ||
		condition.Status != metav1.ConditionFalse {
		t.Fatalf("condition=%#v", condition)
	}
	if err := kube.Delete(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), key, stored); err == nil {
		t.Fatal("owned import finalizer blocked deletion")
	}
}

func TestPausedMigrationBlocksEverySupportedKindBeforeExternalWrites(t *testing.T) {
	objects := map[string]client.Object{
		"floor":                &hav1.HomeAssistantFloor{},
		"area":                 &hav1.HomeAssistantArea{},
		"label":                &hav1.HomeAssistantLabel{},
		"automation":           &hav1.HomeAssistantAutomation{},
		"scene":                &hav1.HomeAssistantScene{},
		"script":               &hav1.HomeAssistantScript{},
		"integration":          &hav1.HomeAssistantIntegration{},
		"dashboard":            &hav1alpha1.HomeAssistantDashboard{},
		"community repository": &hav1.HomeAssistantCommunityRepository{},
	}
	for name, object := range objects {
		t.Run(name, func(t *testing.T) {
			object.SetAnnotations(pausedMigrationAnnotations())
			if !migrationWritesBlocked(object) {
				t.Fatal("paused import reached a controller path that can write to HA")
			}
			// A new object read after a controller restart contains only durable
			// metadata and must remain paused when HA is unavailable.
			object.SetAnnotations(pausedMigrationAnnotations())
			if !migrationWritesBlocked(object) {
				t.Fatal("paused import was unblocked after restart")
			}
		})
	}
}

func TestPausedMigrationDeletionOnlyRemovesItsKubernetesFinalizer(t *testing.T) {
	now := metav1.NewTime(time.Now())
	object := &hav1.HomeAssistantFloor{ObjectMeta: metav1.ObjectMeta{
		Name: "paused", Namespace: "default", DeletionTimestamp: &now,
		Annotations: pausedMigrationAnnotations(), Finalizers: []string{"test.finalizer"},
	}}
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(object).Build()
	handled, err := removeBlockedMigrationFinalizer(
		context.Background(),
		store,
		object,
		"test.finalizer",
	)
	if err != nil || !handled {
		t.Fatalf("removeBlockedMigrationFinalizer() = %t, %v", handled, err)
	}
	if controllerutil.ContainsFinalizer(object, "test.finalizer") {
		t.Fatal("paused deletion retained controller finalizer")
	}
}

func TestUnpausedAdoptedMigrationDeletionStillPreservesHA(t *testing.T) {
	now := metav1.NewTime(time.Now())
	annotations := pausedMigrationAnnotations()
	delete(annotations, migrationPausedAnnotation)
	annotations[migrationAdoptionAnnotation] = migrationAdopted
	object := &hav1.HomeAssistantFloor{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "adopted",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Annotations:       annotations,
			Finalizers:        []string{"test.finalizer"},
		},
	}
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(object).Build()
	handled, err := removeBlockedMigrationFinalizer(
		context.Background(),
		store,
		object,
		"test.finalizer",
	)
	if err != nil || !handled || controllerutil.ContainsFinalizer(object, "test.finalizer") {
		t.Fatalf("handled=%t finalizers=%v err=%v", handled, object.Finalizers, err)
	}
}
