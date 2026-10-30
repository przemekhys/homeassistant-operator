package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestDedicatedAccountBanRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		runtime    bool
		status     int
	}{
		{"runtime ban", "/api/websocket", true, 403},
		{"runtime healthy", "", true, 200},
		{"runtime rate limited", "/api/websocket", true, 429},
		{"readiness ban", "/api/config", false, 403},
		{"onboarding ban", "/api/", false, 403},
		{"onboarding state ban", "/api/onboarding", false, 403},
		{"login flow ban", "/auth/login_flow", false, 403},
		{"credential login ban", "/auth/login_flow/flow", false, 403},
		{"token exchange ban", "/auth/token", false, 403},
		{"provisioning ban", "/api/websocket", false, 403},
		{"provisioning limit", "/api/websocket", false, 403},
		{"provisioning cooldown", "/api/websocket", false, 403},
		{"owner login alone preserves recovery", "/api/websocket", false, 500},
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
						Enabled: true,
						Credentials: &hav1.BootstrapCredentials{
							SecretRef: &hav1.CredentialsSecretRef{Name: "bootstrap"},
						},
						OperatorAccount: &hav1.OperatorAccountSpec{Mode: "dedicated", TokenSecretName: "operator"},
					},
				},
			}
			// Owner password authentication belongs to initial onboarding or an
			// explicit recovery request; migration authenticates with its legacy token.
			if !tc.runtime && (strings.HasPrefix(tc.path, "/auth/") || tc.path == "/api/websocket") {
				ha.Spec.Bootstrap.OperatorAccount.RecoveryRequestID = "ban-check"
			}
			{
				ha.Status.BanRestartWindowCount = 1
				past := metav1.NewTime(time.Now().Add(-10 * time.Minute))
				ha.Status.LastSelfUnban = &past
				ha.Status.BanRestartWindowStart = &past
				meta.SetStatusCondition(
					&ha.Status.Conditions,
					metav1.Condition{
						Type:    conditionTypeBanRecovery,
						Status:  metav1.ConditionFalse,
						Reason:  reasonBanRecoveryInProgress,
						Message: "previous ban",
					},
				)
			}
			switch tc.name {
			case "provisioning limit":
				ha.Status.BanRestartWindowCount = banRestartMaxCount
			case "provisioning cooldown":
				recent := metav1.Now()
				ha.Status.LastSelfUnban = &recent
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "operator",
					Namespace: "home",
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "homeassistant-operator",
						operatorPurposeLabel:           "operator-api-token",
					},
					Annotations: map[string]string{
						operatorStageAnnotation: "Provisioning",
						operatorUserAnnotation:  "operator-id",
					},
				},
				Type: corev1.SecretTypeOpaque,
			}
			if tc.runtime {
				secret.Annotations[operatorStageAnnotation] = "Verified"
				secret.Data = map[string][]byte{"token": []byte("operator-token")}
			}
			_ = controllerutil.SetControllerReference(ha, secret, scheme)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "home-0", Namespace: "home"}}
			bootstrap := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "bootstrap", Namespace: "home"},
				Data:       map[string][]byte{"username": []byte("owner"), "password": []byte("password")},
			}
			c := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(ha).
				WithObjects(ha, secret, pod, bootstrap).
				Build()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == tc.path {
					w.WriteHeader(tc.status)
					return
				}
				switch req.URL.Path {
				case "/api/config":
					w.WriteHeader(http.StatusUnauthorized)
				case "/api/":
					w.WriteHeader(http.StatusOK)
				case "/api/onboarding":
					_ = json.NewEncoder(w).Encode([]haclient.OnboardingStep{{Step: "user"}})
				case "/auth/login_flow":
					_ = json.NewEncoder(w).Encode(map[string]string{"flow_id": "flow", "type": "form"})
				case "/auth/login_flow/flow":
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "create_entry", "result": "code"})
				case "/auth/token":
					_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "owner-session"})
				case "/api/websocket":
					conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
					if err != nil {
						return
					}
					defer func() { _ = conn.Close() }()
					_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
					var msg map[string]interface{}
					_ = conn.ReadJSON(&msg)
					_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
					_ = conn.ReadJSON(&msg)
					if msg["type"] != "auth/current_user" {
						t.Error("unexpected account mutation")
					}
					_ = conn.WriteJSON(
						map[string]interface{}{
							"id":      1,
							"type":    "result",
							"success": true,
							"result":  haclient.AuthUser{ID: "operator-id", IsAdmin: true},
						},
					)
				default:
					t.Errorf("unexpected request %s", req.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			r := &HomeAssistantReconciler{
				Client:      c,
				Scheme:      scheme,
				Recorder:    events.NewFakeRecorder(16),
				NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
			}
			result, err := r.reconcileBootstrap(ctx, ha)
			if err != nil {
				t.Fatal(err)
			}
			updated := &hav1.HomeAssistant{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(ha), updated); err != nil {
				t.Fatal(err)
			}
			podErr := c.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{})
			assertDedicatedBanRecoveryResult(t, tc.name, tc.status, result, updated, podErr)
		})
	}
}

