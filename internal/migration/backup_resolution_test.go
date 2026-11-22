package migration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
)

type resolutionReadSource struct {
	backupReader
	details *haclient.BackupDetails
	reads   int
}

func TestReadBackupResolutionUsesOnlyReadTransport(t *testing.T) {
	var generate atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
		var auth map[string]any
		if conn.ReadJSON(&auth) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
		var cmd map[string]any
		if conn.ReadJSON(&cmd) != nil {
			return
		}
		var result any
		switch cmd["type"] {
		case "backup/config/info":
			result = map[string]any{"schedule": map[string]any{"recurrence": "daily"}, "create_backup": map[string]any{"include_database": true, "agent_ids": []string{"backup.local"}, "password": "synthetic-key"}}
		case "backup/agents/info":
			result = map[string]any{"agents": []any{map[string]any{"agent_id": "backup.local"}}}
		case "backup/details":
			if cmd["backup_id"] != "exact-id" {
				t.Error("unexpected backup identifier")
			}
			result = map[string]any{"agent_errors": map[string]any{}, "backup": map[string]any{"backup_id": "exact-id", "date": "2026-11-16T12:00:00Z", "homeassistant_version": "2026.11.0", "with_automatic_settings": true, "database_included": true, "homeassistant_included": true, "folders": []any{}, "addons": []any{}, "failed_folders": []any{}, "failed_addons": []any{}, "failed_agent_ids": []any{}, "agents": map[string]any{"backup.local": map[string]any{"protected": true, "size": 123}}}}
		default:
			generate.Add(1)
			t.Error("unexpected HA command")
		}
		_ = conn.WriteJSON(map[string]any{"id": cmd["id"], "type": "result", "success": true, "result": result})
	}))
	defer server.Close()
	client := haclient.NewClient(server.URL)
	scope, err := BackupScopeDigest(observeBackupPreflight(context.Background(), client, "synthetic-token"))
	if err != nil {
		t.Fatal(err)
	}
	r := ha.MigrationExecutionRequest{Stage: "backup", OperationUID: "op", RequestID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SourceID: server.URL, Target: ha.MigrationTarget{UID: "target"}, ScopeDigest: scope}
	e := migrationprotocol.Evidence{Version: "v1", Stage: r.Stage, OperationUID: r.OperationUID, RequestID: r.RequestID, SourceID: r.SourceID, TargetUID: r.Target.UID, JobID: "exact-id"}
	if _, err := ReadBackupResolution(context.Background(), client, "synthetic-token", []byte("synthetic-key"), r, e); err != nil {
		t.Fatal(err)
	}
	if generate.Load() != 0 {
		t.Fatal("resolution issued a mutation")
	}
}

func (s *resolutionReadSource) GetBackupDetails(_ context.Context, _ string, id string) (*haclient.BackupDetails, error) {
	s.reads++
	if s.details == nil || s.details.Backup.ID != id {
		return nil, context.DeadlineExceeded
	}
	return s.details, nil
}

func TestReadBackupResolutionRequiresExactSourceAndScope(t *testing.T) {
	include := true
	password := "synthetic-secret"
	source := &resolutionReadSource{backupReader: backupReader{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password}}, agents: []haclient.BackupAgent{{ID: "backup.local"}}}}
	scope, err := BackupScopeDigest(observeBackupPreflight(context.Background(), source, "token"))
	if err != nil {
		t.Fatal(err)
	}
	r := ha.MigrationExecutionRequest{Stage: "backup", OperationUID: "operation", RequestID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SourceID: "https://source", Target: ha.MigrationTarget{UID: "target"}, ScopeDigest: scope}
	e := migrationprotocol.Evidence{Version: "v1", Stage: r.Stage, OperationUID: r.OperationUID, RequestID: r.RequestID, SourceID: r.SourceID, TargetUID: r.Target.UID, JobID: "job-id", BackupID: "job-id"}
	source.details = &haclient.BackupDetails{AgentErrors: map[string]any{}, Backup: haclient.BackupArchive{ID: "job-id", Date: "2026-11-16T12:00:00Z", HomeAssistantVersion: "2026.11.0", Automatic: true, DatabaseIncluded: &include, HomeAssistantIncluded: &include, Folders: []string{}, Addons: []json.RawMessage{}, FailedFolders: []string{}, FailedAddons: []json.RawMessage{}, FailedAgentIDs: []string{}, Agents: map[string]haclient.BackupAgentCopy{"backup.local": {Protected: true, Size: 123}}}}
	result, err := ReadBackupResolution(context.Background(), source, "token", []byte(password), r, e)
	if err != nil || result.BackupID != "job-id" || result.AgentID != "backup.local" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	onlyBackupID := e
	onlyBackupID.JobID = ""
	if _, err := ReadBackupResolution(context.Background(), source, "token", []byte(password), r, onlyBackupID); err != nil {
		t.Fatal("qualified backup ID rejected", err)
	}
	wrong := e
	wrong.JobID = "foreign"
	if _, err := ReadBackupResolution(context.Background(), source, "token", []byte(password), r, wrong); err == nil {
		t.Fatal("foreign job accepted")
	}
	wrong = e
	wrong.TargetUID = "foreign"
	if _, err := ReadBackupResolution(context.Background(), source, "token", []byte(password), r, wrong); err == nil {
		t.Fatal("foreign target accepted")
	}
	if _, err := ReadBackupResolution(context.Background(), source, "token", []byte("wrong"), r, e); err == nil {
		t.Fatal("foreign key accepted")
	}
	source.details = nil
	if _, err := ReadBackupResolution(context.Background(), source, "token", []byte(password), r, e); err == nil {
		t.Fatal("missing result accepted")
	}
}
