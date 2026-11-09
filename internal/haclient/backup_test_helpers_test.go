package haclient

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

// newBackupWebSocketServer provides a local protocol fixture for backup-client tests.
func newBackupWebSocketServer(t *testing.T, handler func(map[string]any, *websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
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
		if err := conn.ReadJSON(&command); err == nil {
			handler(command, conn)
		}
	}))
}
