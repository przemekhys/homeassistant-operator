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

	"github.com/gorilla/websocket"
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

func TestRecoveredStorageContains(t *testing.T) {
	configDir := t.TempDir()
	storageDir := filepath.Join(configDir, ".storage")
	if err := os.Mkdir(storageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "local_todo.control.ics"), []byte("control-item"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := RecoveredStorageContains(configDir, "local_todo.*.ics", "control-item")
	if err != nil || !found {
		t.Fatalf("RecoveredStorageContains() = %t, %v", found, err)
	}
	found, err = RecoveredStorageContains(configDir, "local_todo.*.ics", "absent")
	if err != nil || found {
		t.Fatalf("RecoveredStorageContains() absent = %t, %v", found, err)
	}
}

func TestRestoreNativeBackupDoesNotExposeRecoveryKey(t *testing.T) {
	const key = "synthetic-recovery-key"
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]string
		_ = connection.ReadJSON(&auth)
		_ = connection.WriteJSON(map[string]string{"type": "auth_ok"})
		var command map[string]any
		_ = connection.ReadJSON(&command)
		if command["password"] != key {
			t.Error("native restore did not pass recovery key in frame")
		}
		_ = connection.WriteJSON(map[string]any{"id": 1, "type": "result", "success": true})
	}))
	defer server.Close()
	if err := RestoreNativeBackup(context.Background(), server.URL, "synthetic-token", "backup-1", "backup.local", key); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeBackupWebSocketURL(server.URL + "?password=" + key); err == nil {
		t.Fatal("recovery target URL accepted a password")
	}
}

func TestRestoreNativeBackupSanitizesRejectedKey(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.WriteJSON(map[string]string{"type": "auth_required"})
		var ignored map[string]any
		_ = connection.ReadJSON(&ignored)
		_ = connection.WriteJSON(map[string]string{"type": "auth_ok"})
		_ = connection.ReadJSON(&ignored)
		_ = connection.WriteJSON(map[string]any{"id": 1, "type": "result", "success": false,
			"error": map[string]string{"code": "password_incorrect", "message": "synthetic-recovery-key"}})
	}))
	defer server.Close()
	err := RestoreNativeBackup(context.Background(), server.URL, "synthetic-token", "backup-1", "backup.local", "synthetic-recovery-key")
	if err == nil || !strings.Contains(err.Error(), "password_incorrect") || strings.Contains(err.Error(), "synthetic-recovery-key") {
		t.Fatalf("unsafe rejected-key error: %v", err)
	}
}

func TestOnboardingUploadAndInfoUseFreshTargetEndpoints(t *testing.T) {
	workspace, err := NewRecoveryWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Cleanup()
	if err := os.WriteFile(workspace.Archive, []byte("synthetic archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/onboarding/backup/upload":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			payload, _ := io.ReadAll(file)
			if string(payload) != "synthetic archive" {
				t.Error("unexpected archive payload")
			}
			w.WriteHeader(http.StatusOK)
		case "/api/onboarding/backup/info":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if err := UploadBackupForOnboarding(context.Background(), server.Client(), server.URL, workspace.Archive); err != nil {
		t.Fatal(err)
	}
	if err := OnboardingBackupInfo(context.Background(), server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
}

func TestOnboardingUploadDoesNotExposeServerResponse(t *testing.T) {
	workspace, err := NewRecoveryWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Cleanup()
	if err := os.WriteFile(workspace.Archive, []byte("synthetic archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	const sensitiveResponse = "synthetic-recovery-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "Can't upload backup file: "+sensitiveResponse)
	}))
	defer server.Close()
	err = UploadBackupForOnboarding(context.Background(), server.Client(), server.URL, workspace.Archive)
	if err == nil || !strings.Contains(err.Error(), "onboarding upload rejected: HTTP 500") || strings.Contains(err.Error(), sensitiveResponse) {
		t.Fatalf("unsafe onboarding upload error: %v", err)
	}
}

func TestOnboardingRestoreSanitizesWrongRecoveryKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/onboarding/backup/restore" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":"incorrect_password","message":"synthetic-recovery-key"}`)
	}))
	defer server.Close()
	err := RestoreOnboardingBackup(context.Background(), server.Client(), server.URL, "backup-1", "backup.local", "synthetic-recovery-key")
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") || strings.Contains(err.Error(), "synthetic-recovery-key") {
		t.Fatalf("unsafe onboarding restore error: %v", err)
	}
}

func TestSetNativeBackupPasswordKeepsKeyOutOfErrors(t *testing.T) {
	const key = "synthetic-recovery-key"
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.WriteJSON(map[string]string{"type": "auth_required"})
		var ignored map[string]any
		_ = connection.ReadJSON(&ignored)
		_ = connection.WriteJSON(map[string]string{"type": "auth_ok"})
		_ = connection.ReadJSON(&ignored)
		_ = connection.WriteJSON(map[string]any{"id": 1, "type": "result", "success": false,
			"error": map[string]string{"code": "invalid_key", "message": key}})
	}))
	defer server.Close()
	err := SetNativeBackupPassword(context.Background(), server.URL, "synthetic-token", key)
	if err == nil || !strings.Contains(err.Error(), "invalid_key") || strings.Contains(err.Error(), key) {
		t.Fatalf("unsafe backup key error: %v", err)
	}
}
