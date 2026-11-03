package haclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestGetBackupDetailsDecodesNativeAgentMapAndFailures(t *testing.T) {
	server := backupWebSocketServer(t, func(map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"success": true, "result": map[string]any{
			"backup": map[string]any{
				"backup_id": "archive", "agents": map[string]any{"backup.local": map[string]any{"protected": true}},
				"failed_agent_ids": []string{"failed"},
			},
			"agent_errors": map[string]string{"unreachable": "native-secret-error-canary"},
		}}
	})
	defer server.Close()
	details, err := backupClient(server).GetBackupDetails(context.Background(), "token", "archive")
	if err != nil {
		t.Fatal(err)
	}
	if details.BackupID != "archive" || len(details.Agents) != 3 {
		t.Fatal("native backup/destination envelope was lost")
	}
	for _, agent := range details.Agents {
		if agent.AgentID == "backup.local" {
			if !agent.Protected || agent.Error != "" {
				t.Fatal("protected successful destination was lost")
			}
		} else if agent.Error != BackupErrorSourceUnavailable {
			t.Fatal("native destination failure was not classified")
		}
	}
}

func TestGetBackupDetailsRejectsMissingOrMismatchedArchive(t *testing.T) {
	for _, backup := range []any{nil, map[string]any{"backup_id": "different"}, "native-secret-error-canary"} {
		server := backupWebSocketServer(t, func(map[string]interface{}) map[string]interface{} {
			return map[string]interface{}{"success": true, "result": map[string]any{"backup": backup}}
		})
		result, err := backupClient(server).GetBackupDetails(context.Background(), "token", "archive")
		server.Close()
		if err == nil || result != nil {
			t.Fatal("unverified backup identity was accepted")
		}
		if strings.Contains(err.Error(), "native-secret-error-canary") {
			t.Fatal("raw archive response was disclosed")
		}
	}
}

func TestSubscribeBackupEventsAcceptsNativeEventBeforeAcknowledgement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
		var command map[string]any
		if conn.ReadJSON(&command) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
		if conn.ReadJSON(&command) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"id": command["id"], "type": "event", "event": map[string]any{
			"manager_state": "create_backup", "state": "in_progress", "stage": "home_assistant",
		}})
		_ = conn.WriteJSON(map[string]any{"id": command["id"], "type": "result", "success": true, "result": nil})
		_ = conn.WriteJSON(map[string]any{"id": command["id"], "type": "event", "event": map[string]any{
			"manager_state": "create_backup", "state": "completed", "stage": nil,
		}})
		// Keep the transport open so cancellation, rather than server closure,
		// must end observation without replaying the subscription or a command.
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var stages []string
	err := backupClient(server).SubscribeBackupEvents(ctx, "token", func(event BackupEvent) {
		stages = append(stages, event.State)
		if event.State == "completed" {
			cancel()
		}
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if strings.Join(stages, ",") != "in_progress,completed" {
		t.Fatal("native events around the result acknowledgement were lost")
	}
}
