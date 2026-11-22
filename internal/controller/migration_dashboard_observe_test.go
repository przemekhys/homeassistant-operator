package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestFindMigrationDashboardRequiresUniqueExactID(t *testing.T) {
	dashboards := make([]haclient.Dashboard, 0, 3)
	dashboards = append(dashboards, haclient.Dashboard{ID: "first", URLPath: "same"},
		haclient.Dashboard{ID: "second", URLPath: "same"})
	if got := findMigrationDashboardID(dashboards, "second"); got == nil ||
		got.ID != "second" {
		t.Fatalf("got %#v", got)
	}
	if findMigrationDashboardID(dashboards, "same") != nil ||
		findMigrationDashboardID(dashboards, "") != nil {
		t.Fatal("accepted path or empty ID")
	}
	dashboards = append(dashboards, haclient.Dashboard{ID: "second"})
	if findMigrationDashboardID(dashboards, "second") != nil {
		t.Fatal("accepted ambiguous ID")
	}
}

func TestMigrationDashboardListRequiresCompleteUnambiguousMetadata(t *testing.T) {
	valid := `[{"id":"id","url_path":"path","mode":"storage","title":"Title","icon":null,"show_in_sidebar":true,"require_admin":false}]`
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"complete", valid, true},
		{"missing optional icon", strings.Replace(valid, `,"icon":null`, "", 1), true},
		{"invalid icon", strings.Replace(valid, `"icon":null`, `"icon":false`, 1), false},
		{"missing field", strings.Replace(valid, `,"require_admin":false`, "", 1), false},
		{"null boolean", strings.Replace(valid, `"require_admin":false`, `"require_admin":null`, 1), false},
		{"wrong type", strings.Replace(valid, `"require_admin":false`, `"require_admin":"false"`, 1), false},
		{"duplicate field", strings.Replace(valid, `"id":"id"`, `"id":"id","id":"id"`, 1), false},
		{"null list", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := migrationDashboardList(json.RawMessage(tc.raw))
			if (err == nil) != tc.valid {
				t.Fatalf("err=%v, valid=%t", err, tc.valid)
			}
		})
	}
}

