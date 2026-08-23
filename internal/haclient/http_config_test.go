package haclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Specs registered here run as part of the single Ginkgo suite entry point
// (TestHAClient) declared in client_test.go — Ginkgo only supports one
// RunSpecs() call per test binary.
var _ = Describe("HAClient http/config methods", func() {
	var (
		wsServer *httptest.Server
		client   *Client
		ctx      context.Context
		upgrader = websocket.Upgrader{}
	)

	BeforeEach(func() {
		ctx = context.Background()
	})

	AfterEach(func() {
		if wsServer != nil {
			wsServer.Close()
		}
	})

	// serveOnce upgrades a single WebSocket connection, performs the standard
	// auth_required/auth_ok handshake, reads exactly one command, and replies
	// with the given result/success/error via respond.
	serveOnce := func(respond func(cmd map[string]interface{}, conn *websocket.Conn)) {
		wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
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
			respond(cmd, conn)
		}))
		wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
		client = NewClient(wsURL)
	}

	Describe("GetHTTPConfig", func() {
		It("returns the parsed stable/pending/revert_at/active_config_type", func() {
			serveOnce(func(cmd map[string]interface{}, conn *websocket.Conn) {
				Expect(cmd["type"]).To(Equal("http/config"))
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": true,
					"result": map[string]interface{}{
						"stable":             map[string]interface{}{"server_port": 8123},
						"pending":            nil,
						"revert_at":          nil,
						"active_config_type": "stable",
					},
				})
			})

			cfg, err := client.GetHTTPConfig(ctx, "test-token")
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Stable.ServerPort).To(Equal(8123))
			Expect(cfg.Pending).To(BeNil())
			Expect(cfg.ActiveConfigType).To(Equal("stable"))
		})

		It("surfaces a pending config populated with Error/ErrorMessage after a reverted trial", func() {
			serveOnce(func(cmd map[string]interface{}, conn *websocket.Conn) {
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": true,
					"result": map[string]interface{}{
						"stable": map[string]interface{}{"server_port": 8123},
						"pending": map[string]interface{}{
							"server_port":   8123,
							"error":         "apply_failed",
							"error_message": "Failed to bind to the configured address",
						},
						"revert_at":          nil,
						"active_config_type": "stable",
					},
				})
			})

			cfg, err := client.GetHTTPConfig(ctx, "test-token")
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Pending).NotTo(BeNil())
			Expect(cfg.Pending.Error).To(Equal("apply_failed"))
			Expect(cfg.Pending.ErrorMessage).To(Equal("Failed to bind to the configured address"))
		})

		It("returns ErrorTypeUnknownCommand when HA reports unknown_command", func() {
			serveOnce(func(cmd map[string]interface{}, conn *websocket.Conn) {
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": false,
					"error": map[string]interface{}{
						"code":    "unknown_command",
						"message": "Unknown command",
					},
				})
			})

			_, err := client.GetHTTPConfig(ctx, "test-token")
			Expect(err).To(HaveOccurred())
			Expect(IsUnknownCommand(err)).To(BeTrue())
		})
	})

	Describe("ConfigureHTTPConfig", func() {
		It("sends the full config and returns nil on success", func() {
			serveOnce(func(cmd map[string]interface{}, conn *websocket.Conn) {
				Expect(cmd["type"]).To(Equal("http/config/configure"))
				config, ok := cmd["config"].(map[string]interface{})
				Expect(ok).To(BeTrue())
				Expect(config["ssl_certificate"]).To(Equal("/config/ssl/tls.crt"))
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": true,
					"result":  map[string]interface{}{"restart": true},
				})
			})

			err := client.ConfigureHTTPConfig(ctx, "test-token", &HTTPConfigData{
				SSLCertificate: "/config/ssl/tls.crt",
				SSLKey:         "/config/ssl/tls.key",
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("sends an explicit false for ip_ban_enabled/use_x_frame_options, not omitting it", func() {
			serveOnce(func(cmd map[string]interface{}, conn *websocket.Conn) {
				config, ok := cmd["config"].(map[string]interface{})
				Expect(ok).To(BeTrue())
				// A field the operator omits here is not "kept unchanged" —
				// http/config/configure replaces the whole stored config, so
				// an omitted ip_ban_enabled/use_x_frame_options would fall
				// back to HA's own schema default instead of the false the
				// user actually configured. HaveKey (not just checking the
				// value) is the point: omitempty on a plain bool would have
				// dropped the key entirely for a false value.
				Expect(config).To(HaveKeyWithValue("ip_ban_enabled", false))
				Expect(config).To(HaveKeyWithValue("use_x_frame_options", false))
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": true,
					"result":  map[string]interface{}{"restart": true},
				})
			})

			falseVal := false
			err := client.ConfigureHTTPConfig(ctx, "test-token", &HTTPConfigData{
				IPBanEnabled:     &falseVal,
				UseXFrameOptions: &falseVal,
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("omits ip_ban_enabled/use_x_frame_options entirely when never configured", func() {
			serveOnce(func(cmd map[string]interface{}, conn *websocket.Conn) {
				config, ok := cmd["config"].(map[string]interface{})
				Expect(ok).To(BeTrue())
				Expect(config).NotTo(HaveKey("ip_ban_enabled"))
				Expect(config).NotTo(HaveKey("use_x_frame_options"))
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": true,
					"result":  map[string]interface{}{"restart": true},
				})
			})

			err := client.ConfigureHTTPConfig(ctx, "test-token", &HTTPConfigData{
				SSLCertificate: "/config/ssl/tls.crt",
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("serializes cfg == nil as JSON null", func() {
			serveOnce(func(cmd map[string]interface{}, conn *websocket.Conn) {
				Expect(cmd).To(HaveKey("config"))
				Expect(cmd["config"]).To(BeNil())
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": true,
					"result":  map[string]interface{}{"restart": false},
				})
			})

			Expect(client.ConfigureHTTPConfig(ctx, "test-token", nil)).To(Succeed())
		})

		It("returns ErrorTypeNotRunning when HA reports not_running", func() {
			serveOnce(func(cmd map[string]interface{}, conn *websocket.Conn) {
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": false,
					"error": map[string]interface{}{
						"code":    "not_running",
						"message": "Home Assistant is not running",
					},
				})
			})

			err := client.ConfigureHTTPConfig(ctx, "test-token", &HTTPConfigData{})
			Expect(err).To(HaveOccurred())
			Expect(IsNotRunning(err)).To(BeTrue())
		})
	})

	Describe("PromoteHTTPConfig", func() {
		It("succeeds when there is a pending config to promote", func() {
			serveOnce(func(cmd map[string]interface{}, conn *websocket.Conn) {
				Expect(cmd["type"]).To(Equal("http/config/promote"))
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": true,
					"result":  nil,
				})
			})

			Expect(client.PromoteHTTPConfig(ctx, "test-token")).To(Succeed())
		})

		It("returns an error when there is nothing to promote", func() {
			serveOnce(func(cmd map[string]interface{}, conn *websocket.Conn) {
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      cmd["id"],
					"type":    "result",
					"success": false,
					"error": map[string]interface{}{
						"code":    "not_allowed",
						"message": "No pending config to promote",
					},
				})
			})

			Expect(client.PromoteHTTPConfig(ctx, "test-token")).To(HaveOccurred())
		})
	})
})
