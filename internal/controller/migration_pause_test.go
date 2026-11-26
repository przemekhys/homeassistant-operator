package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
)

type pausedDependencyAudit struct {
	client.Client
	reads atomic.Int32
}

func (c *pausedDependencyAudit) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key.Name == "ha" || key.Name == "ha-homeassistant-api-token" {
		c.reads.Add(1)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *pausedDependencyAudit) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.reads.Add(1)
	return c.Client.List(ctx, list, opts...)
}

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

func TestPausedMigrationReconcilersNeverContactHAAfterRestart(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{hav1.AddToScheme, hav1alpha1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected Home Assistant request", http.StatusInternalServerError)
	}))
	defer server.Close()
	newClient := func(string) *haclient.Client { return haclient.NewClient(server.URL) }
	ref := hav1.HomeAssistantReference{Name: "ha"}
	tests := []struct {
		name      string
		object    client.Object
		finalizer string
		reconcile func(client.Client, ctrl.Request) (ctrl.Result, error)
	}{
		{"floor", &hav1.HomeAssistantFloor{Spec: hav1.HomeAssistantFloorSpec{HomeAssistantRef: ref, Name: "Floor"}, Status: hav1.HomeAssistantFloorStatus{FloorID: "source"}}, floorFinalizerName, func(c client.Client, req ctrl.Request) (ctrl.Result, error) {
			return (&HomeAssistantFloorReconciler{Client: c, NewHAClient: newClient}).Reconcile(context.Background(), req)
		}},
		{"area", &hav1.HomeAssistantArea{Spec: hav1.HomeAssistantAreaSpec{HomeAssistantRef: ref, Name: "Area"}, Status: hav1.HomeAssistantAreaStatus{AreaID: "source"}}, areaFinalizerName, func(c client.Client, req ctrl.Request) (ctrl.Result, error) {
			return (&HomeAssistantAreaReconciler{Client: c, NewHAClient: newClient}).Reconcile(context.Background(), req)
		}},
		{"label", &hav1.HomeAssistantLabel{Spec: hav1.HomeAssistantLabelSpec{HomeAssistantRef: ref, Name: "Label"}, Status: hav1.HomeAssistantLabelStatus{LabelID: "source"}}, labelFinalizerName, func(c client.Client, req ctrl.Request) (ctrl.Result, error) {
			return (&HomeAssistantLabelReconciler{Client: c, NewHAClient: newClient}).Reconcile(context.Background(), req)
		}},
		{"automation", &hav1.HomeAssistantAutomation{Spec: hav1.HomeAssistantAutomationSpec{HomeAssistantRef: ref, ID: "source"}}, automationFinalizerName, func(c client.Client, req ctrl.Request) (ctrl.Result, error) {
			return (&HomeAssistantAutomationReconciler{Client: c, NewHAClient: newClient}).Reconcile(context.Background(), req)
		}},
		{"scene", &hav1.HomeAssistantScene{Spec: hav1.HomeAssistantSceneSpec{HomeAssistantRef: ref, ID: "source"}}, sceneFinalizerName, func(c client.Client, req ctrl.Request) (ctrl.Result, error) {
			return (&HomeAssistantSceneReconciler{Client: c, NewHAClient: newClient}).Reconcile(context.Background(), req)
		}},
		{"script", &hav1.HomeAssistantScript{Spec: hav1.HomeAssistantScriptSpec{HomeAssistantRef: ref, ID: "source"}}, scriptFinalizerName, func(c client.Client, req ctrl.Request) (ctrl.Result, error) {
			return (&HomeAssistantScriptReconciler{Client: c, NewHAClient: newClient}).Reconcile(context.Background(), req)
		}},
		{"integration", &hav1.HomeAssistantIntegration{Spec: hav1.HomeAssistantIntegrationSpec{HomeAssistantRef: ref, Domain: "mqtt"}, Status: hav1.HomeAssistantIntegrationStatus{EntryID: "source"}}, integrationFinalizerName, func(c client.Client, req ctrl.Request) (ctrl.Result, error) {
			return (&HomeAssistantIntegrationReconciler{Client: c, NewHAClient: newClient}).Reconcile(context.Background(), req)
		}},
		{"dashboard", &hav1alpha1.HomeAssistantDashboard{Spec: hav1alpha1.HomeAssistantDashboardSpec{HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: "ha"}, Title: "Dashboard", URLPath: "dashboard", Inline: "views: []"}, Status: hav1alpha1.HomeAssistantDashboardStatus{DashboardID: "source"}}, dashboardFinalizer, func(c client.Client, req ctrl.Request) (ctrl.Result, error) {
			return (&HomeAssistantDashboardReconciler{Client: c, NewHAClient: newClient}).Reconcile(context.Background(), req)
		}},
		{"community repository", &hav1.HomeAssistantCommunityRepository{Spec: hav1.HomeAssistantCommunityRepositorySpec{HomeAssistantRef: ref}}, communityRepositoryFinalizerName, func(c client.Client, req ctrl.Request) (ctrl.Result, error) {
			return (&HomeAssistantCommunityRepositoryReconciler{Client: c, NewHAClient: newClient}).Reconcile(context.Background(), req)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.object.SetName("imported")
			tc.object.SetNamespace("default")
			tc.object.SetAnnotations(pausedMigrationAnnotations())
			tc.object.SetFinalizers([]string{tc.finalizer})
			ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "default", UID: "target"}}
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ha-homeassistant-api-token", Namespace: "default"}, Data: map[string][]byte{"token": []byte("token")}}
			store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(tc.object).WithObjects(tc.object, ha, secret).Build()
			audit := &pausedDependencyAudit{Client: store}
			key := client.ObjectKeyFromObject(tc.object)
			before := calls.Load()
			// Each invocation constructs a fresh reconciler, as after a restart.
			for i := 0; i < 2; i++ {
				if _, err := tc.reconcile(audit, ctrl.Request{NamespacedName: key}); err != nil {
					t.Fatal(err)
				}
				if audit.reads.Load() != 0 || calls.Load() != before {
					t.Fatalf("paused reconcile contacted dependencies: reads=%d requests=%d", audit.reads.Load(), calls.Load()-before)
				}
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
