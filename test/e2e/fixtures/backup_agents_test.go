package fixtures

import (
	"encoding/json"
	"os/exec"
	"testing"
)

func TestBackupAgentConfigMap(t *testing.T) {
	fixture, err := BackupAgentConfigMap("disposable-backup-test")
	if err != nil {
		t.Fatal(err)
	}
	metadata := fixture["metadata"].(map[string]string)
	if metadata["namespace"] != "disposable-backup-test" || metadata["name"] != "backup-agent-fixture" {
		t.Fatal("fixture must remain scoped to the test namespace")
	}
	data := fixture["data"].(map[string]string)
	if len(data) != 3 || data["backup.py"] == "" || data["__init__.py"] == "" {
		t.Fatal("fixture component files are missing")
	}
	var manifest struct {
		Domain       string   `json:"domain"`
		Requirements []string `json:"requirements"`
	}
	if err := json.Unmarshal([]byte(data["manifest.json"]), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Domain != "backup_test" || len(manifest.Requirements) != 0 {
		t.Fatal("fixture must use its own test domain without external dependencies")
	}
}

func TestNativeAgentFixtureStorage(t *testing.T) {
	output, err := exec.Command("python3", "-B", "test_agents.py").CombinedOutput()
	if err != nil {
		t.Fatalf("fixture storage tests failed: %s", output)
	}
}
