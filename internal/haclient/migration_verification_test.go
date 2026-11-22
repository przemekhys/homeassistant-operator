package haclient

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
)

func TestMigrationVerificationProjectsOnlyNonSecretFields(t *testing.T) {
	server := newBackupWebSocketServer(t, func(command map[string]any, conn *websocket.Conn) {
		if command["type"] != "config/entity_registry/get" {
			t.Error("unexpected command")
		}
		_ = conn.WriteJSON(map[string]any{"id": 1, "type": "result", "success": true, "result": map[string]any{"entity_id": "sensor.test", "unique_id": "uid", "platform": "sample", "config_entry_id": nil, "secret": "must-not-appear"}})
	})
	defer server.Close()
	projection, err := NewClient(server.URL).ReadMigrationVerification(context.Background(), "token", "EntityRegistryIdentity", "sensor.test")
	if err != nil || strings.Contains(string(projection), "must-not-appear") {
		t.Fatalf("projection unavailable or leaked: %v", err)
	}
	var parsed migrationprotocol.EntityRegistryIdentity
	if err := json.Unmarshal(projection, &parsed); err != nil || parsed.EntityID != "sensor.test" || parsed.UniqueID != "uid" || parsed.ConfigEntryID != "" {
		t.Fatalf("invalid projection: %#v %v", parsed, err)
	}
	if _, err := migrationprotocol.ProjectionDigest("EntityRegistryIdentity", "sensor.test", projection); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationVerificationRejectsMissingEntity(t *testing.T) {
	server := newBackupWebSocketServer(t, func(_ map[string]any, conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]any{"id": 1, "type": "result", "success": true, "result": map[string]any{"entity_id": "sensor.other", "unique_id": "uid", "platform": "sample"}})
	})
	defer server.Close()
	if _, err := NewClient(server.URL).ReadMigrationVerification(context.Background(), "token", "EntityRegistryIdentity", "sensor.test"); err == nil {
		t.Fatal("accepted wrong entity")
	}
}
