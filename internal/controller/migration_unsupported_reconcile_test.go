package controller

import (
	"context"
	"testing"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUnsupportedMigrationKindsNeverReconcileOrDeleteHA(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		object    client.Object
		finalizer string
		reconcile func(client.Client, ctrl.Request) (ctrl.Result, error)
	}{
		{
			"floor",
			&hav1.HomeAssistantFloor{},
			floorFinalizerName,
			func(c client.Client, r ctrl.Request) (ctrl.Result, error) {
				return (&HomeAssistantFloorReconciler{Client: c}).Reconcile(
					context.Background(),
					r,
				)
			},
		},
		{
			"area",
			&hav1.HomeAssistantArea{},
			areaFinalizerName,
			func(c client.Client, r ctrl.Request) (ctrl.Result, error) {
				return (&HomeAssistantAreaReconciler{Client: c}).Reconcile(
					context.Background(),
					r,
				)
			},
		},
		{
			"label",
			&hav1.HomeAssistantLabel{},
			labelFinalizerName,
			func(c client.Client, r ctrl.Request) (ctrl.Result, error) {
				return (&HomeAssistantLabelReconciler{Client: c}).Reconcile(
					context.Background(),
					r,
				)
			},
		},
		{
			"automation",
			&hav1.HomeAssistantAutomation{},
			automationFinalizerName,
			func(c client.Client, r ctrl.Request) (ctrl.Result, error) {
				return (&HomeAssistantAutomationReconciler{Client: c}).Reconcile(
					context.Background(),
					r,
				)
			},
		},
		{
			"scene",
			&hav1.HomeAssistantScene{},
			sceneFinalizerName,
			func(c client.Client, r ctrl.Request) (ctrl.Result, error) {
				return (&HomeAssistantSceneReconciler{Client: c}).Reconcile(
					context.Background(),
					r,
				)
			},
		},
		{
			"script",
			&hav1.HomeAssistantScript{},
			scriptFinalizerName,
			func(c client.Client, r ctrl.Request) (ctrl.Result, error) {
				return (&HomeAssistantScriptReconciler{Client: c}).Reconcile(
					context.Background(),
					r,
				)
			},
		},
		{
			"integration",
			&hav1.HomeAssistantIntegration{},
			integrationFinalizerName,
			func(c client.Client, r ctrl.Request) (ctrl.Result, error) {
				return (&HomeAssistantIntegrationReconciler{Client: c}).Reconcile(
					context.Background(),
					r,
				)
			},
		},
		{
			"dashboard",
			&hav1alpha1.HomeAssistantDashboard{},
			dashboardFinalizer,
			func(c client.Client, r ctrl.Request) (ctrl.Result, error) {
				return (&HomeAssistantDashboardReconciler{Client: c}).Reconcile(
					context.Background(),
					r,
				)
			},
		},
		{
			"community repository",
			&hav1.HomeAssistantCommunityRepository{},
			communityRepositoryFinalizerName,
			func(c client.Client, r ctrl.Request) (ctrl.Result, error) {
				return (&HomeAssistantCommunityRepositoryReconciler{Client: c}).Reconcile(
					context.Background(),
					r,
				)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.object.SetName("imported")
			tc.object.SetNamespace("default")
			tc.object.SetAnnotations(
				map[string]string{
					migrationImportedAnnotation: "true",
					migrationAdoptionAnnotation: migrationAdopted,
				},
			)
			tc.object.SetFinalizers([]string{tc.finalizer})
			c := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(tc.object).
				WithObjects(tc.object).
				Build()
			key := client.ObjectKeyFromObject(tc.object)
			req := ctrl.Request{NamespacedName: key}
			// No HA reference or client is provided: reaching normal reconcile
			// or HA deletion is a test failure or panic.
			if _, err := tc.reconcile(c, req); err != nil {
				t.Fatal(err)
			}
			if err := c.Delete(context.Background(), tc.object); err != nil {
				t.Fatal(err)
			}
			if _, err := tc.reconcile(c, req); err != nil {
				t.Fatal(err)
			}
			current := tc.object.DeepCopyObject().(client.Object)
			if err := c.Get(context.Background(), key, current); !apierrors.IsNotFound(err) {
				t.Fatalf("blocked resource was not deleted: %v", err)
			}
		})
	}
}
