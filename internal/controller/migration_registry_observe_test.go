package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMigrationRegistryLifecycleReobservesChangesAndNeverRecreates(t *testing.T) {
	for _, kind := range []string{"floor", "area", "label"} {
		t.Run(kind, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := hav1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			var state, calls atomic.Int32
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					conn, err := upgrader.Upgrade(w, req, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.Close() }()
					_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
					var auth map[string]any
					if conn.ReadJSON(&auth) != nil {
						return
					}
					_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
					var command map[string]any
					if conn.ReadJSON(&command) != nil {
						return
					}
					calls.Add(1)
					if command["type"] != "config/"+kind+"_registry/list" {
						t.Errorf("unexpected mutation %v", command["type"])
						return
					}
					entry := map[string]any{
						kind + "_id": "id",
						"name":       "Home",
						"icon":       nil,
						"level":      nil,
						"color":      nil,
						"floor_id":   nil,
						"labels":     []string{},
					}
					if state.Load() == 1 {
						entry["name"] = "Changed"
					}
					items := []map[string]any{entry}
					if state.Load() == 2 {
						items = nil
					}
					if items == nil {
						items = []map[string]any{}
					}
					_ = conn.WriteJSON(
						map[string]any{
							"id":      command["id"],
							"type":    "result",
							"success": true,
							"result":  items,
						},
					)
				}),
			)
			defer server.Close()
			finalizer := map[string]string{"floor": floorFinalizerName, "area": areaFinalizerName, "label": labelFinalizerName}[kind]
			metadata := metav1.ObjectMeta{
				Name:       "imported",
				Namespace:  "home",
				UID:        "object",
				Generation: 1,
				Finalizers: []string{finalizer},
				Annotations: map[string]string{
					migrationImportedAnnotation:  "true",
					migrationSourceIDAnnotation:  "id",
					migrationTargetUIDAnnotation: "uid",
					migrationAdoptionAnnotation:  migrationAdopted,
				},
			}
			ref := hav1.HomeAssistantReference{Name: "ha"}
			var object client.Object
			switch kind {
			case "floor":
				object = &hav1.HomeAssistantFloor{
					ObjectMeta: metadata,
					Spec: hav1.HomeAssistantFloorSpec{
						Name:             "Home",
						HomeAssistantRef: ref,
					},
				}
			case "area":
				object = &hav1.HomeAssistantArea{
					ObjectMeta: metadata,
					Spec: hav1.HomeAssistantAreaSpec{
						Name:             "Home",
						HomeAssistantRef: ref,
					},
				}
			case "label":
				object = &hav1.HomeAssistantLabel{
					ObjectMeta: metadata,
					Spec: hav1.HomeAssistantLabelSpec{
						Name:             "Home",
						HomeAssistantRef: ref,
					},
				}
			}
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "home", UID: "uid"},
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ha-homeassistant-api-token",
					Namespace: "home",
				},
				Data: map[string][]byte{"token": []byte("token")},
			}
			kube := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(object).
				WithObjects(object, ha, secret).
				Build()
			key := client.ObjectKeyFromObject(object)
			factory := func(string) *haclient.Client { return haclient.NewClient(server.URL) }
			reconcile := func() {
				var err error
				req := ctrl.Request{NamespacedName: key}
				switch kind {
				case "floor":
					_, err = (&HomeAssistantFloorReconciler{Client: kube, NewHAClient: factory}).Reconcile(
						context.Background(),
						req,
					)
				case "area":
					_, err = (&HomeAssistantAreaReconciler{Client: kube, NewHAClient: factory}).Reconcile(
						context.Background(),
						req,
					)
				case "label":
					_, err = (&HomeAssistantLabelReconciler{Client: kube, NewHAClient: factory}).Reconcile(
						context.Background(),
						req,
					)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, step := range []struct {
				state  int32
				reason string
			}{{0, "Adopted"}, {1, "RegistryConflict"}, {2, "RegistryIdentityNotFound"}, {0, "Adopted"}} {
				state.Store(step.state)
				reconcile()
				stored := object.DeepCopyObject().(client.Object)
				if err := kube.Get(context.Background(), key, stored); err != nil {
					t.Fatal(err)
				}
				var conditions []metav1.Condition
				switch v := stored.(type) {
				case *hav1.HomeAssistantFloor:
					conditions = v.Status.Conditions
				case *hav1.HomeAssistantArea:
					conditions = v.Status.Conditions
				case *hav1.HomeAssistantLabel:
					conditions = v.Status.Conditions
				}
				condition := meta.FindStatusCondition(conditions, "MigrationAdoption")
				if condition == nil || condition.Reason != step.reason {
					t.Fatalf("state=%d condition=%#v", step.state, condition)
				}
			}
			if calls.Load() != 4 {
				t.Fatalf("expected four fresh reads, got %d", calls.Load())
			}
			stored := object.DeepCopyObject().(client.Object)
			if err := kube.Get(context.Background(), key, stored); err != nil {
				t.Fatal(err)
			}
			switch v := stored.(type) {
			case *hav1.HomeAssistantFloor:
				v.Spec.Name = "Changed"
			case *hav1.HomeAssistantArea:
				v.Spec.Name = "Changed"
			case *hav1.HomeAssistantLabel:
				v.Spec.Name = "Changed"
			}
			stored.SetGeneration(2)
			if err := kube.Update(context.Background(), stored); err != nil {
				t.Fatal(err)
			}
			reconcile()
			if err := kube.Get(context.Background(), key, stored); err != nil {
				t.Fatal(err)
			}
			var conditions []metav1.Condition
			switch v := stored.(type) {
			case *hav1.HomeAssistantFloor:
				conditions = v.Status.Conditions
			case *hav1.HomeAssistantArea:
				conditions = v.Status.Conditions
			case *hav1.HomeAssistantLabel:
				conditions = v.Status.Conditions
			}
			if c := meta.FindStatusCondition(conditions, "MigrationAdoption"); c == nil ||
				c.Reason != "RegistryConflict" ||
				c.ObservedGeneration != 2 {
				t.Fatalf("changed CR condition=%#v", c)
			}
			stored.GetAnnotations()[migrationPausedAnnotation] = "true"
			if err := kube.Update(context.Background(), stored); err != nil {
				t.Fatal(err)
			}
			reconcile()
			if calls.Load() != 5 {
				t.Fatalf("paused reconcile contacted HA: calls=%d", calls.Load())
			}
			if err := kube.Get(context.Background(), key, stored); err != nil {
				t.Fatal(err)
			}
			delete(stored.GetAnnotations(), migrationPausedAnnotation)
			if err := kube.Update(context.Background(), stored); err != nil {
				t.Fatal(err)
			}
			if err := kube.Delete(context.Background(), stored); err != nil {
				t.Fatal(err)
			}
			reconcile()
			if err := kube.Get(context.Background(), key, stored); !apierrors.IsNotFound(err) {
				t.Fatalf("import deletion retained CR: %v", err)
			}
			if calls.Load() != 5 {
				t.Fatalf("deletion contacted HA: calls=%d", calls.Load())
			}
		})
	}
}

