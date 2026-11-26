package haclient

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

// TestMigrationDefinitionOnDisposableHA runs only against an explicitly
// supplied freshly created disposable instance. It bootstraps and writes test
// objects; never point it at an existing or production instance.
func TestMigrationDefinitionOnDisposableHA(t *testing.T) {
	endpoint := os.Getenv("HA_MIGRATION_DISPOSABLE_URL")
	if endpoint == "" {
		t.Skip("requires explicitly provisioned disposable HA")
	}
	client := NewClient(endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for client.CheckAPIReady(ctx) != nil {
		select {
		case <-ctx.Done():
			t.Fatal("disposable HA did not become ready")
		case <-time.After(2 * time.Second):
		}
	}
	token, err := client.PerformBootstrap(
		ctx,
		"migration-test",
		"migration-test-password",
		"Migration Test",
		"en",
		&BootstrapOptions{CreateLongLivedToken: true},
	)
	if err != nil || token == "" {
		t.Fatalf("bootstrap disposable HA: %v", err)
	}
	objects := []struct {
		kind, id string
		data     map[string]interface{}
		put      func(context.Context, string, string, map[string]interface{}) error
	}{
		{
			"automation",
			"migration_test_automation",
			map[string]interface{}{
				"id":    "migration_test_automation",
				"alias": "Migration test",
				"trigger": []interface{}{
					map[string]interface{}{
						"platform":   "event",
						"event_type": "migration_test",
					},
				},
				"action": []interface{}{
					map[string]interface{}{"event": "migration_test_result"},
				},
			},
			client.PutAutomation,
		},
		{
			"scene",
			"migration_test_scene",
			map[string]interface{}{
				"id":   "migration_test_scene",
				"name": "Migration test",
				"entities": map[string]interface{}{
					"light.migration_test": map[string]interface{}{"state": "on"},
				},
			},
			client.PutScene,
		},
		{
			"script",
			"migration_test_script",
			map[string]interface{}{
				"alias": "Migration test",
				"sequence": []interface{}{
					map[string]interface{}{"event": "migration_test_result"},
				},
			},
			client.PutScript,
		},
	}
	for _, object := range objects {
		t.Run(object.kind, func(t *testing.T) {
			if err := object.put(ctx, token, object.id, object.data); err != nil {
				t.Fatalf("create fixture: %v", err)
			}
			actual, err := client.ReadMigrationDefinition(ctx, token, object.kind, object.id)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var observed map[string]interface{}
			if err := json.Unmarshal(actual, &observed); err != nil {
				t.Fatal("invalid JSON returned")
			}
			for key, value := range object.data {
				observedKey := key
				if object.kind == "automation" && (key == "trigger" || key == "action") {
					observedKey += "s"
				}
				if !reflect.DeepEqual(value, observed[observedKey]) {
					t.Errorf(
						"fixture field %q was not represented by response field %q",
						key,
						observedKey,
					)
				}
			}
		})
	}
}
