package haclient

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestConfigureBackupNeverSendsPassword(t *testing.T) {
	password := "synthetic-fixture-key"
	server := newBackupWebSocketServer(t, func(command map[string]any, conn *websocket.Conn) {
		encoded, _ := json.Marshal(command)
		if strings.Contains(string(encoded), password) || strings.Contains(string(encoded), "password") {
			t.Errorf("backup update contains a password: %s", encoded)
		}
		_ = conn.WriteJSON(map[string]any{"id": 1, "type": "result", "success": true, "result": map[string]any{}})
	})
	defer server.Close()

	err := NewClient(server.URL).ConfigureBackup(context.Background(), "token", &BackupConfigRequest{
		CreateBackup: &BackupCreateConfig{Password: &password, AgentIDs: []string{"backup.local"}},
	})
	if err != nil {
		t.Fatalf("ConfigureBackup() error = %v", err)
	}
}

func TestGetBackupDetailsParsesFixtureShape(t *testing.T) {
	server := newBackupWebSocketServer(t, func(command map[string]any, conn *websocket.Conn) {
		if command["backup_id"] != "fixture-backup-001" {
			t.Errorf("backup_id = %v", command["backup_id"])
		}
		_ = conn.WriteJSON(map[string]any{"id": 1, "type": "result", "success": true, "result": map[string]any{
			"backup": map[string]any{"backup_id": "fixture-backup-001", "date": "2026-11-03T12:00:00Z"},
		}})
	})
	defer server.Close()

	details, err := NewClient(server.URL).GetBackupDetails(context.Background(), "token", "fixture-backup-001")
	if err != nil || details.Backup.ID != "fixture-backup-001" {
		t.Fatalf("GetBackupDetails() = %#v, %v", details, err)
	}
}