func TestMigrationRegistryApprovalBlocksUnsafeOverwrite(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	floor := &hav1.HomeAssistantFloor{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "floor",
			Namespace:  "home",
			UID:        "floor-uid",
			Generation: 2,
			Annotations: map[string]string{
				migrationImportedAnnotation:  "true",
				migrationSourceIDAnnotation:  "source-id",
				migrationTargetUIDAnnotation: "ha-uid",
			},
		},
		Spec: hav1.HomeAssistantFloorSpec{
			Name:             "Desired",
			HomeAssistantRef: hav1.HomeAssistantReference{Name: "ha"},
		},
	}
	raw := json.RawMessage(
		`[{"floor_id":"source-id","name":"Actual","icon":null,"level":null}]`,
	)
	identity := migrationIdentity{SourceID: "source-id", TargetUID: "ha-uid"}
	digest := migrationRegistryComparisonDigest(floor, identity, raw, "", nil)
	floor.Annotations[migrationOverwriteApprovalAnnotation] = fmt.Sprintf(
		`{"version":"v1","approved":true,"objectUID":"floor-uid","generation":2,"targetUID":"ha-uid","sourceID":"source-id","comparisonDigest":%q}`,
		digest,
	)
	var calls atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			conn, err := upgrader.Upgrade(w, req, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
			var auth map[string]any
			if conn.ReadJSON(&auth) != nil {
				return
			}
			_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
			var command map[string]any
			if conn.ReadJSON(&command) != nil {
				return
			}
			calls.Add(1)
			if command["type"] != "config/floor_registry/list" {
				t.Errorf("unexpected HA command %v", command["type"])
				return
			}
			_ = conn.WriteJSON(
				map[string]any{
					"id":      command["id"],
					"type":    "result",
					"success": true,
					"result":  raw,
				},
			)
		}),
	)
	defer server.Close()
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "home", UID: "ha-uid"},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-homeassistant-api-token", Namespace: "home"},
		Data:       map[string][]byte{"token": []byte("token")},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(floor).
		WithObjects(floor, ha, secret).
		Build()
	key := client.ObjectKeyFromObject(floor)
	for i, want := range []string{"UnsafeOverwriteUnsupported", "RegistryConflict"} {
		r := &HomeAssistantFloorReconciler{
			Client:      kube,
			NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
		}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		stored := &hav1.HomeAssistantFloor{}
		if err := kube.Get(context.Background(), key, stored); err != nil {
			t.Fatal(err)
		}
		condition := meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
		if condition == nil || condition.Status != metav1.ConditionFalse ||
			condition.Reason != want ||
			!strings.Contains(condition.Message, digest) {
			t.Fatalf("condition=%#v", condition)
		}
		if i == 0 {
			delete(stored.Annotations, migrationOverwriteApprovalAnnotation)
			if err := kube.Update(context.Background(), stored); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("requests=%d", calls.Load())
	}
}

