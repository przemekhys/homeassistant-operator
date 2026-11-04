package utils

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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

func TestRecoveryWorkspaceDownloadsOnlyConfirmedProtectedArchive(t *testing.T) {
	workspace, err := NewRecoveryWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Cleanup()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("authorization header not supplied")
		}
		_, _ = io.WriteString(w, "archive")
	}))
	defer server.Close()
	if _, err := DownloadProtectedBackup(context.Background(), server.Client(), server.URL, "Bearer synthetic-token", workspace.Archive, false); err == nil {
		t.Fatal("unconfirmed archive download was accepted")
	}
	bytes, err := DownloadProtectedBackup(context.Background(), server.Client(), server.URL, "Bearer synthetic-token", workspace.Archive, true)
	if err != nil || bytes != int64(len("archive")) {
		t.Fatalf("protected download = %d, %v", bytes, err)
	}
	info, err := os.Stat(workspace.Archive)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive permissions = %v, %v", info.Mode(), err)
	}
	if _, err := DownloadProtectedBackup(context.Background(), server.Client(), server.URL+"?password=unsafe", "Bearer synthetic-token", filepath.Join(workspace.Dir, "second"), true); err == nil {
		t.Fatal("password URL was accepted")
	}
}
