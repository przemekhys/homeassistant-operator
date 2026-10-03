package haclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHAClient(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "HAClient Suite")
}

const (
	testAPIPath         = "/api/"
	testOnboardingPath  = "/api/onboarding"
	testOnboardingUsers = "/api/onboarding/users"
	testAuthToken       = "/auth/token"
	testCoreConfig      = "/api/onboarding/core_config"
	testAnalytics       = "/api/onboarding/analytics"
	testIntegration     = "/api/onboarding/integration"
	testWebsocket       = "/api/websocket"
)

var _ = Describe("HAClient", func() {
	var (
		server *httptest.Server
		client *Client
		ctx    context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
	})

	Describe("CheckHealth", func() {
		It("Should return nil for 200 OK response", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal(testAPIPath))
				Expect(r.Method).To(Equal("GET"))
				Expect(r.Header.Get("User-Agent")).To(Equal(userAgent))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"message": "API running."}`))
			}))

			client = NewClient(server.URL)
			err := client.CheckHealth(ctx)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return nil for 401 Unauthorized (HA ready but needs onboarding)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}))

			client = NewClient(server.URL)
			err := client.CheckHealth(ctx)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return NotReady error for other status codes", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			}))

			client = NewClient(server.URL)
			err := client.CheckHealth(ctx)
			Expect(err).To(HaveOccurred())
			Expect(IsNotReady(err)).To(BeTrue())
		})

		It("Should return NotReady error for network failure", func() {
			client = NewClient("http://localhost:1") // Invalid address
			client = client.WithTimeout(100 * time.Millisecond)
			err := client.CheckHealth(ctx)
			Expect(err).To(HaveOccurred())
			Expect(IsNotReady(err)).To(BeTrue())
		})
	})

	Describe("CheckHealthWithToken", func() {
		It("Should send the Bearer token", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer llat"))
				w.WriteHeader(http.StatusOK)
			}))

			client = NewClient(server.URL)
			Expect(client.CheckHealthWithToken(ctx, "llat")).To(Succeed())
		})
	})

	Describe("CheckAPIReady", func() {
		It("Should return nil for 401 Unauthorized (API routes loaded)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/api/config"))
				Expect(r.Method).To(Equal("GET"))
				Expect(r.Header.Get("Authorization")).To(BeEmpty())
				w.WriteHeader(http.StatusUnauthorized)
			}))

			client = NewClient(server.URL)
			err := client.CheckAPIReady(ctx)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return nil for 200 OK (API ready, unexpected but valid)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))

			client = NewClient(server.URL)
			err := client.CheckAPIReady(ctx)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return NotReady for 404 (routes not registered yet)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}))

			client = NewClient(server.URL)
			err := client.CheckAPIReady(ctx)
			Expect(err).To(HaveOccurred())
			Expect(IsNotReady(err)).To(BeTrue())
		})

		It("Should return NotReady for 500 (server error)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}))

			client = NewClient(server.URL)
			err := client.CheckAPIReady(ctx)
			Expect(err).To(HaveOccurred())
			Expect(IsNotReady(err)).To(BeTrue())
		})

		It("Should return NotReady on connection failure", func() {
			client = NewClient("http://localhost:1")
			client = client.WithTimeout(100 * time.Millisecond)
			err := client.CheckAPIReady(ctx)
			Expect(err).To(HaveOccurred())
			Expect(IsNotReady(err)).To(BeTrue())
		})
	})

	Describe("CheckOnboardingStatus", func() {
		It("Should return nil when onboarding is needed (all steps pending)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal(testOnboardingPath))
				Expect(r.Method).To(Equal("GET"))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`[{"step":"user","done":false},{"step":"core_config","done":false}]`))
			}))

			client = NewClient(server.URL)
			err := client.CheckOnboardingStatus(ctx)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return OnboardingDone when 404 (endpoint not registered)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}))

			client = NewClient(server.URL)
			err := client.CheckOnboardingStatus(ctx)
			Expect(err).To(HaveOccurred())
			Expect(IsOnboardingDone(err)).To(BeTrue())
		})

		It("Should return OnboardingDone when user step is done in array", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(
					`[{"step":"user","done":true},{"step":"core_config","done":false}]`,
				))
			}))

			client = NewClient(server.URL)
			err := client.CheckOnboardingStatus(ctx)
			Expect(err).To(HaveOccurred())
			Expect(IsOnboardingDone(err)).To(BeTrue())
		})

		It("Should return HTTP error for non-200/non-404 status", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}))

			client = NewClient(server.URL)
			err := client.CheckOnboardingStatus(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeHTTP))
		})
	})

	Describe("CreateUser", func() {
		It("Should create user successfully and return auth_code", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal(testOnboardingUsers))
				Expect(r.Method).To(Equal("POST"))
				Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))

				var req CreateUserRequest
				err := json.NewDecoder(r.Body).Decode(&req)
				Expect(err).NotTo(HaveOccurred())
				Expect(req.Username).To(Equal("admin"))
				Expect(req.Password).To(Equal("password123"))
				Expect(req.Name).To(Equal("Admin"))
				Expect(req.Language).To(Equal("en"))

				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"auth_code": "test-auth-code-123"}`))
			}))

			client = NewClient(server.URL)
			resp, err := client.CreateUser(ctx, &CreateUserRequest{
				ClientID: "test-client",
				Name:     "Admin",
				Username: "admin",
				Password: "password123",
				Language: "en",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp).NotTo(BeNil())
			Expect(resp.AuthCode).To(Equal("test-auth-code-123"))
		})

		It("Should return error for invalid response (no auth_code)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))

			client = NewClient(server.URL)
			resp, err := client.CreateUser(ctx, &CreateUserRequest{
				Username: "admin",
				Password: "password123",
			})
			Expect(err).To(HaveOccurred())
			Expect(resp).To(BeNil())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeInvalidResponse))
		})

		It("Should return error for HTTP error status", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error": "invalid username"}`))
			}))

			client = NewClient(server.URL)
			resp, err := client.CreateUser(ctx, &CreateUserRequest{
				Username: "admin",
				Password: "password123",
			})
			Expect(err).To(HaveOccurred())
			Expect(resp).To(BeNil())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeHTTP))
			Expect(err.(*Error).StatusCode).To(Equal(http.StatusBadRequest))
		})
	})

	Describe("ExchangeAuthCode", func() {
		It("Should exchange auth code for access token", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal(testAuthToken))
				Expect(r.Method).To(Equal("POST"))
				Expect(r.Header.Get("Content-Type")).To(Equal("application/x-www-form-urlencoded"))

				err := r.ParseForm()
				Expect(err).NotTo(HaveOccurred())
				Expect(r.Form.Get("grant_type")).To(Equal("authorization_code"))
				Expect(r.Form.Get("code")).To(Equal("test-auth-code"))
				Expect(r.Form.Get("client_id")).To(Equal("test-client-id"))

				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"access_token": "test-access-token",
					"token_type": "Bearer",
					"expires_in": 1800
				}`))
			}))

			client = NewClient(server.URL)
			resp, err := client.ExchangeAuthCode(ctx, "test-auth-code", "test-client-id")
			Expect(err).NotTo(HaveOccurred())
			Expect(resp).NotTo(BeNil())
			Expect(resp.AccessToken).To(Equal("test-access-token"))
			Expect(resp.TokenType).To(Equal("Bearer"))
			Expect(resp.ExpiresIn).To(Equal(1800))
		})

		It("Should return error for invalid response (no access_token)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))

			client = NewClient(server.URL)
			resp, err := client.ExchangeAuthCode(ctx, "test-auth-code", "test-client-id")
			Expect(err).To(HaveOccurred())
			Expect(resp).To(BeNil())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeInvalidResponse))
		})

		It("Should return auth error for authentication failure", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error": "invalid_grant"}`))
			}))

			client = NewClient(server.URL)
			resp, err := client.ExchangeAuthCode(ctx, "invalid-code", "test-client-id")
			Expect(err).To(HaveOccurred())
			Expect(resp).To(BeNil())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeAuth))
			Expect(err.(*Error).StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})

	Describe("SetCoreConfig", func() {
		It("Should set core config successfully", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal(testCoreConfig))
				Expect(r.Method).To(Equal("POST"))
				Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-token"))

				var req CoreConfigRequest
				err := json.NewDecoder(r.Body).Decode(&req)
				Expect(err).NotTo(HaveOccurred())
				Expect(req.LocationName).To(Equal("Home"))
				Expect(req.Latitude).To(Equal(52.2297))
				Expect(req.Longitude).To(Equal(21.0122))
				Expect(req.UnitSystem).To(Equal("metric"))
				Expect(req.Country).To(Equal("PL"))

				w.WriteHeader(http.StatusOK)
			}))

			client = NewClient(server.URL)
			err := client.SetCoreConfig(ctx, "test-token", &CoreConfigRequest{
				LocationName: "Home",
				Latitude:     52.2297,
				Longitude:    21.0122,
				UnitSystem:   "metric",
				Country:      "PL",
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return error for HTTP error status", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error": "invalid coordinates"}`))
			}))

			client = NewClient(server.URL)
			err := client.SetCoreConfig(ctx, "test-token", &CoreConfigRequest{
				LocationName: "Home",
			})
			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeHTTP))
			Expect(err.(*Error).StatusCode).To(Equal(http.StatusBadRequest))
		})
	})

	Describe("SetAnalytics", func() {
		It("Should enable analytics successfully", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal(testAnalytics))
				Expect(r.Method).To(Equal("POST"))
				Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-token"))

				var req AnalyticsRequest
				err := json.NewDecoder(r.Body).Decode(&req)
				Expect(err).NotTo(HaveOccurred())
				Expect(req.Preferences).NotTo(BeNil())
				Expect(req.Preferences.Base).To(BeTrue())
				Expect(req.Preferences.Diagnostics).To(BeTrue())

				w.WriteHeader(http.StatusOK)
			}))

			client = NewClient(server.URL)
			err := client.SetAnalytics(ctx, "test-token", true)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should disable analytics successfully", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal(testAnalytics))

				var req AnalyticsRequest
				err := json.NewDecoder(r.Body).Decode(&req)
				Expect(err).NotTo(HaveOccurred())
				Expect(req.Preferences).To(BeNil())

				w.WriteHeader(http.StatusOK)
			}))

			client = NewClient(server.URL)
			err := client.SetAnalytics(ctx, "test-token", false)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return error for HTTP error status", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}))

			client = NewClient(server.URL)
			err := client.SetAnalytics(ctx, "test-token", true)
			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeHTTP))
		})
	})

	Describe("SendWebSocketCommand", func() {
		var (
			wsServer *httptest.Server
			upgrader = websocket.Upgrader{}
		)

		It("Should send command and return result", func() {
			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				// Auth flow
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
				var authMsg map[string]interface{}
				_ = conn.ReadJSON(&authMsg)
				Expect(authMsg["access_token"]).To(Equal("test-token"))
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

				// Read command
				var cmd map[string]interface{}
				_ = conn.ReadJSON(&cmd)
				Expect(cmd["type"]).To(Equal("config/floor_registry/list"))
				Expect(cmd["id"]).To(BeNumerically("==", 1))

				// Send result
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      1,
					"type":    "result",
					"success": true,
					"result":  []map[string]interface{}{{"floor_id": "abc", "name": "Ground"}},
				})
			}))

			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			result, err := client.SendWebSocketCommand(ctx, "test-token", "config/floor_registry/list", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).NotTo(BeNil())

			var floors []map[string]interface{}
			Expect(json.Unmarshal(result, &floors)).To(Succeed())
			Expect(floors).To(HaveLen(1))
			Expect(floors[0]["name"]).To(Equal("Ground"))
		})

		It("Should pass data fields in command", func() {
			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
				var authMsg map[string]interface{}
				_ = conn.ReadJSON(&authMsg)
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

				var cmd map[string]interface{}
				_ = conn.ReadJSON(&cmd)
				Expect(cmd["type"]).To(Equal("config/floor_registry/create"))
				Expect(cmd["name"]).To(Equal("Ground Floor"))
				Expect(cmd["level"]).To(BeNumerically("==", 0))

				_ = conn.WriteJSON(map[string]interface{}{
					"id":      1,
					"type":    "result",
					"success": true,
					"result":  map[string]interface{}{"floor_id": "new-id", "name": "Ground Floor"},
				})
			}))

			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			result, err := client.SendWebSocketCommand(ctx, "test-token", "config/floor_registry/create", map[string]interface{}{
				"name":  "Ground Floor",
				"level": 0,
			})
			Expect(err).NotTo(HaveOccurred())

			var floor map[string]interface{}
			Expect(json.Unmarshal(result, &floor)).To(Succeed())
			Expect(floor["floor_id"]).To(Equal("new-id"))
		})

		It("Should return error on auth failure", func() {
			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
				var authMsg map[string]interface{}
				_ = conn.ReadJSON(&authMsg)
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_invalid", "message": "bad token"})
			}))

			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			_, err := client.SendWebSocketCommand(ctx, "bad-token", "config/floor_registry/list", nil)
			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeAuth))
		})

		It("Should return error when command fails", func() {
			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
				var authMsg map[string]interface{}
				_ = conn.ReadJSON(&authMsg)
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

				var cmd map[string]interface{}
				_ = conn.ReadJSON(&cmd)

				_ = conn.WriteJSON(map[string]interface{}{
					"id":      1,
					"type":    "result",
					"success": false,
					"error":   map[string]interface{}{"code": "not_found", "message": "Unknown command"},
				})
			}))

			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			_, err := client.SendWebSocketCommand(ctx, "test-token", "config/invalid/command", nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Unknown command"))
		})

		It("Should return error when connection fails", func() {
			client = NewClient("ws://localhost:1")

			_, err := client.SendWebSocketCommand(ctx, "test-token", "config/floor_registry/list", nil)
			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeHTTP))
		})

		AfterEach(func() {
			if wsServer != nil {
				wsServer.Close()
			}
		})
	})

	Describe("CreateLongLivedToken", func() {
		var (
			wsServer *httptest.Server
			upgrader = websocket.Upgrader{}
		)

		It("Should create long-lived token via WebSocket", func() {
			// Create WebSocket server
			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				// Send auth_required
				err = conn.WriteJSON(map[string]interface{}{
					"type": "auth_required",
				})
				Expect(err).NotTo(HaveOccurred())

				// Read auth message
				var authMsg map[string]interface{}
				err = conn.ReadJSON(&authMsg)
				Expect(err).NotTo(HaveOccurred())
				Expect(authMsg["type"]).To(Equal("auth"))
				Expect(authMsg["access_token"]).To(Equal("test-access-token"))

				// Send auth_ok
				err = conn.WriteJSON(map[string]interface{}{
					"type": "auth_ok",
				})
				Expect(err).NotTo(HaveOccurred())

				// Read token request
				var tokenReq map[string]interface{}
				err = conn.ReadJSON(&tokenReq)
				Expect(err).NotTo(HaveOccurred())
				Expect(tokenReq["type"]).To(Equal("auth/long_lived_access_token"))
				Expect(tokenReq["client_name"]).To(Equal("test-client"))
				Expect(tokenReq["lifespan"]).To(BeNumerically("==", 3650))

				// Send success response
				err = conn.WriteJSON(map[string]interface{}{
					"id":      tokenReq["id"],
					"type":    "result",
					"success": true,
					"result":  "test-long-lived-token-123",
				})
				Expect(err).NotTo(HaveOccurred())
			}))

			// Convert http URL to ws URL
			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			resp, err := client.CreateLongLivedToken(ctx, "test-access-token", &LongLivedTokenRequest{
				ClientName: "test-client",
				Lifespan:   3650,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp).NotTo(BeNil())
			Expect(resp.Token).To(Equal("test-long-lived-token-123"))
		})

		It("Should return error for auth failure", func() {
			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				// Send auth_required
				_ = conn.WriteJSON(map[string]interface{}{
					"type": "auth_required",
				})

				// Read auth message
				var authMsg map[string]interface{}
				_ = conn.ReadJSON(&authMsg)

				// Send auth_invalid
				_ = conn.WriteJSON(map[string]interface{}{
					"type":    "auth_invalid",
					"message": "Invalid access token",
				})
			}))

			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			resp, err := client.CreateLongLivedToken(ctx, "invalid-token", &LongLivedTokenRequest{
				ClientName: "test-client",
				Lifespan:   3650,
			})
			Expect(err).To(HaveOccurred())
			Expect(resp).To(BeNil())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeAuth))
		})

		It("Should return error when token creation fails", func() {
			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				// Auth flow
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
				var authMsg map[string]interface{}
				_ = conn.ReadJSON(&authMsg)
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

				// Read token request
				var tokenReq map[string]interface{}
				_ = conn.ReadJSON(&tokenReq)

				// Send error response
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      tokenReq["id"],
					"type":    "result",
					"success": false,
					"error": map[string]interface{}{
						"message": "Failed to create token",
					},
				})
			}))

			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			resp, err := client.CreateLongLivedToken(ctx, "test-token", &LongLivedTokenRequest{
				ClientName: "test-client",
				Lifespan:   3650,
			})
			Expect(err).To(HaveOccurred())
			Expect(resp).To(BeNil())
		})

		AfterEach(func() {
			if wsServer != nil {
				wsServer.Close()
			}
		})
	})

	Describe("PerformBootstrap", func() {
		var (
			wsServer          *httptest.Server
			upgrader          = websocket.Upgrader{}
			msgID             atomic.Int64
			healthCalled      bool
			onboardCalled     bool
			userCalled        bool
			configCalled      bool
			analyticsCalled   bool
			integrationCalled bool
		)

		BeforeEach(func() {
			healthCalled = false
			onboardCalled = false
			userCalled = false
			configCalled = false
			analyticsCalled = false
			integrationCalled = false
			msgID.Store(0)
		})

		It("Should perform complete bootstrap flow successfully", func() {
			// Create combined HTTP + WebSocket server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case testAPIPath:
					healthCalled = true
					w.WriteHeader(http.StatusUnauthorized)
				case testOnboardingPath:
					onboardCalled = true
					w.WriteHeader(http.StatusOK)
					steps := `[{"step":"user","done":false},` +
						`{"step":"core_config","done":false},` +
						`{"step":"analytics","done":false},` +
						`{"step":"integration","done":false}]`
					_, _ = w.Write([]byte(steps))
				case testOnboardingUsers:
					userCalled = true
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"auth_code": "test-auth-code"}`))
				case testAuthToken:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"access_token": "test-access-token", "token_type": "Bearer", "expires_in": 1800}`))
				case testCoreConfig:
					configCalled = true
					w.WriteHeader(http.StatusOK)
				case testAnalytics:
					analyticsCalled = true
					w.WriteHeader(http.StatusOK)
				case testIntegration:
					integrationCalled = true
					w.WriteHeader(http.StatusOK)
				case testWebsocket:
					// WebSocket upgrade
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer func() { _ = conn.Close() }()

					// WebSocket flow
					_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
					var authMsg map[string]interface{}
					_ = conn.ReadJSON(&authMsg)
					_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

					var tokenReq map[string]interface{}
					_ = conn.ReadJSON(&tokenReq)
					_ = conn.WriteJSON(map[string]interface{}{
						"id":      tokenReq["id"],
						"type":    "result",
						"success": true,
						"result":  "test-long-lived-token",
					})
				}
			}))

			client = NewClient(server.URL)
			token, err := client.PerformBootstrap(ctx, "admin", "password123", "Admin", "en", &BootstrapOptions{
				CreateLongLivedToken: true,
				CoreConfig: &CoreConfigRequest{
					LocationName: "Home",
					Latitude:     52.2297,
					Longitude:    21.0122,
					UnitSystem:   "metric",
				},
				EnableAnalytics: true,
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(token).To(Equal("test-long-lived-token"))
			Expect(healthCalled).To(BeTrue())
			Expect(onboardCalled).To(BeTrue())
			Expect(userCalled).To(BeTrue())
			Expect(configCalled).To(BeTrue())
			Expect(analyticsCalled).To(BeTrue())
			Expect(integrationCalled).To(BeTrue())
		})

		It("Should skip long-lived token creation when not requested", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case testAPIPath:
					w.WriteHeader(http.StatusOK)
				case testOnboardingPath:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`[{"step":"user","done":false}]`))
				case testOnboardingUsers:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"auth_code": "test-auth-code"}`))
				case testAuthToken:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"access_token": "test-access-token", "token_type": "Bearer", "expires_in": 1800}`))
				case testAnalytics:
					w.WriteHeader(http.StatusOK)
				case testIntegration:
					w.WriteHeader(http.StatusOK)
				}
			}))

			client = NewClient(server.URL)
			token, err := client.PerformBootstrap(ctx, "admin", "password123", "Admin", "en", &BootstrapOptions{
				CreateLongLivedToken: false,
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(token).To(Equal(""))
		})

		It("Should return OnboardingDone error when already completed", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case testAPIPath:
					w.WriteHeader(http.StatusOK)
				case testOnboardingPath:
					// HA returns 404 when onboarding is fully complete
					w.WriteHeader(http.StatusNotFound)
				}
			}))

			client = NewClient(server.URL)
			token, err := client.PerformBootstrap(ctx, "admin", "password123", "Admin", "en", nil)

			Expect(err).To(HaveOccurred())
			Expect(IsOnboardingDone(err)).To(BeTrue())
			Expect(token).To(Equal(""))
		})

		It("Should return NotReady error when HA not responding", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			}))

			client = NewClient(server.URL)
			token, err := client.PerformBootstrap(ctx, "admin", "password123", "Admin", "en", nil)

			Expect(err).To(HaveOccurred())
			Expect(IsNotReady(err)).To(BeTrue())
			Expect(token).To(Equal(""))
		})

		It("Should fail when user creation fails", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case testAPIPath:
					w.WriteHeader(http.StatusOK)
				case testOnboardingPath:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`[{"step":"user","done":false}]`))
				case testOnboardingUsers:
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error": "invalid username"}`))
				}
			}))

			client = NewClient(server.URL)
			token, err := client.PerformBootstrap(ctx, "admin", "pass", "Admin", "en", nil)

			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeHTTP))
			Expect(token).To(Equal(""))
		})

		AfterEach(func() {
			if wsServer != nil {
				wsServer.Close()
			}
		})
	})

	Describe("Error Helpers", func() {
		It("IsNotReady should return true for NotReady error", func() {
			err := &Error{Type: ErrorTypeNotReady, Message: "not ready"}
			Expect(IsNotReady(err)).To(BeTrue())
		})

		It("IsNotReady should return false for other error types", func() {
			err := &Error{Type: ErrorTypeHTTP, Message: "http error"}
			Expect(IsNotReady(err)).To(BeFalse())
		})

		It("IsNotReady should return false for non-Error type", func() {
			err := fmt.Errorf("generic error")
			Expect(IsNotReady(err)).To(BeFalse())
		})

		It("IsOnboardingDone should return true for OnboardingDone error", func() {
			err := &Error{Type: ErrorTypeOnboardingDone, Message: "already done"}
			Expect(IsOnboardingDone(err)).To(BeTrue())
		})

		It("IsOnboardingDone should return false for other error types", func() {
			err := &Error{Type: ErrorTypeAuth, Message: "auth error"}
			Expect(IsOnboardingDone(err)).To(BeFalse())
		})

		It("IsOnboardingDone should return false for non-Error type", func() {
			err := fmt.Errorf("generic error")
			Expect(IsOnboardingDone(err)).To(BeFalse())
		})
	})

	Describe("Client Configuration", func() {
		It("Should trim trailing slash from base URL", func() {
			client := NewClient("http://example.com/")
			Expect(client.baseURL).To(Equal("http://example.com"))
		})

		It("Should set custom timeout", func() {
			client := NewClient("http://example.com")
			client = client.WithTimeout(5 * time.Second)
			Expect(client.httpClient.Timeout).To(Equal(5 * time.Second))
		})

		It("Should have default timeout", func() {
			client := NewClient("http://example.com")
			Expect(client.httpClient.Timeout).To(Equal(defaultTimeout))
		})
	})

	Describe("CheckConfig", func() {
		It("Should handle object response with errors", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/api/services/homeassistant/check_config"))
				Expect(r.Method).To(Equal("POST"))
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-token"))
				Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))
				Expect(r.Header.Get("User-Agent")).To(Equal(userAgent))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"errors": ["config error 1", "config error 2"]}`))
			}))

			client = NewClient(server.URL)
			err := client.CheckConfig(ctx, "test-token")
			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeHTTP))
			Expect(err.(*Error).Message).To(ContainSubstring("config validation errors"))
		})

		It("Should handle object response without errors", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/api/services/homeassistant/check_config"))
				Expect(r.Method).To(Equal("POST"))
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-token"))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result": "valid"}`))
			}))

			client = NewClient(server.URL)
			err := client.CheckConfig(ctx, "test-token")
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should handle empty array response", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/api/services/homeassistant/check_config"))
				Expect(r.Method).To(Equal("POST"))
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-token"))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`[]`))
			}))

			client = NewClient(server.URL)
			err := client.CheckConfig(ctx, "test-token")
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should handle array response with elements", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/api/services/homeassistant/check_config"))
				Expect(r.Method).To(Equal("POST"))
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-token"))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`[{"service": "homeassistant.check_config"}]`))
			}))

			client = NewClient(server.URL)
			err := client.CheckConfig(ctx, "test-token")
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return HTTP error for non-200 status", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error": "unauthorized"}`))
			}))

			client = NewClient(server.URL)
			err := client.CheckConfig(ctx, "test-token")
			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeHTTP))
			Expect(err.(*Error).StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("Should return error for invalid JSON response", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`invalid json`))
			}))

			client = NewClient(server.URL)
			err := client.CheckConfig(ctx, "test-token")
			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeInvalidResponse))
		})

		It("Should return error for unexpected response format", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`"just a string"`))
			}))

			client = NewClient(server.URL)
			err := client.CheckConfig(ctx, "test-token")
			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeInvalidResponse))
			Expect(err.(*Error).Message).To(ContainSubstring("unexpected response format"))
		})
	})

	Describe("GetConfig", func() {
		const validConfigResponse = `{
			"components": ["automation", "script", "scene", "homeassistant", "http"],
			"version": "2024.2.0",
			"location_name": "Test Home",
			"time_zone": "Europe/Berlin",
			"config_dir": "/config",
			"whitelist_external_dirs": ["/config/custom_components"]
		}`

		It("Should successfully get config", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/api/config"))
				Expect(r.Method).To(Equal("GET"))
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-token"))
				Expect(r.Header.Get("User-Agent")).To(Equal(userAgent))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(validConfigResponse))
			}))

			client = NewClient(server.URL)
			config, err := client.GetConfig(ctx, "test-token")
			Expect(err).NotTo(HaveOccurred())
			Expect(config).NotTo(BeNil())
			Expect(config.Version).To(Equal("2024.2.0"))
			Expect(config.LocationName).To(Equal("Test Home"))
			Expect(config.TimeZone).To(Equal("Europe/Berlin"))
			Expect(config.ConfigDir).To(Equal("/config"))
			Expect(config.Components).To(HaveLen(5))
			Expect(config.Components).To(ContainElement("automation"))
			Expect(config.Components).To(ContainElement("script"))
			Expect(config.Components).To(ContainElement("scene"))
		})

		It("Should return error for 401 Unauthorized", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message": "Invalid token"}`))
			}))

			client = NewClient(server.URL)
			config, err := client.GetConfig(ctx, "invalid-token")
			Expect(err).To(HaveOccurred())
			Expect(config).To(BeNil())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeHTTP))
			Expect(err.(*Error).StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("Should return error for 503 Service Unavailable", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`Service Unavailable`))
			}))

			client = NewClient(server.URL)
			config, err := client.GetConfig(ctx, "test-token")
			Expect(err).To(HaveOccurred())
			Expect(config).To(BeNil())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeHTTP))
			Expect(err.(*Error).StatusCode).To(Equal(http.StatusServiceUnavailable))
		})

		It("Should return error for invalid JSON response", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`invalid json`))
			}))

			client = NewClient(server.URL)
			config, err := client.GetConfig(ctx, "test-token")
			Expect(err).To(HaveOccurred())
			Expect(config).To(BeNil())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeInvalidResponse))
			Expect(err.(*Error).Message).To(ContainSubstring("failed to decode config response"))
		})

		It("Should handle timeout", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(100 * time.Millisecond)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(validConfigResponse))
			}))

			client = NewClient(server.URL).WithTimeout(10 * time.Millisecond)
			config, err := client.GetConfig(ctx, "test-token")
			Expect(err).To(HaveOccurred())
			Expect(config).To(BeNil())
			Expect(err.(*Error).Type).To(Equal(ErrorTypeNotReady))
		})
	})

	Describe("IsComponentLoaded", func() {
		const configWithScript = `{
			"components": ["automation", "script", "scene", "homeassistant"],
			"version": "2024.2.0"
		}`

		const configWithoutScript = `{
			"components": ["automation", "scene", "homeassistant"],
			"version": "2024.2.0"
		}`

		It("Should return true when component is loaded", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/api/config"))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(configWithScript))
			}))

			client = NewClient(server.URL)
			loaded, err := client.IsComponentLoaded(ctx, "test-token", "script")
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded).To(BeTrue())
		})

		It("Should return false when component is not loaded", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(configWithoutScript))
			}))

			client = NewClient(server.URL)
			loaded, err := client.IsComponentLoaded(ctx, "test-token", "script")
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded).To(BeFalse())
		})

		It("Should check for automation component", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(configWithScript))
			}))

			client = NewClient(server.URL)
			loaded, err := client.IsComponentLoaded(ctx, "test-token", "automation")
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded).To(BeTrue())
		})

		It("Should check for scene component", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(configWithScript))
			}))

			client = NewClient(server.URL)
			loaded, err := client.IsComponentLoaded(ctx, "test-token", "scene")
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded).To(BeTrue())
		})

		It("Should return error when GetConfig fails", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message": "Invalid token"}`))
			}))

			client = NewClient(server.URL)
			loaded, err := client.IsComponentLoaded(ctx, "invalid-token", "script")
			Expect(err).To(HaveOccurred())
			Expect(loaded).To(BeFalse())
			Expect(err.(*Error).StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("Should handle non-existent component", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(configWithScript))
			}))

			client = NewClient(server.URL)
			loaded, err := client.IsComponentLoaded(ctx, "test-token", "nonexistent_component")
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded).To(BeFalse())
		})
	})

	Describe("PutAutomation", func() {
		automationData := map[string]interface{}{
			"alias": "Test automation",
			"trigger": []interface{}{
				map[string]interface{}{"trigger": "sun", "event": "sunset"},
			},
			"action": []interface{}{
				map[string]interface{}{"action": "light.turn_on", "target": map[string]interface{}{"area_id": "living_room"}},
			},
		}

		It("Should POST automation successfully", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Method).To(Equal("POST"))
				Expect(r.URL.Path).To(Equal("/api/config/automation/config/test-id"))
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-token"))
				Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result": "ok"}`))
			}))

			client = NewClient(server.URL)
			err := client.PutAutomation(ctx, "test-token", "test-id", automationData)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return error on non-200 response", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message": "invalid automation"}`))
			}))

			client = NewClient(server.URL)
			err := client.PutAutomation(ctx, "test-token", "test-id", automationData)
			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("Should return NotReady error when HA is unreachable", func() {
			client = NewClient("http://localhost:19999")
			err := client.PutAutomation(ctx, "test-token", "test-id", automationData)
			Expect(err).To(HaveOccurred())
			Expect(IsNotReady(err)).To(BeTrue())
		})
	})

	Describe("DeleteAutomation", func() {
		It("Should DELETE automation successfully", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Method).To(Equal("DELETE"))
				Expect(r.URL.Path).To(Equal("/api/config/automation/config/test-id"))
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-token"))
				w.WriteHeader(http.StatusOK)
			}))

			client = NewClient(server.URL)
			err := client.DeleteAutomation(ctx, "test-token", "test-id")
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return nil when automation not found (404 — idempotent)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}))

			client = NewClient(server.URL)
			err := client.DeleteAutomation(ctx, "test-token", "test-id")
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return error on 500 response", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message": "internal error"}`))
			}))

			client = NewClient(server.URL)
			err := client.DeleteAutomation(ctx, "test-token", "test-id")
			Expect(err).To(HaveOccurred())
			Expect(err.(*Error).StatusCode).To(Equal(http.StatusInternalServerError))
		})
	})

	Describe("PutScene", func() {
		sceneData := map[string]interface{}{
			"name": "Evening",
			"entities": map[string]interface{}{
				"light.living_room": map[string]interface{}{"state": "on", "brightness": 100},
			},
		}

		It("Should POST scene successfully", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Method).To(Equal("POST"))
				Expect(r.URL.Path).To(Equal("/api/config/scene/config/test-scene-id"))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result": "ok"}`))
			}))

			client = NewClient(server.URL)
			err := client.PutScene(ctx, "test-token", "test-scene-id", sceneData)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return nil on DELETE 404 (idempotent)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}))

			client = NewClient(server.URL)
			err := client.DeleteScene(ctx, "test-token", "test-scene-id")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("PutScript", func() {
		scriptData := map[string]interface{}{
			"alias": "Morning routine",
			"sequence": []interface{}{
				map[string]interface{}{"action": "light.turn_on", "target": map[string]interface{}{"entity_id": "light.bedroom"}},
			},
		}

		It("Should POST script successfully", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Method).To(Equal("POST"))
				Expect(r.URL.Path).To(Equal("/api/config/script/config/test-script-id"))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result": "ok"}`))
			}))

			client = NewClient(server.URL)
			err := client.PutScript(ctx, "test-token", "test-script-id", scriptData)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should return nil on DELETE 404 (idempotent)", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}))

			client = NewClient(server.URL)
			err := client.DeleteScript(ctx, "test-token", "test-script-id")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("GetBackupConfig", func() {
		var (
			wsServer *httptest.Server
			upgrader = websocket.Upgrader{}
		)

		AfterEach(func() {
			if wsServer != nil {
				wsServer.Close()
			}
		})

		It("Should return backup config from HA", func() {
			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
				var authMsg map[string]interface{}
				_ = conn.ReadJSON(&authMsg)
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

				var cmd map[string]interface{}
				_ = conn.ReadJSON(&cmd)
				Expect(cmd["type"]).To(Equal("backup/config/info"))

				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": true,
					"result": map[string]interface{}{
						"schedule": map[string]interface{}{
							"recurrence": "daily",
							"time":       "03:00:00",
						},
						"retention": map[string]interface{}{
							"copies": 7,
							"days":   nil,
						},
						"create_backup": map[string]interface{}{
							"include_database": true,
							"agent_ids":        []string{"backup.local"},
						},
					},
				})
			}))

			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			config, err := client.GetBackupConfig(ctx, "test-token")
			Expect(err).NotTo(HaveOccurred())
			Expect(config).NotTo(BeNil())
			Expect(config.Schedule.Recurrence).To(Equal("daily"))
			Expect(config.Schedule.Time).NotTo(BeNil())
			Expect(*config.Schedule.Time).To(Equal("03:00:00"))
			Expect(config.Retention.Copies).NotTo(BeNil())
			Expect(*config.Retention.Copies).To(Equal(7))
			Expect(config.Retention.Days).To(BeNil())
			Expect(config.CreateBackup.IncludeDatabase).NotTo(BeNil())
			Expect(*config.CreateBackup.IncludeDatabase).To(BeTrue())
			Expect(config.CreateBackup.AgentIDs).To(ConsistOf("backup.local"))
		})

		It("Should handle config with null time and unlimited retention", func() {
			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
				var authMsg map[string]interface{}
				_ = conn.ReadJSON(&authMsg)
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

				var cmd map[string]interface{}
				_ = conn.ReadJSON(&cmd)

				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": true,
					"result": map[string]interface{}{
						"schedule": map[string]interface{}{
							"recurrence": "never",
							"time":       nil,
						},
						"retention": map[string]interface{}{
							"copies": nil,
							"days":   nil,
						},
						"create_backup": map[string]interface{}{
							"include_database": false,
						},
					},
				})
			}))

			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			config, err := client.GetBackupConfig(ctx, "test-token")
			Expect(err).NotTo(HaveOccurred())
			Expect(config.Schedule.Recurrence).To(Equal("never"))
			Expect(config.Schedule.Time).To(BeNil())
			Expect(config.Retention.Copies).To(BeNil())
			Expect(config.Retention.Days).To(BeNil())
			Expect(config.CreateBackup.IncludeDatabase).NotTo(BeNil())
			Expect(*config.CreateBackup.IncludeDatabase).To(BeFalse())
		})

		It("Should return error on WS failure", func() {
			client = NewClient("ws://localhost:19999")
			config, err := client.GetBackupConfig(ctx, "test-token")
			Expect(err).To(HaveOccurred())
			Expect(config).To(BeNil())
		})
	})

	Describe("ConfigureBackup", func() {
		var (
			wsServer *httptest.Server
			upgrader = websocket.Upgrader{}
		)

		AfterEach(func() {
			if wsServer != nil {
				wsServer.Close()
			}
		})

		It("Should send backup config update with all fields", func() {
			var receivedCmd map[string]json.RawMessage

			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
				var authMsg map[string]interface{}
				_ = conn.ReadJSON(&authMsg)
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

				_ = conn.ReadJSON(&receivedCmd)

				_ = conn.WriteJSON(map[string]interface{}{
					"id":      1,
					"type":    "result",
					"success": true,
					"result":  nil,
				})
			}))

			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			copies := 5
			includeDB := true
			timeStr := "04:00:00"
			err := client.ConfigureBackup(ctx, "test-token", &BackupConfigRequest{
				Schedule: &BackupSchedule{
					Recurrence: "daily",
					Time:       &timeStr,
				},
				Retention: &BackupRetention{
					Copies: &copies,
				},
				CreateBackup: &BackupCreateConfig{
					IncludeDatabase: &includeDB,
					AgentIDs:        []string{"backup.local"},
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify the command type
			var cmdType string
			Expect(json.Unmarshal(receivedCmd["type"], &cmdType)).To(Succeed())
			Expect(cmdType).To(Equal("backup/config/update"))

			// Verify schedule was sent
			var schedule BackupSchedule
			Expect(json.Unmarshal(receivedCmd["schedule"], &schedule)).To(Succeed())
			Expect(schedule.Recurrence).To(Equal("daily"))
			Expect(*schedule.Time).To(Equal("04:00:00"))

			// Verify retention was sent
			var retention BackupRetention
			Expect(json.Unmarshal(receivedCmd["retention"], &retention)).To(Succeed())
			Expect(*retention.Copies).To(Equal(5))
		})

		It("Should send only provided fields", func() {
			var receivedCmd map[string]json.RawMessage

			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
				var authMsg map[string]interface{}
				_ = conn.ReadJSON(&authMsg)
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

				_ = conn.ReadJSON(&receivedCmd)

				_ = conn.WriteJSON(map[string]interface{}{
					"id":      1,
					"type":    "result",
					"success": true,
					"result":  nil,
				})
			}))

			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			err := client.ConfigureBackup(ctx, "test-token", &BackupConfigRequest{
				Schedule: &BackupSchedule{
					Recurrence: "never",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Should have schedule but NOT retention or create_backup
			Expect(receivedCmd).To(HaveKey("schedule"))
			Expect(receivedCmd).NotTo(HaveKey("retention"))
			Expect(receivedCmd).NotTo(HaveKey("create_backup"))
		})

		It("Should return error on WS command failure", func() {
			wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()

				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
				var authMsg map[string]interface{}
				_ = conn.ReadJSON(&authMsg)
				_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

				var cmd map[string]interface{}
				_ = conn.ReadJSON(&cmd)

				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": false,
					"error": map[string]interface{}{
						"code":    "unknown_error",
						"message": "Backup component not loaded",
					},
				})
			}))

			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
			client = NewClient(wsURL)

			err := client.ConfigureBackup(ctx, "test-token", &BackupConfigRequest{
				Schedule: &BackupSchedule{Recurrence: "daily"},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Backup component not loaded"))
		})
	})
})