func TestMigrationRegistryRequiresUniqueIDAndDoesNotMatchByName(t *testing.T) {
	for _, tc := range []struct {
		name, kind, raw, want string
		objectName            string
	}{
		{"floor match", "floor", `[{"floor_id":"id","name":"Home","icon":null,"level":null}]`, "Adopted", "Home"},
		{"area conflict", "area", `[{"area_id":"id","name":"Other"}]`, "RegistryConflict", "Home"},
		{"label duplicate", "label", `[{"label_id":"id","name":"Home"},{"label_id":"id","name":"Home"}]`, "RegistryIdentityAmbiguous", "Home"},
		{"same name different ID", "floor", `[{"floor_id":"other","name":"Home"}]`, "RegistryIdentityNotFound", "Home"},
		{"missing name", "area", `[{"area_id":"id"}]`, "RegistryIncomplete", "Home"},
		{"duplicate JSON key", "label", `[{"label_id":"id","label_id":"other","name":"Home"}]`, "RegistryInvalid", "Home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			switch tc.kind {
			case "floor":
				got = compareMigrationRegistry(
					&hav1.HomeAssistantFloor{
						Spec: hav1.HomeAssistantFloorSpec{Name: tc.objectName},
					},
					tc.kind,
					"id",
					json.RawMessage(tc.raw),
				)
			case "area":
				got = compareMigrationRegistry(
					&hav1.HomeAssistantArea{
						Spec: hav1.HomeAssistantAreaSpec{Name: tc.objectName},
					},
					tc.kind,
					"id",
					json.RawMessage(tc.raw),
				)
			case "label":
				got = compareMigrationRegistry(
					&hav1.HomeAssistantLabel{
						Spec: hav1.HomeAssistantLabelSpec{Name: tc.objectName},
					},
					tc.kind,
					"id",
					json.RawMessage(tc.raw),
				)
			}
			if got != tc.want {
				t.Fatalf("reason = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestMigrationRegistryControllersOnlyReadAndReconstructStatus(t *testing.T) {
	for _, kind := range []string{"floor", "area", "label"} {
		t.Run(kind, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := hav1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					conn, err := upgrader.Upgrade(w, req, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.Close() }()
					_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
					var auth map[string]any
					if conn.ReadJSON(&auth) != nil {
						return
					}
					_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
					var command map[string]any
					if conn.ReadJSON(&command) != nil {
						return
					}
					calls.Add(1)
					if command["type"] != "config/"+kind+"_registry/list" {
						t.Errorf("unexpected HA operation %v", command["type"])
						return
					}
					entry := map[string]any{
						"name":     "Home",
						"icon":     nil,
						"level":    nil,
						"color":    nil,
						"floor_id": nil,
						"labels":   []string{},
					}
					entry[kind+"_id"] = "source-id"
					_ = conn.WriteJSON(
						map[string]any{
							"id":      command["id"],
							"type":    "result",
							"success": true,
							"result":  []map[string]any{entry},
						},
					)
				}),
			)
			defer server.Close()
			metadata := metav1.ObjectMeta{
				Name:       "imported",
				Namespace:  "home",
				UID:        "object-uid",
				Generation: 3,
				Annotations: map[string]string{
					migrationImportedAnnotation:  "true",
					migrationPausedAnnotation:    "true",
					migrationSourceIDAnnotation:  "source-id",
					migrationTargetUIDAnnotation: "uid",
					migrationAdoptionAnnotation:  migrationAdopted,
				},
			}
			ref := hav1.HomeAssistantReference{Name: "ha"}
			var object client.Object
			switch kind {
			case "floor":
				object = &hav1.HomeAssistantFloor{
					ObjectMeta: metadata,
					Spec: hav1.HomeAssistantFloorSpec{
						HomeAssistantRef: ref,
						Name:             "Home",
					},
				}
			case "area":
				object = &hav1.HomeAssistantArea{
					ObjectMeta: metadata,
					Spec: hav1.HomeAssistantAreaSpec{
						HomeAssistantRef: ref,
						Name:             "Home",
					},
				}
			case "label":
				object = &hav1.HomeAssistantLabel{
					ObjectMeta: metadata,
					Spec: hav1.HomeAssistantLabelSpec{
						HomeAssistantRef: ref,
						Name:             "Home",
					},
				}
			}
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "home", UID: "uid"},
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ha-homeassistant-api-token",
					Namespace: "home",
				},
				Data: map[string][]byte{"token": []byte("token")},
			}
			kube := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(object).
				WithObjects(object, ha, secret).
				Build()
			factory := func(string) *haclient.Client { return haclient.NewClient(server.URL) }
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(object)}
			paused := object.DeepCopyObject().(client.Object)
			if err := kube.Get(context.Background(), req.NamespacedName, paused); err != nil {
				t.Fatal(err)
			}
			delete(paused.GetAnnotations(), migrationPausedAnnotation)
			if err := kube.Update(context.Background(), paused); err != nil {
				t.Fatal(err)
			}
			for iteration := 0; iteration < 3; iteration++ {
				var reconcile func(context.Context, ctrl.Request) (ctrl.Result, error)
				switch kind {
				case "floor":
					reconcile = (&HomeAssistantFloorReconciler{Client: kube, NewHAClient: factory}).Reconcile
				case "area":
					reconcile = (&HomeAssistantAreaReconciler{Client: kube, NewHAClient: factory}).Reconcile
				case "label":
					reconcile = (&HomeAssistantLabelReconciler{Client: kube, NewHAClient: factory}).Reconcile
				}
				if _, err := reconcile(context.Background(), req); err != nil {
					t.Fatal(err)
				}
				stored := object.DeepCopyObject().(client.Object)
				if err := kube.Get(context.Background(), req.NamespacedName, stored); err != nil {
					t.Fatal(err)
				}
				var conditions []metav1.Condition
				var id string
				switch v := stored.(type) {
				case *hav1.HomeAssistantFloor:
					conditions = v.Status.Conditions
					id = v.Status.FloorID
				case *hav1.HomeAssistantArea:
					conditions = v.Status.Conditions
					id = v.Status.AreaID
				case *hav1.HomeAssistantLabel:
					conditions = v.Status.Conditions
					id = v.Status.LabelID
				}
				condition := meta.FindStatusCondition(conditions, "MigrationAdoption")
				if calls.Load() != int32(iteration+1) || id != "source-id" || condition == nil ||
					condition.Reason != "Adopted" ||
					condition.Status != metav1.ConditionTrue ||
					condition.ObservedGeneration != 3 {
					t.Fatalf(
						"iteration=%d calls=%d id=%q condition=%#v",
						iteration,
						calls.Load(),
						id,
						condition,
					)
				}
				if iteration == 1 {
					switch v := stored.(type) {
					case *hav1.HomeAssistantFloor:
						v.Status = hav1.HomeAssistantFloorStatus{}
					case *hav1.HomeAssistantArea:
						v.Status = hav1.HomeAssistantAreaStatus{}
					case *hav1.HomeAssistantLabel:
						v.Status = hav1.HomeAssistantLabelStatus{}
					}
					if err := kube.Status().Update(context.Background(), stored); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestMigrationRegistryClearsStaleReadyOnPauseAndMissingIdentity(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		annotations map[string]string
		reason      string
	}{
		{"paused", map[string]string{migrationImportedAnnotation: "true", migrationPausedAnnotation: "true"}, "MigrationPaused"},
		{"missing identity", map[string]string{migrationImportedAnnotation: "true"}, "IncompleteIdentity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			floor := &hav1.HomeAssistantFloor{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "floor",
					Namespace:   "home",
					Annotations: tc.annotations,
				},
				Status: hav1.HomeAssistantFloorStatus{
					Conditions: []metav1.Condition{
						{
							Type:   conditionTypeReady,
							Status: metav1.ConditionTrue,
							Reason: "OldReady",
						},
					},
				},
			}
			kube := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(floor).
				WithObjects(floor).
				Build()
			r := &HomeAssistantFloorReconciler{Client: kube}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(floor)}); err != nil {
				t.Fatal(err)
			}
			stored := &hav1.HomeAssistantFloor{}
			if err := kube.Get(context.Background(), client.ObjectKeyFromObject(floor), stored); err != nil {
				t.Fatal(err)
			}
			ready := meta.FindStatusCondition(stored.Status.Conditions, conditionTypeReady)
			if ready == nil || ready.Status != metav1.ConditionFalse ||
				ready.Reason != tc.reason {
				t.Fatalf("ready = %#v", ready)
			}
		})
	}
}

