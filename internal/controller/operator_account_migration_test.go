package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestLegacyMigrationDoesNotFallBackToOwnerLogin(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{
		{"missing", "LegacyCredentialMissing"},
		{"malformed", "LegacyCredentialMalformed"},
		{"rejected", "LegacyCredentialRejected"},
		{"not administrator", "LegacyCredentialRequiresAdmin"},
		{"dedicated identity", "LegacyCredentialRejected"},
		{"unavailable", reasonHomeAssistantUnavailable},
		{"lost status", "LegacyCredentialRejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			_ = hav1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "home", UID: "uid"},
				Spec: hav1.HomeAssistantSpec{
					Bootstrap: &hav1.BootstrapSpec{
						Enabled:         true,
						OperatorAccount: &hav1.OperatorAccountSpec{Mode: "dedicated", TokenSecretName: "operator"},
					},
				},
				Status: hav1.HomeAssistantStatus{
					Bootstrap: &hav1.BootstrapStatus{Completed: true, APITokenSecretName: "custom-legacy"},
				},
			}
			legacy := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "custom-legacy", Namespace: "home"},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{"token": []byte("legacy-sentinel")},
			}
			if tc.name == "malformed" {
				legacy.Data = map[string][]byte{"password": []byte("unrelated-sensitive-value")}
			}
			objects := []client.Object{}
			if tc.name != "missing" {
				objects = append(objects, legacy)
			}
			if tc.name == "lost status" || tc.name == "dedicated identity" {
				ledger := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "operator",
						Namespace: "home",
						Labels: map[string]string{
							"app.kubernetes.io/managed-by": "homeassistant-operator",
							operatorPurposeLabel:           "operator-api-token",
						},
						Annotations: map[string]string{
							operatorStageAnnotation:  "Provisioning",
							operatorLegacyAnnotation: "custom-legacy",
							operatorUserAnnotation:   "operator-id",
						},
					},
					Type: corev1.SecretTypeOpaque,
				}
				_ = controllerutil.SetControllerReference(ha, ledger, scheme)
				objects = append(objects, ledger)
				if tc.name == "lost status" {
					ha.Status = hav1.HomeAssistantStatus{}
				}
			}
			objects = append(objects, ha)
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(objects...).Build()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/api/websocket" {
					t.Errorf("migration attempted password login or onboarding: %s", req.URL.Path)
					w.WriteHeader(500)
					return
				}
				if tc.name == "unavailable" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
				var msg map[string]interface{}
				_ = conn.ReadJSON(&msg)
				if msg["access_token"] != "legacy-sentinel" {
					t.Error("wrong migration credential")
				}
				if tc.name == "rejected" || tc.name == "lost status" {
					_ = conn.WriteJSON(map[string]string{"type": "auth_invalid"})
					return
				}
				_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
				_ = conn.ReadJSON(&msg)
				if msg["type"] != "auth/current_user" {
					t.Error("unverified source token mutated an account")
				}
				user := haclient.AuthUser{ID: "administrator-id", IsAdmin: tc.name != "not administrator"}
				if tc.name == "dedicated identity" {
					user.ID = "operator-id"
				}
				_ = conn.WriteJSON(map[string]interface{}{"id": 1, "type": "result", "success": true, "result": user})
			}))
			defer server.Close()
			r := &HomeAssistantReconciler{
				Client:      c,
				Scheme:      scheme,
				NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
			}
			for range 2 {
				result, err := r.reconcileBootstrap(ctx, ha)
				if err != nil || result.RequeueAfter == 0 {
					t.Fatalf("result=%v error=%v", result, err)
				}
				condition := meta.FindStatusCondition(ha.Status.Conditions, conditionOperatorCredentialReady)
				if condition == nil || condition.Reason != tc.reason {
					t.Fatalf("condition=%+v want=%s", condition, tc.reason)
				}
				if strings.Contains(condition.Message, "legacy-sentinel") {
					t.Fatal("source token leaked into status")
				}
			}
			if tc.name != "missing" {
				unchanged := &corev1.Secret{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(legacy), unchanged); err != nil {
					t.Fatal(err)
				}
				if string(unchanged.Data["token"]) != string(legacy.Data["token"]) {
					t.Fatal("migration modified the legacy credential")
				}
			}
		})
	}
}
