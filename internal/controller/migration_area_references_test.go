package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMigrationAreaReferencesUseDurableIdentity(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ref := hav1.HomeAssistantReference{Name: "ha"}
	annotations := func(id string) map[string]string {
		return map[string]string{
			migrationImportedAnnotation:  "true",
			migrationSourceIDAnnotation:  id,
			migrationTargetUIDAnnotation: "target",
		}
	}
	floor := &hav1.HomeAssistantFloor{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "floor-cr",
			Namespace:   "home",
			Annotations: annotations("floor-id"),
		},
		Spec: hav1.HomeAssistantFloorSpec{HomeAssistantRef: ref},
	}
	label := &hav1.HomeAssistantLabel{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "label-cr",
			Namespace:   "home",
			Annotations: annotations("label-id"),
		},
		Spec: hav1.HomeAssistantLabelSpec{HomeAssistantRef: ref},
	}
	area := &hav1.HomeAssistantArea{
		ObjectMeta: metav1.ObjectMeta{Name: "area", Namespace: "home"},
		Spec: hav1.HomeAssistantAreaSpec{
			HomeAssistantRef: ref,
			Name:             "Room",
			FloorName:        "floor-cr",
			Labels:           []string{"label-cr"},
		},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(floor, label).Build()
	floorID, labelIDs, err := resolveMigrationAreaReferences(
		context.Background(),
		kube,
		area,
		"target",
	)
	if err != nil || floorID != "floor-id" || len(labelIDs) != 1 || labelIDs[0] != "label-id" {
		t.Fatalf("resolved %q %v: %v", floorID, labelIDs, err)
	}
	raw := json.RawMessage(
		`[{"area_id":"area-id","name":"Room","icon":null,"floor_id":"floor-id","labels":["label-id"]}]`,
	)
	if reason := compareMigrationRegistryArea(area, "area-id", raw, floorID, labelIDs); reason != "Adopted" {
		t.Fatalf("reason = %s", reason)
	}
	if reason := compareMigrationRegistryArea(area, "area-id", raw, "other", labelIDs); reason != "RegistryConflict" {
		t.Fatalf("conflict reason = %s", reason)
	}
	if _, _, err := resolveMigrationAreaReferences(context.Background(), kube, area, "other-target"); err == nil {
		t.Fatal("cross-target reference accepted")
	}
	area.Spec.Labels = append(area.Spec.Labels, "label-cr")
	if _, _, err := resolveMigrationAreaReferences(context.Background(), kube, area, "target"); err == nil {
		t.Fatal("duplicate label identity accepted")
	}
}

func TestMigrationAreaControllerVerifiesReferencedRegistriesReadOnly(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		labels []map[string]any
		want   string
	}{
		{"all references exist", []map[string]any{{"label_id": "label-id"}}, "Adopted"},
		{"label missing", []map[string]any{}, "RegistryReferencesUnverified"},
		{"label ambiguous", []map[string]any{{"label_id": "label-id"}, {"label_id": "label-id"}}, "RegistryReferencesUnverified"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
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
					typ, _ := command["type"].(string)
					mu.Lock()
					commands = append(commands, typ)
					mu.Unlock()
					var result any
					switch typ {
					case "config/area_registry/list":
						result = []map[string]any{
							{
								"area_id":  "area-id",
								"name":     "Room",
								"icon":     nil,
								"floor_id": "floor-id",
								"labels":   []string{"label-id"},
							},
						}
					case "config/floor_registry/list":
						result = []map[string]any{{"floor_id": "floor-id"}}
					case "config/label_registry/list":
						result = scenario.labels
					default:
						t.Errorf("unexpected HA write: %s", typ)
						return
					}
					_ = conn.WriteJSON(
						map[string]any{
							"id":      command["id"],
							"type":    "result",
							"success": true,
							"result":  result,
						},
					)
				}),
			)
			defer server.Close()
			ref := hav1.HomeAssistantReference{Name: "ha"}
			annotations := func(id string) map[string]string {
				return map[string]string{
					migrationImportedAnnotation:  "true",
					migrationSourceIDAnnotation:  id,
					migrationTargetUIDAnnotation: "uid",
				}
			}
			area := &hav1.HomeAssistantArea{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "area",
					Namespace:   "home",
					Annotations: annotations("area-id"),
				},
				Spec: hav1.HomeAssistantAreaSpec{
					HomeAssistantRef: ref,
					Name:             "Room",
					FloorName:        "floor-cr",
					Labels:           []string{"label-cr"},
				},
			}
			floor := &hav1.HomeAssistantFloor{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "floor-cr",
					Namespace:   "home",
					Annotations: annotations("floor-id"),
				},
				Spec: hav1.HomeAssistantFloorSpec{HomeAssistantRef: ref},
			}
			label := &hav1.HomeAssistantLabel{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "label-cr",
					Namespace:   "home",
					Annotations: annotations("label-id"),
				},
				Spec: hav1.HomeAssistantLabelSpec{HomeAssistantRef: ref},
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
				WithStatusSubresource(area).
				WithObjects(area, floor, label, ha, secret).
				Build()
			r := &HomeAssistantAreaReconciler{
				Client:      kube,
				NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
			}
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(area)}
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			stored := &hav1.HomeAssistantArea{}
			if err := kube.Get(context.Background(), req.NamespacedName, stored); err != nil {
				t.Fatal(err)
			}
			condition := meta.FindStatusCondition(
				stored.Status.Conditions,
				"MigrationAdoption",
			)
			wantStatus := metav1.ConditionFalse
			if scenario.want == "Adopted" {
				wantStatus = metav1.ConditionTrue
			}
			if condition == nil || condition.Status != wantStatus ||
				condition.Reason != scenario.want {
				t.Fatalf("condition=%#v", condition)
			}
			mu.Lock()
			defer mu.Unlock()
			wantReads := 2
			if scenario.want == "Adopted" {
				wantReads++
			}
			if len(commands) != wantReads {
				t.Fatalf("commands=%v, want %d reads", commands, wantReads)
			}
			if scenario.want == "Adopted" && commands[len(commands)-1] != "config/area_registry/list" {
				t.Fatalf("area read must follow reference verification: %v", commands)
			}
		})
	}
}

func TestMigrationRegistryReferencedIDsMustExistUniquely(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		ids       []string
		want      bool
	}{
		{"matched", `[{"label_id":"a"},{"label_id":"b"}]`, []string{"b", "a"}, true},
		{"absent", `[{"label_id":"a"}]`, []string{"b"}, false},
		{"ambiguous", `[{"label_id":"a"},{"label_id":"a"}]`, []string{"a"}, false},
		{"malformed entry", `[{"label_id":"a"},{}]`, []string{"a"}, false},
		{"duplicate requested", `[{"label_id":"a"}]`, []string{"a", "a"}, false},
		{"duplicate JSON field", `[{"label_id":"a","label_id":"b"}]`, []string{"a"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := migrationRegistryContainsUniqueIDs(json.RawMessage(tc.raw), "label", tc.ids); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
