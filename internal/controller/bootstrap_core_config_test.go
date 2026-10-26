package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

func TestBootstrapCoreConfigFailureStatusAndRetry(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		createToken, existingToken bool
	}{
		{"without token creation", false, true},
		{"with existing token", true, true},
		{"owner login without token creation", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"},
				Spec: hav1.HomeAssistantSpec{Bootstrap: &hav1.BootstrapSpec{
					Enabled: true, CreateAPIToken: tc.createToken,
					Location:    &hav1.LocationConfig{Country: "PL"},
					Credentials: &hav1.BootstrapCredentials{SecretRef: &hav1.CredentialsSecretRef{Name: "owner"}},
				}},
			}
			scheme := runtime.NewScheme()
			if err := hav1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "home-homeassistant-api-token", Namespace: ha.Namespace},
				Data:       map[string][]byte{"token": []byte("owner-token")},
			}
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ha, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: ha.Namespace},
				Data:       map[string][]byte{"username": []byte("owner"), "password": []byte("password")},
			}).WithStatusSubresource(&hav1.HomeAssistant{})
			if tc.existingToken {
				builder = builder.WithObjects(secret)
			}
			cl := builder.Build()
			var success atomic.Bool
			var updates atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/auth/login_flow":
					_ = json.NewEncoder(w).Encode(map[string]string{"flow_id": "flow", "type": "form"})
					return
				case "/auth/login_flow/flow":
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "create_entry", "result": "code"})
					return
				case "/auth/token":
					_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "owner-token"})
					return
				}
				if req.URL.Path == "/api/onboarding" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
				var auth, cmd map[string]any
				_ = conn.ReadJSON(&auth)
				_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
				_ = conn.ReadJSON(&cmd)
				if cmd["type"] != "config/core/update" || cmd["country"] != "PL" {
					t.Errorf("unexpected settings command: %v", cmd)
				}
				for _, field := range []string{"latitude", "longitude", "elevation"} {
					if _, ok := cmd[field]; ok {
						t.Errorf("unspecified %s must not be overwritten", field)
					}
				}
				updates.Add(1)
				_ = conn.WriteJSON(map[string]any{
					"id": cmd["id"], "type": "result", "success": success.Load(),
					"error": map[string]any{"code": "unknown_error", "message": "storage unavailable"},
				})
			}))
			defer server.Close()
			r := &HomeAssistantReconciler{
				Client: cl, Scheme: scheme,
				NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
			}
			result, err := r.handleOnboardingAlreadyDone(ctx, ha)
			if err != nil {
				t.Fatal(err)
			}
			if result.RequeueAfter != bootstrapRetryInterval {
				t.Fatalf("retry interval = %s, want %s", result.RequeueAfter, bootstrapRetryInterval)
			}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(ha), ha); err != nil {
				t.Fatal(err)
			}
			condition := meta.FindStatusCondition(ha.Status.Conditions, "BootstrapReady")
			if ha.Status.Bootstrap.Completed || condition == nil || condition.Status != metav1.ConditionFalse ||
				condition.Reason != reasonBootstrapFailed || !strings.Contains(condition.Message, "country") {
				t.Fatalf("incorrect failure status: %+v", ha.Status)
			}
			success.Store(true)
			result, err = r.handleOnboardingAlreadyDone(ctx, ha)
			if err != nil {
				t.Fatal(err)
			}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(ha), ha); err != nil {
				t.Fatal(err)
			}
			if !ha.Status.Bootstrap.Completed || result.RequeueAfter != 0 || updates.Load() != 2 {
				t.Fatalf(
					"retry did not complete: status=%+v result=%+v writes=%d",
					ha.Status.Bootstrap,
					result,
					updates.Load(),
				)
			}
		})
	}
}

func TestDedicatedBootstrapCoreConfigFailure(t *testing.T) {
	ctx := context.Background()
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default", UID: "home-uid"},
		Spec: hav1.HomeAssistantSpec{Bootstrap: &hav1.BootstrapSpec{
			Enabled:         true,
			Location:        &hav1.LocationConfig{Country: "PL"},
			Credentials:     &hav1.BootstrapCredentials{SecretRef: &hav1.CredentialsSecretRef{Name: "owner"}},
			OperatorAccount: &hav1.OperatorAccountSpec{Mode: "dedicated", TokenSecretName: "operator"},
		}},
	}
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ha, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: ha.Namespace},
		Data:       map[string][]byte{"username": []byte("owner"), "password": []byte("password")},
	}, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "home-homeassistant-api-token", Namespace: ha.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"token": []byte("owner-token")},
	}).WithStatusSubresource(&hav1.HomeAssistant{}).Build()
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/config", "/api/":
			w.WriteHeader(http.StatusUnauthorized)
		case "/api/onboarding":
			w.WriteHeader(http.StatusNotFound)
		case "/auth/login_flow":
			t.Error("migration must use the legacy token instead of owner password login")
			_ = json.NewEncoder(w).Encode(map[string]string{"flow_id": "flow", "type": "form"})
		case "/auth/login_flow/flow":
			_ = json.NewEncoder(w).Encode(map[string]string{"type": "create_entry", "result": "code"})
		case "/auth/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "owner-token"})
		case "/api/websocket":
			conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
			var auth, cmd map[string]any
			_ = conn.ReadJSON(&auth)
			_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
			_ = conn.ReadJSON(&cmd)
			if auth["access_token"] != "owner-token" {
				t.Error("wrong migration credential")
			}
			if cmd["type"] == "auth/current_user" {
				_ = conn.WriteJSON(map[string]any{"id": cmd["id"], "type": "result", "success": true,
					"result": haclient.AuthUser{ID: "owner-id", IsOwner: true, IsAdmin: true}})
				return
			}
			if cmd["type"] != "config/core/update" || cmd["country"] != "PL" {
				t.Errorf("unexpected command before settings were applied: %v", cmd)
			}
			writes.Add(1)
			_ = conn.WriteJSON(map[string]any{
				"id": cmd["id"], "type": "result", "success": false,
				"error": map[string]any{"code": "unknown_error", "message": "storage unavailable"},
			})
		default:
			t.Errorf("unexpected request: %s", req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	r := &HomeAssistantReconciler{
		Client: cl, Scheme: scheme,
		NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
	}
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := r.reconcileBootstrap(ctx, ha)
		if err != nil {
			t.Fatal(err)
		}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(ha), ha); err != nil {
			t.Fatal(err)
		}
		condition := meta.FindStatusCondition(ha.Status.Conditions, "BootstrapReady")
		if result.RequeueAfter != bootstrapRetryInterval || ha.Status.Bootstrap.Completed ||
			condition == nil || condition.Status != metav1.ConditionFalse ||
			!strings.Contains(condition.Message, "country") || writes.Load() != int32(attempt) {
			t.Fatalf("attempt %d did not retain failure and retry: status=%+v result=%+v writes=%d",
				attempt, ha.Status, result, writes.Load())
		}
	}
}
