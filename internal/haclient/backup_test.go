package haclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestGetBackupConfigRejectsMissingStructure(t *testing.T) {
	for name, payload := range map[string]any{
		"null": nil, "empty": map[string]any{}, "unrelated": map[string]any{"other": true},
		"null config": map[string]any{"config": nil},
	} {
		t.Run(name, func(t *testing.T) {
			server := newBackupWebSocketServer(t, func(_ map[string]any, conn *websocket.Conn) {
				_ = conn.WriteJSON(map[string]any{"id": 1, "type": "result", "success": true, "result": payload})
			})
			defer server.Close()
			if _, err := NewClient(server.URL).GetBackupConfig(context.Background(), "token"); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestDownloadBackupUsesStreamingTimeoutWithoutChangingClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte("end"))
	}))
	defer server.Close()
	c := NewClient(server.URL).WithTimeout(30 * time.Millisecond)
	response, err := c.DownloadBackup(context.Background(), "token", "archive", "backup.local")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil || string(data) != "startend" {
		t.Fatal("stream did not complete")
	}
	if c.httpClient.Timeout != 30*time.Millisecond {
		t.Fatal("shared client timeout was changed")
	}
}
