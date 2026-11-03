package utils

import (
	"net/url"
	"strings"
	"testing"
)

func TestNativeBackupDownloadURLNeverIncludesPassword(t *testing.T) {
	endpoint, err := NativeBackupDownloadURL("http://127.0.0.1:18123", "backup/id", "backup.local")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("agent_id") != "backup.local" || parsed.Query().Get("password") != "" {
		t.Fatalf("unsafe native download URL: %s", endpoint)
	}
	if strings.Contains(endpoint, "password") {
		t.Fatalf("password parameter present in %q", endpoint)
	}
}

func TestValidateRecoveryTargetRequiresIsolation(t *testing.T) {
	if err := ValidateRecoveryTarget("restore", "0.0.0.0:18124", true); err == nil {
		t.Fatal("public recovery listener was accepted")
	}
	if err := ValidateRecoveryTarget("restore", "127.0.0.1:18124", false); err == nil {
		t.Fatal("external recovery network was accepted")
	}
	if err := ValidateRecoveryTarget("restore", "127.0.0.1:18124", true); err != nil {
		t.Fatalf("isolated recovery target rejected: %v", err)
	}
}

func TestRecoveryContainerArgsUsesOnlyFreshLoopbackTarget(t *testing.T) {
	args, err := RecoveryContainerArgs("restore", "restore-net", "/tmp/opencode/restore-config", "ghcr.io/home-assistant/home-assistant:2026.10.4")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{"--network restore-net", "127.0.0.1:18124:8123", "dst=/config"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("recovery container args missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "--privileged") || strings.Contains(joined, "/dev/") {
		t.Fatalf("unsafe recovery container args: %s", joined)
	}
}
