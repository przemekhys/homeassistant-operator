package migration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

type backupObservationFixture struct {
	backupReader
	details       *haclient.BackupDetails
	keyOK         bool
	archive       string
	afterDownload func()
}

func (f *backupObservationFixture) GetConfig(context.Context, string) (*haclient.ConfigResponse, error) {
	return &haclient.ConfigResponse{Version: "2026.11.0"}, nil
}

func (f *backupObservationFixture) GetBackupDetails(context.Context, string, string) (*haclient.BackupDetails, error) {
	return f.details, nil
}
func (f *backupObservationFixture) CanDecryptBackup(context.Context, string, string, string, string) error {
	if !f.keyOK {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func (f *backupObservationFixture) DownloadBackup(context.Context, string, string, string) (*http.Response, error) {
	if f.afterDownload != nil {
		f.afterDownload()
	}
	return &http.Response{StatusCode: 200, ContentLength: int64(len(f.archive)), Body: io.NopCloser(strings.NewReader(f.archive))}, nil
}

func TestObserveBackupRejectsWrongResultAndKey(t *testing.T) {
	include := true
	password := "private-recovery"
	data := "encrypted-archive"
	f := &backupObservationFixture{backupReader: backupReader{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password}}, agents: []haclient.BackupAgent{{ID: "backup.local"}}}, details: &haclient.BackupDetails{AgentErrors: map[string]any{}, Backup: haclient.BackupArchive{ID: "job-id", Automatic: true, DatabaseIncluded: &include, HomeAssistantIncluded: &include, Folders: []string{}, Addons: []json.RawMessage{}, FailedFolders: []string{}, FailedAddons: []json.RawMessage{}, FailedAgentIDs: []string{}, Agents: map[string]haclient.BackupAgentCopy{"backup.local": {Protected: true, Size: int64(len(data))}}}}, keyOK: true, archive: data}
	scope, err := BackupScopeDigest(observeBackupPreflight(context.Background(), f, "token"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	result, err := ObserveAndStoreBackup(context.Background(), f, "token", []byte(password), "job-id", "backup.local", scope, dir, "archive.tar")
	if err != nil || result.Size != int64(len(data)) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	f.details.Backup.ID = "foreign"
	if _, err := ObserveAndStoreBackup(context.Background(), f, "token", []byte(password), "job-id", "backup.local", scope, t.TempDir(), "archive.tar"); err == nil {
		t.Fatal("foreign backup accepted")
	}
	f.details.Backup.ID = "job-id"
	excluded := false
	f.details.Backup.DatabaseIncluded = &excluded
	if _, err := ObserveAndStoreBackup(context.Background(), f, "token", []byte(password), "job-id", "backup.local", scope, t.TempDir(), "archive.tar"); err == nil {
		t.Fatal("backup with mismatched database scope accepted")
	}
	f.details.Backup.DatabaseIncluded = &include
	f.keyOK = false
	if _, err := ObserveAndStoreBackup(context.Background(), f, "token", []byte(password), "job-id", "backup.local", scope, t.TempDir(), "archive.tar"); err == nil || strings.Contains(err.Error(), password) {
		t.Fatal("invalid key accepted or leaked")
	}
	f.keyOK = true
	copy := f.details.Backup.Agents["backup.local"]
	copy.Protected = false
	f.details.Backup.Agents["backup.local"] = copy
	if _, err := ObserveAndStoreBackup(context.Background(), f, "token", []byte(password), "job-id", "backup.local", scope, t.TempDir(), "archive.tar"); err == nil {
		t.Fatal("unprotected backup accepted")
	}
	copy.Protected = true
	f.details.Backup.Agents["backup.local"] = copy
	f.afterDownload = func() { f.config.CreateBackup.IncludeDatabase = &excluded }
	if _, err := ObserveAndStoreBackup(context.Background(), f, "token", []byte(password), "job-id", "backup.local", scope, t.TempDir(), "archive.tar"); err == nil {
		t.Fatal("settings change during transfer accepted")
	}
}