func TestMigrationRegistryRestartReobservesDurableIdentityWithoutHAWrites(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		entries    []map[string]any
		want       string
	}{
		{"missing", "floor", []map[string]any{{"floor_id": "other", "name": "Home"}}, "RegistryIdentityNotFound"},
		{"conflict", "area", []map[string]any{{"area_id": "source-id", "name": "Changed"}}, "RegistryConflict"},
		{"ambiguous", "label", []map[string]any{{"label_id": "source-id", "name": "Home"}, {"label_id": "source-id", "name": "Home"}}, "RegistryIdentityAmbiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := hav1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					conn, err := upgrader.Upgrade(w, req, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.Close() }()
					_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
					var auth map[string]any
					if conn.ReadJSON(&auth) != nil {
						return
					}
					_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
					var command map[string]any
					if conn.ReadJSON(&command) != nil {
						return
					}
					calls.Add(1)
					if command["type"] != "config/"+tc.kind+"_registry/list" {
						t.Errorf("unexpected HA operation %v", command["type"])
						return
					}
					_ = conn.WriteJSON(
						map[string]any{
							"id":      command["id"],
							"type":    "result",
							"success": true,
							"result":  tc.entries,
						},
					)
				}),
			)
			defer server.Close()
			metadata := metav1.ObjectMeta{
				Name:      "imported",
				Namespace: "home",
				Annotations: map[string]string{
					migrationImportedAnnotation:  "true",
					migrationSourceIDAnnotation:  "source-id",
					migrationTargetUIDAnnotation: "uid",
				},
			}
			ref := hav1.HomeAssistantReference{Name: "ha"}
			var object client.Object
			switch tc.kind {
			case "floor":
				object = &hav1.HomeAssistantFloor{
					ObjectMeta: metadata,
					Spec: hav1.HomeAssistantFloorSpec{
						HomeAssistantRef: ref,
						Name:             "Home",
					},
				}
			case "area":
				object = &hav1.HomeAssistantArea{
					ObjectMeta: metadata,
					Spec: hav1.HomeAssistantAreaSpec{
						HomeAssistantRef: ref,
						Name:             "Home",
					},
				}
			case "label":
				object = &hav1.HomeAssistantLabel{
					ObjectMeta: metadata,
					Spec: hav1.HomeAssistantLabelSpec{
						HomeAssistantRef: ref,
						Name:             "Home",
					},
				}
			}
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "home", UID: "uid"},
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ha-homeassistant-api-token",
					Namespace: "home",
				},
				Data: map[string][]byte{"token": []byte("token")},
			}
			kube := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(object).
				WithObjects(object, ha, secret).
				Build()
			factory := func(string) *haclient.Client { return haclient.NewClient(server.URL) }
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(object)}
			reconcile := func() error {
				switch tc.kind {
				case "floor":
					_, err := (&HomeAssistantFloorReconciler{Client: kube, NewHAClient: factory}).Reconcile(
						context.Background(),
						req,
					)
					return err
				case "area":
					_, err := (&HomeAssistantAreaReconciler{Client: kube, NewHAClient: factory}).Reconcile(
						context.Background(),
						req,
					)
					return err
				default:
					_, err := (&HomeAssistantLabelReconciler{Client: kube, NewHAClient: factory}).Reconcile(
						context.Background(),
						req,
					)
					return err
				}
			}
			for i := 0; i < 2; i++ {
				if err := reconcile(); err != nil {
					t.Fatal(err)
				}
				stored := object.DeepCopyObject().(client.Object)
				if err := kube.Get(context.Background(), req.NamespacedName, stored); err != nil {
					t.Fatal(err)
				}
				var conditions []metav1.Condition
				switch v := stored.(type) {
				case *hav1.HomeAssistantFloor:
					conditions = v.Status.Conditions
					v.Status = hav1.HomeAssistantFloorStatus{}
				case *hav1.HomeAssistantArea:
					conditions = v.Status.Conditions
					v.Status = hav1.HomeAssistantAreaStatus{}
				case *hav1.HomeAssistantLabel:
					conditions = v.Status.Conditions
					v.Status = hav1.HomeAssistantLabelStatus{}
				}
				condition := meta.FindStatusCondition(conditions, "MigrationAdoption")
				if condition == nil || condition.Reason != tc.want {
					t.Fatalf("iteration %d condition=%#v", i, condition)
				}
				if i == 0 {
					if err := kube.Status().Update(context.Background(), stored); err != nil {
						t.Fatal(err)
					}
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("HA requests=%d, want two reads", calls.Load())
			}
		})
	}
}

