/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package haclient

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// TestWithRootCAs verifies the native-TLS trust plumbing: a CA pool is installed
// on the transport and certificate verification is never disabled.
func TestWithRootCAs(t *testing.T) {
	c := NewClient("https://home.default.svc.cluster.local:8123").
		WithRootCAs([]byte("-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----"))

	tr, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c.httpClient.Transport)
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("expected TLSClientConfig to be configured")
	}
	if tr.TLSClientConfig.RootCAs == nil {
		t.Fatal("expected RootCAs pool to be set")
	}
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify must never be enabled for native TLS")
	}
}

// TestWSAuthConnectHonorsRootCAs is the regression test for a bug found live
// in CI: WithRootCAs only ever configured c.httpClient's transport (used by
// plain HTTP calls like CheckHealth), never the separate websocket.Dialer
// wsAuthConnect builds for every WS command (GetHTTPConfig,
// ConfigureHTTPConfig, PromoteHTTPConfig, and everything else that goes
// through SendWebSocketCommand). A wss:// connection therefore silently fell
// back to Go's default system root CAs and rejected any cert-manager or
// bring-your-own certificate — the operator was never actually able to use
// its own native-TLS WS calls over HTTPS at all, regardless of how correctly
// TLSReady/scheme selection was otherwise computed.
func TestWSAuthConnectHonorsRootCAs(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			"id": cmd["id"], "type": "result", "success": true,
			"result": map[string]interface{}{
				"stable":  map[string]interface{}{"server_port": 8123},
				"pending": nil, "revert_at": nil, "active_config_type": "stable",
			},
		})
	}))
	defer server.Close()

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	wssURL := "wss" + strings.TrimPrefix(server.URL, "https")

	t.Run("fails closed against a self-signed server without WithRootCAs", func(t *testing.T) {
		c := NewClient(wssURL)
		if _, err := c.GetHTTPConfig(context.Background(), "test-token"); err == nil {
			t.Fatal("expected GetHTTPConfig to fail: the server's self-signed cert is untrusted by default")
		}
	})

	t.Run("succeeds once WithRootCAs trusts the server's certificate", func(t *testing.T) {
		c := NewClient(wssURL).WithRootCAs(certPEM)
		if _, err := c.GetHTTPConfig(context.Background(), "test-token"); err != nil {
			t.Fatalf("expected GetHTTPConfig to succeed with WithRootCAs configured, got: %v", err)
		}
	})
}