func assertDedicatedBanRecoveryResult(
	t *testing.T, name string, status int, result ctrl.Result, updated *hav1.HomeAssistant, podErr error,
) {
	t.Helper()
	if name == "provisioning limit" {
		condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeBanRecovery)
		if podErr != nil || updated.Status.SelfUnbanCount != 0 || condition == nil ||
			condition.Reason != reasonBanRecoveryLimitExceeded || result.RequeueAfter != banRestartWindow {
			t.Fatal("owner login bypassed the restart limit")
		}
		return
	}
	if name == "provisioning cooldown" {
		if podErr != nil || updated.Status.SelfUnbanCount != 0 || updated.Status.BanRestartWindowCount != 1 ||
			result.RequeueAfter <= 0 || result.RequeueAfter > selfUnbanCooldown {
			t.Fatal("owner login bypassed the restart cooldown")
		}
		return
	}
	if status == 403 {
		if !apierrors.IsNotFound(podErr) || result.RequeueAfter != selfUnbanRequeueWait ||
			updated.Status.SelfUnbanCount != 1 || updated.Status.BanRestartWindowCount != 2 {
			t.Fatalf(
				"ban did not trigger bounded pod recovery: result=%v status=%+v podError=%v",
				result,
				updated.Status,
				podErr,
			)
		}
		return
	}
	if podErr != nil || updated.Status.SelfUnbanCount != 0 {
		t.Fatal("non-ban response restarted the pod")
	}
	wantCount := int32(0)
	if status != 200 {
		wantCount = 1
	}
	if updated.Status.BanRestartWindowCount != wantCount {
		t.Fatalf("ban window count=%d want=%d", updated.Status.BanRestartWindowCount, wantCount)
	}
	banCondition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeBanRecovery)
	if wantCount == 0 && (updated.Status.LastSelfUnban != nil || banCondition != nil) {
		t.Fatal("successful authentication left stale ban recovery state")
	}
}

func TestOperatorIssuanceBanPreservesRestartWindow(t *testing.T) {
	for _, failAt := range []string{"token issuance", "principal verification", "health verification"} {
		t.Run(failAt, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			_ = hav1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			past := metav1.NewTime(time.Now().Add(-10 * time.Minute))
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "home", UID: "uid"},
				Status: hav1.HomeAssistantStatus{
					BanRestartWindowCount: 2,
					BanRestartWindowStart: &past,
					LastSelfUnban:         &past,
				},
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "home", Annotations: map[string]string{}},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha, secret).Build()
			attempt := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/auth/login_flow":
					_ = json.NewEncoder(w).Encode(map[string]string{"flow_id": "flow"})
				case "/auth/login_flow/flow":
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "create_entry", "result": "code"})
				case "/auth/token":
					_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "session"})
				case "/api/":
					w.WriteHeader(http.StatusForbidden)
				case "/api/websocket":
					attempt++
					if (failAt == "token issuance" && attempt == 2) ||
						(failAt == "principal verification" && attempt == 3) {
						w.WriteHeader(http.StatusForbidden)
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
					_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
					_ = conn.ReadJSON(&msg)
					var result interface{}
					switch msg["type"] {
					case "config/auth_provider/homeassistant/create":
					case "auth/long_lived_access_token":
						result = "issued-token"
					case "auth/current_user":
						result = haclient.AuthUser{ID: "operator-id", IsAdmin: true}
					default:
						t.Errorf("unexpected command %v", msg["type"])
					}
					_ = conn.WriteJSON(
						map[string]interface{}{"id": 1, "type": "result", "success": true, "result": result},
					)
				default:
					t.Errorf("unexpected path %s", req.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			r := &HomeAssistantReconciler{Client: c, Scheme: scheme}
			err := r.issueOperatorCredential(
				ctx,
				ha,
				secret,
				haclient.NewClient(server.URL),
				&haclient.AuthUser{ID: "operator-id"},
				"owner-session",
				false,
			)
			if !haclient.IsBanned(err) {
				t.Fatalf("expected late ban, got %v", err)
			}
			updated := &hav1.HomeAssistant{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(ha), updated); err != nil {
				t.Fatal(err)
			}
			if updated.Status.BanRestartWindowCount != 2 || updated.Status.LastSelfUnban == nil ||
				updated.Status.LastSelfUnban.Unix() != past.Unix() {
				t.Fatal("partial issuance cleared the restart window or cooldown")
			}
		})
	}
}

func TestOperatorProvisioningLogsRedactExternalErrors(t *testing.T) {
	for _, err := range []error{
		&haclient.Error{Type: haclient.ErrorTypeAuth, Message: "sentinel-credential", Err: fmt.Errorf("sentinel-credential")},
		apierrors.NewForbidden(corev1.Resource("secrets"), "operator", fmt.Errorf("sentinel-credential")),
		fmt.Errorf("sentinel-credential"),
	} {
		var output bytes.Buffer
		ctx := logf.IntoContext(context.Background(), zap.New(zap.WriteTo(&output)))
		logOperatorProvisioningError(ctx, err)
		if output.Len() == 0 {
			t.Fatal("provisioning failure was not logged")
		}
		if strings.Contains(output.String(), "sentinel-credential") {
			t.Fatal("external error exposed a credential in logs")
		}
	}
}
