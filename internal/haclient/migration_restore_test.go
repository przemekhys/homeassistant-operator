package haclient

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMigrationOnboardingOperationsDoNotExposeRecoveryKey(t *testing.T) {
	const key = "migration-recovery-key"
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/onboarding/backup/upload":
				if r.URL.Query().Get("agent_id") != "backup.local" {
					t.Errorf("upload agent_id = %q", r.URL.Query().Get("agent_id"))
				}
				if err := r.ParseMultipartForm(1024); err != nil {
					t.Errorf("invalid streaming multipart body: %v", err)
				}
				w.WriteHeader(http.StatusOK)
			case "/api/onboarding/backup/info":
				_, _ = w.Write([]byte(`{"backups":[{"backup_id":"id","database_included":true,"homeassistant_included":true,"homeassistant_version":"2026.11.0","folders":[],"agents":{"backup.local":{"protected":true,"size":7}}}],"state":"idle","last_action_event":null}`))
			case "/api/onboarding/backup/restore":
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), key) {
					t.Error("recovery key absent from request body")
				}
				if !strings.Contains(string(body), `"restore_database":true`) {
					t.Error("approved database scope was not sent")
				}
				w.WriteHeader(http.StatusBadRequest)
			default:
				http.NotFound(w, r)
			}
		}),
	)
	defer server.Close()
	client := NewClient(server.URL)
	if err := client.UploadMigrationBackup(context.Background(), bytes.NewBufferString("archive"), "archive.tar", 7, "backup.local"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.MigrationOnboardingBackupInfo(context.Background(), MigrationBackupExpectation{BackupID: "id", AgentID: "backup.local", Size: 7, RestoreDatabase: true}); err != nil {
		t.Fatal(err)
	}
	err := client.RestoreMigrationOnboardingBackup(
		context.Background(),
		"id",
		"backup.local",
		key,
		true,
	)
	if err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("unsafe restore error: %v", err)
	}
}

func TestMigrationRestoreHonorsExplicitDatabaseScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"restore_database":false`) {
			t.Error("restore database was inferred instead of bound")
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	if err := NewClient(server.URL).RestoreMigrationOnboardingBackup(context.Background(), "id", "agent", "key", false); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestMigrationOnboardingRejectsFailuresWithoutResponseLeaks(t *testing.T) {
	const recoveryKey = "recovery-key-must-not-leak"
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInsufficientStorage)
			_, _ = w.Write([]byte("invalid archive " + recoveryKey))
		}),
	)
	defer server.Close()
	client := NewClient(server.URL)
	for _, call := range []func() error{
		func() error {
			return client.UploadMigrationBackup(context.Background(), bytes.NewBufferString("bad"), "bad.tar", 3, "backup.local")
		},
		func() error {
			_, err := client.MigrationOnboardingBackupInfo(context.Background(), MigrationBackupExpectation{BackupID: "backup", AgentID: "backup.local", Size: 1})
			return err
		},
		func() error {
			return client.RestoreMigrationOnboardingBackup(context.Background(), "backup", "backup.local", recoveryKey, false)
		},
	} {
		err := call()
		if err == nil || strings.Contains(err.Error(), recoveryKey) ||
			!strings.Contains(err.Error(), "507") {
			t.Fatalf("unsafe or missing HTTP failure classification: %v", err)
		}
	}
}

func TestMigrationOnboardingDestructiveTransportFailureIsUnknown(t *testing.T) {
	client := NewClient("http://127.0.0.1:1")
	for _, call := range []func() error{
		func() error {
			return client.UploadMigrationBackup(context.Background(), bytes.NewBufferString("archive"), "archive.tar", 7, "backup.local")
		},
		func() error {
			return client.RestoreMigrationOnboardingBackup(context.Background(), "backup", "backup.local", "key", false)
		},
	} {
		err := call()
		haError, ok := err.(*Error)
		if !ok || haError.Type != ErrorTypeOutcomeUnknown {
			t.Fatalf("error = %#v, want OutcomeUnknown", err)
		}
	}
	_, err := client.MigrationOnboardingBackupInfo(context.Background(), MigrationBackupExpectation{BackupID: "backup", AgentID: "backup.local", Size: 1})
	haError, ok := err.(*Error)
	if !ok || haError.Type != ErrorTypeNotReady {
		t.Fatalf("info error = %#v, want NotReady", err)
	}
}

func TestMigrationOnboardingRejectsUnsafeTargetAndIncompleteInputs(t *testing.T) {
	client := NewClient("http://example.test/?password=not-allowed")
	if _, err := client.MigrationOnboardingBackupInfo(context.Background(), MigrationBackupExpectation{BackupID: "backup", AgentID: "backup.local", Size: 1}); err == nil ||
		strings.Contains(err.Error(), "not-allowed") {
		t.Fatalf("unsafe target error: %v", err)
	}
	if err := NewClient("http://example.test").UploadMigrationBackup(context.Background(), nil, "", -1, ""); err == nil {
		t.Fatal("incomplete upload input was accepted")
	}
	if err := NewClient("http://example.test").RestoreMigrationOnboardingBackup(context.Background(), "", "", "", false); err == nil {
		t.Fatal("incomplete restore input was accepted")
	}
}

func TestMigrationOnboardingInfoRejectsAmbiguousOrUnboundEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"backups":[{"backup_id":"id","database_included":true,"homeassistant_included":true,"homeassistant_version":"2026.11.0","folders":[],"agents":{"agent":{"protected":true,"size":7}}},{"backup_id":"id","database_included":true,"homeassistant_included":true,"homeassistant_version":"2026.11.0","folders":[],"agents":{"agent":{"protected":true,"size":7}}}],"state":"idle","last_action_event":null}`))
	}))
	defer server.Close()
	if _, err := NewClient(server.URL).MigrationOnboardingBackupInfo(context.Background(), MigrationBackupExpectation{BackupID: "id", AgentID: "agent", Size: 7, RestoreDatabase: true}); err == nil {
		t.Fatal("ambiguous onboarding evidence was accepted")
	}
}