func TestImportedDashboardObservationUsesOnlyReadCommands(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		duplicate    bool
		reason       string
		calls        int
	}{
		{"matching", `{"views":[]}`, false, "Adopted", 3},
		{"conflict", `{"views":[{"title":"private-value"}]}`, false, "DashboardDefinitionConflict", 2},
		{"duplicate identity", `{"views":[]}`, true, "DashboardIdentityUnverified", 1},
		{"metadata changes", `{"views":[]}`, false, "DashboardMetadataChanged", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var commands []string
			var state atomic.Int32
			var metadataChanged atomic.Bool
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
					if err := conn.ReadJSON(&auth); err != nil {
						return
					}
					_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
					var command map[string]any
					if err := conn.ReadJSON(&command); err != nil {
						return
					}
					typ, _ := command["type"].(string)
					mu.Lock()
					commands = append(commands, typ)
					mu.Unlock()
					var result any
					switch typ {
					case "lovelace/dashboards/list":
						items := []map[string]any{
							{
								"id":              "dash-id",
								"url_path":        "my-dashboard",
								"title":           "Dashboard",
								"mode":            "storage",
								"icon":            nil,
								"show_in_sidebar": true,
								"require_admin":   false,
							},
						}
						if tc.duplicate {
							items = append(items, items[0])
						}
						if metadataChanged.Load() {
							items[0]["title"] = "Changed"
						}
						if state.Load() == 2 {
							items = []map[string]any{}
						}
						result = items
					case "lovelace/config":
						if tc.name == "metadata changes" {
							metadataChanged.Store(true)
						}
						result = json.RawMessage(tc.config)
						if state.Load() == 1 {
							result = json.RawMessage(`{"views":[{"title":"changed"}]}`)
						}
					default:
						t.Errorf("unexpected write command %q", typ)
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
			scheme := runtime.NewScheme()
			if err := hav1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := hav1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			dashboard := &hav1alpha1.HomeAssistantDashboard{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "dash",
					Namespace:  "home",
					UID:        "dash-uid",
					Generation: 3,
					Annotations: map[string]string{
						migrationImportedAnnotation:  "true",
						migrationSourceIDAnnotation:  "dash-id",
						migrationTargetUIDAnnotation: "ha-uid",
					},
				},
				Spec: hav1alpha1.HomeAssistantDashboardSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: "ha"},
					Title:            "Dashboard",
					URLPath:          "my-dashboard",
					Inline:           "views: []",
				},
			}
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "home", UID: "ha-uid"},
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
				WithStatusSubresource(dashboard).
				WithObjects(dashboard, ha, secret).
				Build()
			r := &HomeAssistantDashboardReconciler{
				Client:      kube,
				NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
			}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dashboard)}); err != nil {
				t.Fatal(err)
			}
			stored := &hav1alpha1.HomeAssistantDashboard{}
			if err := kube.Get(context.Background(), client.ObjectKeyFromObject(dashboard), stored); err != nil {
				t.Fatal(err)
			}
			condition := meta.FindStatusCondition(
				stored.Status.Conditions,
				"MigrationAdoption",
			)
			wantStatus := metav1.ConditionFalse
			if tc.reason == "Adopted" {
				wantStatus = metav1.ConditionTrue
			}
			if condition == nil || condition.Reason != tc.reason ||
				condition.Status != wantStatus ||
				strings.Contains(condition.Message, "private-value") {
				t.Fatalf("condition = %#v", condition)
			}
			if tc.reason == "Adopted" {
				if stored.Status.DashboardID != "dash-id" ||
					condition.ObservedGeneration != 3 {
					t.Fatalf("status = %#v", stored.Status)
				}
				stored.Status = hav1alpha1.HomeAssistantDashboardStatus{}
				if err := kube.Status().Update(context.Background(), stored); err != nil {
					t.Fatal(err)
				}
				r = &HomeAssistantDashboardReconciler{
					Client:      kube,
					NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
				}
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dashboard)}); err != nil {
					t.Fatal(err)
				}
				if err := kube.Get(context.Background(), client.ObjectKeyFromObject(dashboard), stored); err != nil {
					t.Fatal(err)
				}
				condition = meta.FindStatusCondition(
					stored.Status.Conditions,
					"MigrationAdoption",
				)
				if stored.Status.DashboardID != "dash-id" || condition == nil ||
					condition.Status != metav1.ConditionTrue {
					t.Fatalf("reconstructed status = %#v", stored.Status)
				}
				for _, step := range []struct {
					state  int32
					reason string
				}{{1, "DashboardDefinitionConflict"}, {2, "DashboardIdentityUnverified"}} {
					state.Store(step.state)
					if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dashboard)}); err != nil {
						t.Fatal(err)
					}
					if err := kube.Get(context.Background(), client.ObjectKeyFromObject(dashboard), stored); err != nil {
						t.Fatal(err)
					}
					condition = meta.FindStatusCondition(
						stored.Status.Conditions,
						"MigrationAdoption",
					)
					if condition == nil || condition.Status != metav1.ConditionFalse ||
						condition.Reason != step.reason {
						t.Fatalf("state=%d condition=%#v", step.state, condition)
					}
				}
				stored.Annotations[migrationPausedAnnotation] = "true"
				if err := kube.Update(context.Background(), stored); err != nil {
					t.Fatal(err)
				}
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dashboard)}); err != nil {
					t.Fatal(err)
				}
				if err := kube.Get(context.Background(), client.ObjectKeyFromObject(dashboard), stored); err != nil {
					t.Fatal(err)
				}
				condition = meta.FindStatusCondition(
					stored.Status.Conditions,
					"MigrationAdoption",
				)
				if condition == nil || condition.Status != metav1.ConditionFalse ||
					condition.Reason != "MigrationPaused" {
					t.Fatalf("paused condition=%#v", condition)
				}
				tc.calls = tc.calls*2 + 3
			}
			if tc.reason == "DashboardDefinitionConflict" {
				_, digest, ok := strings.Cut(condition.Message, "comparison=")
				if !ok || digest == "" {
					t.Fatalf("missing comparison digest: %#v", condition)
				}
				stored.Annotations[migrationOverwriteApprovalAnnotation] = fmt.Sprintf(
					`{"version":"v1","approved":true,"objectUID":"dash-uid","generation":3,"targetUID":"ha-uid","sourceID":"dash-id","comparisonDigest":%q}`,
					digest,
				)
				if err := kube.Update(context.Background(), stored); err != nil {
					t.Fatal(err)
				}
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dashboard)}); err != nil {
					t.Fatal(err)
				}
				if err := kube.Get(context.Background(), client.ObjectKeyFromObject(dashboard), stored); err != nil {
					t.Fatal(err)
				}
				condition = meta.FindStatusCondition(
					stored.Status.Conditions,
					"MigrationAdoption",
				)
				if condition == nil || condition.Reason != "UnsafeOverwriteUnsupported" ||
					condition.Status != metav1.ConditionFalse {
					t.Fatalf("condition=%#v", condition)
				}
				tc.calls *= 2
			}
			mu.Lock()
			defer mu.Unlock()
			if len(commands) != tc.calls {
				t.Fatalf("commands = %v, want %d", commands, tc.calls)
			}
		})
	}
}