func TestMigrationRegistryControllersFailClosedBeforeHAOnTargetAndTokenFailure(t *testing.T) {
	for _, kind := range []string{"floor", "area", "label"} {
		for _, scenario := range []struct {
			name, uid               string
			includeHA, includeToken bool
			reason                  string
		}{
			{"target missing", "", false, false, "TargetUnavailable"},
			{"target recreated", "wrong-uid", true, true, "TargetIdentityMismatch"},
			{"token missing", "expected-uid", true, false, "TokenUnavailable"},
		} {
			t.Run(kind+"/"+scenario.name, func(t *testing.T) {
				scheme := runtime.NewScheme()
				if err := hav1.AddToScheme(scheme); err != nil {
					t.Fatal(err)
				}
				if err := corev1.AddToScheme(scheme); err != nil {
					t.Fatal(err)
				}
				metadata := metav1.ObjectMeta{
					Name:      "imported",
					Namespace: "home",
					Annotations: map[string]string{
						migrationImportedAnnotation:  "true",
						migrationSourceIDAnnotation:  "source-id",
						migrationTargetUIDAnnotation: "expected-uid",
					},
				}
				ref := hav1.HomeAssistantReference{Name: "ha"}
				var object client.Object
				switch kind {
				case "floor":
					object = &hav1.HomeAssistantFloor{
						ObjectMeta: metadata,
						Spec: hav1.HomeAssistantFloorSpec{
							HomeAssistantRef: ref,
							Name:             "Home",
						},
					}
				case "area":
					object = &hav1.HomeAssistantArea{
						ObjectMeta: metadata,
						Spec: hav1.HomeAssistantAreaSpec{
							HomeAssistantRef: ref,
							Name:             "Home",
						},
					}
				case "label":
					object = &hav1.HomeAssistantLabel{
						ObjectMeta: metadata,
						Spec: hav1.HomeAssistantLabelSpec{
							HomeAssistantRef: ref,
							Name:             "Home",
						},
					}
				}
				builder := fake.NewClientBuilder().
					WithScheme(scheme).
					WithStatusSubresource(object).
					WithObjects(object)
				if scenario.includeHA {
					builder = builder.WithObjects(
						&hav1.HomeAssistant{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "ha",
								Namespace: "home",
								UID:       types.UID(scenario.uid),
							},
						},
					)
				}
				if scenario.includeToken {
					builder = builder.WithObjects(
						&corev1.Secret{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "ha-homeassistant-api-token",
								Namespace: "home",
							},
							Data: map[string][]byte{"token": []byte("token")},
						},
					)
				}
				kube := builder.Build()
				factory := func(string) *haclient.Client {
					t.Error("HA client constructed before target and token validation")
					return haclient.NewClient("http://127.0.0.1:1")
				}
				req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(object)}
				var err error
				switch kind {
				case "floor":
					_, err = (&HomeAssistantFloorReconciler{Client: kube, NewHAClient: factory}).Reconcile(
						context.Background(),
						req,
					)
				case "area":
					_, err = (&HomeAssistantAreaReconciler{Client: kube, NewHAClient: factory}).Reconcile(
						context.Background(),
						req,
					)
				case "label":
					_, err = (&HomeAssistantLabelReconciler{Client: kube, NewHAClient: factory}).Reconcile(
						context.Background(),
						req,
					)
				}
				if err != nil {
					t.Fatal(err)
				}
				stored := object.DeepCopyObject().(client.Object)
				if err := kube.Get(context.Background(), req.NamespacedName, stored); err != nil {
					t.Fatal(err)
				}
				var conditions []metav1.Condition
				switch v := stored.(type) {
				case *hav1.HomeAssistantFloor:
					conditions = v.Status.Conditions
				case *hav1.HomeAssistantArea:
					conditions = v.Status.Conditions
				case *hav1.HomeAssistantLabel:
					conditions = v.Status.Conditions
				}
				condition := meta.FindStatusCondition(conditions, "MigrationAdoption")
				if condition == nil || condition.Reason != scenario.reason ||
					condition.Status != metav1.ConditionFalse {
					t.Fatalf("condition = %#v", condition)
				}
			})
		}
	}
}
