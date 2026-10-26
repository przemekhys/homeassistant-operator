package haclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"k8s.io/utils/ptr"
)

func TestBootstrapCoreConfigRetry(t *testing.T) {
	for _, failure := range []string{"onboarding", "websocket"} {
		t.Run(failure, func(t *testing.T) {
			var fail atomic.Bool
			fail.Store(true)
			var users, coreWrites, integrationWrites, updates atomic.Int32
			var coreDone, integrationDone atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/":
					w.WriteHeader(http.StatusUnauthorized)
				case "/api/onboarding":
					if integrationDone.Load() {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode([]OnboardingStep{
						{Step: "user", Done: users.Load() > 0},
						{Step: "core_config", Done: coreDone.Load()},
						{Step: "integration", Done: integrationDone.Load()},
					})
				case "/api/onboarding/users":
					users.Add(1)
					_, _ = w.Write([]byte(`{"auth_code":"code"}`))
				case "/auth/token":
					_, _ = w.Write([]byte(`{"access_token":"owner-token"}`))
				case "/api/onboarding/core_config":
					coreWrites.Add(1)
					if failure == "onboarding" && fail.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					coreDone.Store(true)
				case "/api/onboarding/analytics":
				case "/api/onboarding/integration":
					var payload map[string]string
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil ||
						payload["client_id"] == "" || payload["redirect_uri"] != payload["client_id"] {
						t.Errorf("invalid integration completion payload: %v (%v)", payload, err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					integrationWrites.Add(1)
					integrationDone.Store(true)
				case "/api/websocket":
					conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
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
					if cmd["type"] != "config/core/update" || cmd["country"] != "PL" ||
						cmd["latitude"] != float64(0) || cmd["longitude"] != float64(0) {
						t.Errorf("unexpected core update: %v", cmd)
					}
					updates.Add(1)
					_ = conn.WriteJSON(map[string]any{
						"id": cmd["id"], "type": "result", "success": !fail.Load(),
						"error": map[string]any{"code": "unknown_error", "message": "storage unavailable"},
					})
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client := NewClient(server.URL)
			req := &CoreConfigRequest{Country: "PL", UnitSystem: "metric", Latitude: ptr.To(0.0), Longitude: ptr.To(0.0)}
			_, err := client.PerformBootstrap(context.Background(), "admin", "password", "Owner", "en",
				&BootstrapOptions{CoreConfig: req})
			if err == nil || !strings.Contains(err.Error(), "core configuration") {
				t.Fatalf("expected core configuration failure, got %v", err)
			}
			// Retrying while storage is still unavailable must keep failing.
			if err := client.ResumeBootstrapCoreConfig(context.Background(), "owner-token", req, false); err == nil {
				t.Fatal("retry incorrectly succeeded while core config was unavailable")
			}
			fail.Store(false)
			if err := client.ResumeBootstrapCoreConfig(context.Background(), "owner-token", req, false); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
			if users.Load() != 1 || integrationWrites.Load() != 1 || !coreDone.Load() || updates.Load() == 0 {
				t.Fatalf("incorrect resumption: users=%d core=%d integration=%d updates=%d",
					users.Load(), coreWrites.Load(), integrationWrites.Load(), updates.Load())
			}
		})
	}
}
