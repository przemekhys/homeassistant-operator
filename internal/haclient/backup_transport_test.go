package haclient

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBackupTransportValidatesResultEnvelopeAndSanitizesFailures(t *testing.T) {
	server := newBackupWebSocketServer(t, func(command map[string]any, conn *websocket.Conn) {
		if command["id"] != float64(1) || command["type"] != "backup/info" {
			t.Errorf("unexpected command: %#v", command)
		}
		_ = conn.WriteJSON(map[string]any{
			"id": 1, "type": "result", "success": false,
			"error": map[string]any{"code": "unknown_command", "message": "raw backup payload: secret"},
		})
	})
	defer server.Close()

	_, err := NewClient(server.URL).sendBackupCommand(context.Background(), "token", "backup/info", nil)
	if err == nil {
		t.Fatal("expected rejected command error")
	}
	if strings.Contains(err.Error(), "raw backup payload") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("backup transport exposed raw response: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown_command") {
		t.Fatalf("backup transport lost HA error code: %v", err)
	}
}

func TestBackupTransportRejectsEventsAndHonorsContext(t *testing.T) {
	server := newBackupWebSocketServer(t, func(command map[string]any, conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]any{"id": 1, "type": "event", "event": map[string]any{}})
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := NewClient(server.URL).sendBackupCommand(ctx, "token", "backup/info", nil)
	if err == nil || !strings.Contains(err.Error(), "backup command transport failed") {
		t.Fatalf("event must not satisfy a command: %v", err)
	}
}
