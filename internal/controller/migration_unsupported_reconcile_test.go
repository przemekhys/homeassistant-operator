package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestImportedFloorReconcileAndDeleteNeverWriteHA(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var commands []string
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
		var auth, command map[string]any
		if conn.ReadJSON(&auth) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
		if conn.ReadJSON(&command) != nil {
			return
		}
		name, _ := command["type"].(string)
		mu.Lock()
		commands = append(commands, name)
		mu.Unlock()
		var result any
		if name == "config/floor_registry/list" {
			result = []map[string]any{{"floor_id": "source-id", "name": "Existing", "icon": nil, "level": nil}}
		}
		_ = conn.WriteJSON(map[string]any{"id": command["id"], "type": "result", "success": true, "result": result})
	}))
	defer server.Close()
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "default", UID: "target"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ha-homeassistant-api-token", Namespace: "default"}, Data: map[string][]byte{"token": []byte("token")}}
	for _, imported := range []bool{true, false} {
		name := "normal-control"
		annotations := map[string]string{}
		if imported {
			name = "imported"
			annotations = map[string]string{migrationImportedAnnotation: "true", migrationSourceIDAnnotation: "source-id", migrationTargetUIDAnnotation: "target"}
		}
		t.Run(name, func(t *testing.T) {
			mu.Lock()
			commands = nil
			mu.Unlock()
			floor := &hav1.HomeAssistantFloor{ObjectMeta: metav1.ObjectMeta{Name: "floor", Namespace: "default", Finalizers: []string{floorFinalizerName, "example.test/keep"}, Annotations: annotations}, Spec: hav1.HomeAssistantFloorSpec{Name: "Desired", HomeAssistantRef: hav1.HomeAssistantReference{Name: "ha"}}, Status: hav1.HomeAssistantFloorStatus{FloorID: "source-id"}}
			store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(floor).WithObjects(floor, ha.DeepCopy(), secret.DeepCopy()).Build()
			r := &HomeAssistantFloorReconciler{Client: store, NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) }}
			key := client.ObjectKeyFromObject(floor)
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
			if err := store.Get(context.Background(), key, floor); err != nil {
				t.Fatal(err)
			}
			if err := store.Delete(context.Background(), floor); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			got := append([]string(nil), commands...)
			mu.Unlock()
			if imported {
				if len(got) != 1 || got[0] != "config/floor_registry/list" {
					t.Fatalf("imported resource performed unexpected HA requests: %v", got)
				}
				if err := store.Get(context.Background(), key, floor); err != nil {
					t.Fatal(err)
				}
				if len(floor.Finalizers) != 1 || floor.Finalizers[0] != "example.test/keep" {
					t.Fatalf("finalizers=%v", floor.Finalizers)
				}
			} else {
				var updated, deleted bool
				for _, command := range got {
					updated = updated || command == "config/floor_registry/update"
					deleted = deleted || command == "config/floor_registry/delete"
				}
				if !updated || !deleted {
					t.Fatalf("control did not exercise HA writes: %v", got)
				}
			}
		})
	}
	t.Run("incomplete identity", func(t *testing.T) {
		mu.Lock()
		commands = nil
		mu.Unlock()
		floor := &hav1.HomeAssistantFloor{ObjectMeta: metav1.ObjectMeta{Name: "floor", Namespace: "default", Finalizers: []string{floorFinalizerName}, Annotations: map[string]string{migrationImportedAnnotation: "true"}}, Spec: hav1.HomeAssistantFloorSpec{Name: "Desired", HomeAssistantRef: hav1.HomeAssistantReference{Name: "ha"}}, Status: hav1.HomeAssistantFloorStatus{FloorID: "source-id"}}
		store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(floor).WithObjects(floor, ha.DeepCopy(), secret.DeepCopy()).Build()
		r := &HomeAssistantFloorReconciler{Client: store, NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) }}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(floor)}); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(commands) != 0 {
			t.Fatalf("incomplete identity contacted HA: %v", commands)
		}
	})
}
