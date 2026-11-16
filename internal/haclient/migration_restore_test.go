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
				w.WriteHeader(http.StatusOK)
			case "/api/onboarding/backup/restore":
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), key) {
					t.Error("recovery key absent from request body")
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
	if err := client.MigrationOnboardingBackupInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := client.RestoreMigrationOnboardingBackup(
		context.Background(),
		"id",
		"backup.local",
		key,
	)
	if err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("unsafe restore error: %v", err)
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
		func() error { return client.MigrationOnboardingBackupInfo(context.Background()) },
		func() error {
			return client.RestoreMigrationOnboardingBackup(context.Background(), "backup", "backup.local", recoveryKey)
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
			return client.RestoreMigrationOnboardingBackup(context.Background(), "backup", "backup.local", "key")
		},
	} {
		err := call()
		haError, ok := err.(*Error)
		if !ok || haError.Type != ErrorTypeOutcomeUnknown {
			t.Fatalf("error = %#v, want OutcomeUnknown", err)
		}
	}
	err := client.MigrationOnboardingBackupInfo(context.Background())
	haError, ok := err.(*Error)
	if !ok || haError.Type != ErrorTypeNotReady {
		t.Fatalf("info error = %#v, want NotReady", err)
	}
}

func TestMigrationOnboardingRejectsUnsafeTargetAndIncompleteInputs(t *testing.T) {
	client := NewClient("http://example.test/?password=not-allowed")
	if err := client.MigrationOnboardingBackupInfo(context.Background()); err == nil ||
		strings.Contains(err.Error(), "not-allowed") {
		t.Fatalf("unsafe target error: %v", err)
	}
	if err := NewClient("http://example.test").UploadMigrationBackup(context.Background(), nil, "", -1, ""); err == nil {
		t.Fatal("incomplete upload input was accepted")
	}
	if err := NewClient("http://example.test").RestoreMigrationOnboardingBackup(context.Background(), "", "", ""); err == nil {
		t.Fatal("incomplete restore input was accepted")
	}
}
