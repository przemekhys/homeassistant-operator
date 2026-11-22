package controller

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// This test bootstraps and modifies HA. Set the URL only to a new disposable
// instance with no user data, never to an existing installation.
func TestMigrationRegistryOnDisposableHA(t *testing.T) {
	endpoint := os.Getenv("HA_MIGRATION_DISPOSABLE_URL")
	if endpoint == "" {
		t.Skip("requires explicitly provisioned disposable HA")
	}
	c := haclient.NewClient(endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for c.CheckAPIReady(ctx) != nil {
		select {
		case <-ctx.Done():
			t.Fatal("disposable HA did not become ready")
		case <-time.After(2 * time.Second):
		}
	}
	token, err := c.PerformBootstrap(
		ctx,
		"migration-test",
		"migration-test-password",
		"Migration Test",
		"en",
		&haclient.BootstrapOptions{CreateLongLivedToken: true},
	)
	if err != nil || token == "" {
		t.Fatalf("bootstrap disposable HA: %v", err)
	}
	create := func(kind string, data map[string]interface{}) string {
		t.Helper()
		raw, err := c.SendWebSocketCommand(ctx, token, "config/"+kind+"_registry/create", data)
		if err != nil {
			t.Fatalf("create %s fixture: %v", kind, err)
		}
		var value map[string]json.RawMessage
		var id string
		if json.Unmarshal(raw, &value) != nil ||
			json.Unmarshal(value[kind+"_id"], &id) != nil ||
			id == "" {
			t.Fatalf("invalid %s fixture ID", kind)
		}
		return id
	}
	floorID := create("floor", map[string]interface{}{"name": "Migration Test Floor"})
	labelID := create("label", map[string]interface{}{"name": "Migration Test Label"})
	areaID := create(
		"area",
		map[string]interface{}{
			"name":     "Migration Test Area",
			"floor_id": floorID,
			"labels":   []string{labelID},
		},
	)
	if !migrationAreaRegistryReferencesExist(ctx, c, token, floorID, []string{labelID}) {
		t.Fatal("freshly created floor and label IDs were not observed uniquely")
	}
	if migrationAreaRegistryReferencesExist(
		ctx,
		c,
		token,
		floorID,
		[]string{"missing-label-id"},
	) {
		t.Fatal("missing label ID was accepted")
	}
	floor := &hav1.HomeAssistantFloor{
		Spec: hav1.HomeAssistantFloorSpec{Name: "Migration Test Floor"},
	}
	label := &hav1.HomeAssistantLabel{
		Spec: hav1.HomeAssistantLabelSpec{Name: "Migration Test Label"},
	}
	area := &hav1.HomeAssistantArea{
		Spec: hav1.HomeAssistantAreaSpec{
			Name:      "Migration Test Area",
			FloorName: "floor-cr",
			Labels:    []string{"label-cr"},
		},
	}
	for _, tc := range []struct {
		kind, id string
		check    func(json.RawMessage) string
	}{
		{"floor", floorID, func(raw json.RawMessage) string { return compareMigrationRegistry(floor, "floor", floorID, raw) }},
		{"label", labelID, func(raw json.RawMessage) string { return compareMigrationRegistry(label, "label", labelID, raw) }},
		{"area", areaID, func(raw json.RawMessage) string {
			return compareMigrationRegistryArea(area, areaID, raw, floorID, []string{labelID})
		}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			raw, err := c.SendWebSocketCommand(
				ctx,
				token,
				"config/"+tc.kind+"_registry/list",
				nil,
			)
			if err != nil {
				t.Fatalf("read registry: %v", err)
			}
			if reason := tc.check(raw); reason != "Adopted" {
				t.Fatalf("comparison failed closed: %s", reason)
			}
		})
	}
}
