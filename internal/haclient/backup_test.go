package haclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func backupWebSocketServer(t *testing.T, reply func(map[string]interface{}) map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(map[string]interface{}{"type": "auth_required"}); err != nil {
			return
		}
		var auth map[string]interface{}
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]interface{}{"type": "auth_ok"}); err != nil {
			return
		}
		var command map[string]interface{}
		if err := conn.ReadJSON(&command); err != nil {
			return
		}
		response := reply(command)
		response["id"] = command["id"]
		response["type"] = "result"
		if err := conn.WriteJSON(response); err != nil {
			t.Errorf("reply: %v", err)
		}
	}))
}

func backupClient(server *httptest.Server) *Client {
	return NewClient("ws" + strings.TrimPrefix(server.URL, "http"))
}

func TestListBackupAgentsDecodesNativeResult(t *testing.T) {
	server := backupWebSocketServer(t, func(command map[string]interface{}) map[string]interface{} {
		if command["type"] != "backup/agents/info" {
			t.Errorf("type = %v", command["type"])
		}
		return map[string]interface{}{"success": true, "result": map[string]interface{}{"agents": []map[string]interface{}{{"agent_id": "core", "name": "Home Assistant Core"}}}}
	})
	defer server.Close()
	agents, err := backupClient(server).ListBackupAgents(context.Background(), "token")
	if err != nil || len(agents) != 1 || agents[0].ID != "core" {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
}

func TestGenerateBackupUsesSingleNativeCommandAndClassifiesBusy(t *testing.T) {
	calls := 0
	server := backupWebSocketServer(t, func(command map[string]interface{}) map[string]interface{} {
		calls++
		if command["type"] != "backup/generate" || command["password"] != "secret" {
			t.Errorf("unexpected command: %#v", command)
		}
		return map[string]interface{}{"success": false, "error": map[string]interface{}{"code": "busy", "message": "redacted by adapter"}}
	})
	defer server.Close()
	_, err := backupClient(server).GenerateBackup(context.Background(), "token", GenerateBackupRequest{AgentIDs: []string{"core"}, IncludeDatabase: true, Password: "secret"})
	var backupErr *BackupError
	if !errors.As(err, &backupErr) || backupErr.Kind != BackupErrorBusyBeforeAcceptance {
		t.Fatalf("err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("native command count = %d, want 1", calls)
	}
}

func TestRestoreDisconnectIsUnknownAndIsNotRetried(t *testing.T) {
	calls := 0
	server := backupWebSocketServer(t, func(command map[string]interface{}) map[string]interface{} {
		calls++
		return map[string]interface{}{} // close after an invalid response: caller receives no accepted result.
	})
	defer server.Close()
	err := backupClient(server).RestoreBackup(context.Background(), "token", RestoreBackupRequest{BackupID: "archive", AgentID: "core", Password: "secret", RestoreHomeAssistant: true})
	var backupErr *BackupError
	if !errors.As(err, &backupErr) || backupErr.Kind != BackupErrorDispatchUnknown {
		t.Fatalf("err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("native command count = %d, want 1", calls)
	}
}

func TestBackupErrorsDoNotRevealPassword(t *testing.T) {
	err := classifyBackupError("invalid_password", errors.New("secret-value"))
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("safe error leaked secret: %v", err)
	}
}
