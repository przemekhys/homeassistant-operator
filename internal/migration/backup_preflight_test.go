package migration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

type backupReader struct {
	scanReader
	config          *haclient.BackupConfig
	agents          []haclient.BackupAgent
	err             error
	noAgentDefaults bool
}

type backupChangingReader struct {
	*backupReader
	changed string
}

func (r *backupChangingReader) ListBackupAgents(ctx context.Context, token string) ([]haclient.BackupAgent, error) {
	agents, err := r.backupReader.ListBackupAgents(ctx, token)
	r.config.CreateBackup.Password = &r.changed
	return agents, err
}

func TestVerifyBackupScopeAndKeyRejectsKeyChangedDuringAgentRead(t *testing.T) {
	include := true
	password := "original-key"
	reader := &backupReader{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{
		IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password,
	}}, agents: []haclient.BackupAgent{{ID: "backup.local"}}}
	digest, err := BackupScopeDigest(observeBackupPreflight(context.Background(), reader, "token"))
	if err != nil {
		t.Fatal(err)
	}
	changing := &backupChangingReader{backupReader: reader, changed: "replacement-key"}
	if err := VerifyBackupScopeAndKey(context.Background(), changing, "token", digest, []byte(password)); err == nil {
		t.Fatal("accepted a key changed during agent discovery")
	}
}

func (r *backupReader) GetBackupConfig(context.Context, string) (*haclient.BackupConfig, error) {
	if r.config != nil {
		if !r.noAgentDefaults && r.config.Agents == nil {
			r.config.Agents = make(map[string]haclient.BackupAgentConfig)
			for _, id := range r.config.CreateBackup.AgentIDs {
				value := true
				r.config.Agents[id] = haclient.BackupAgentConfig{Protected: &value}
			}
		}
		if r.config.CreateBackup.IncludeAllAddons == nil {
			value := false
			r.config.CreateBackup.IncludeAllAddons = &value
		}
		if r.config.CreateBackup.IncludeAddons == nil {
			r.config.CreateBackup.IncludeAddons = json.RawMessage(`null`)
		}
		if r.config.CreateBackup.IncludeFolders == nil {
			r.config.CreateBackup.IncludeFolders = json.RawMessage(`null`)
		}
	}
	return r.config, r.err
}
func (r *backupReader) ListBackupAgents(context.Context, string) ([]haclient.BackupAgent, error) {
	return r.agents, r.err
}

func TestScanBackupPreflightIsRedactedAndScopeBound(t *testing.T) {
	include := true
	password := "recovery-key-must-not-escape"
	reader := &backupReader{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password}}, agents: []haclient.BackupAgent{{ID: "backup.local"}}}
	request := ScanRequest{SourceID: "source", Token: "token-must-not-escape", Target: Target{Namespace: "home", Name: "target", UID: "uid"}}
	report, err := Scan(context.Background(), reader, request)
	if err != nil || report.Backup == nil || report.Backup.Status != "Ready" || !report.Backup.Protected {
		t.Fatalf("backup preflight failed: %v", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), password) || strings.Contains(string(encoded), request.Token) {
		t.Fatal("report contains credentials")
	}
	include = false
	if report.Backup.IncludeDatabase == nil || !*report.Backup.IncludeDatabase {
		t.Fatal("report retained a mutable source pointer")
	}
	changed, err := Scan(context.Background(), reader, request)
	if err != nil || changed.ID == report.ID {
		t.Fatalf("backup scope change did not change report ID: %v", err)
	}
}

func TestBackupPreflightFailsClosedWithoutSensitiveErrors(t *testing.T) {
	include := true
	password := "private-password"
	for _, reader := range []*backupReader{
		{err: errors.New("private-password")},
		{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{IncludeDatabase: &include, AgentIDs: []string{"backup.local"}}}},
		{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password}}},
	} {
		result := observeBackupPreflight(context.Background(), reader, "token")
		encoded, err := json.Marshal(result)
		if err != nil || result.Status != "Blocked" || strings.Contains(string(encoded), password) {
			t.Fatalf("unsafe preflight: %s %v", encoded, err)
		}
	}
}

func TestBackupScopeDigestBindsScopeAndRejectsIncompleteObservation(t *testing.T) {
	include := true
	scope := &BackupPreflight{Status: "Ready", Protected: true, IncludeDatabase: &include,
		AgentIDs: []string{"backup.remote", "backup.local"}, IncludeAllAddons: &include,
		IncludeAddons: json.RawMessage(`null`), IncludeFolders: json.RawMessage(`null`)}
	digest, err := BackupScopeDigest(scope)
	if err != nil || digest == "" {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	scope.AgentIDs[0], scope.AgentIDs[1] = scope.AgentIDs[1], scope.AgentIDs[0]
	reordered, err := BackupScopeDigest(scope)
	if err != nil || reordered != digest {
		t.Fatal("agent order changed scope identity")
	}
	include = false
	changed, err := BackupScopeDigest(scope)
	if err != nil || changed == digest {
		t.Fatal("database scope change was not detected")
	}
	scope.AgentIDs = []string{"backup.local", "backup.local"}
	if _, err := BackupScopeDigest(scope); err == nil {
		t.Fatal("duplicate agent accepted")
	}
	scope.AgentIDs = []string{"backup.local"}
	scope.Protected = false
	if _, err := BackupScopeDigest(scope); err == nil {
		t.Fatal("unprotected scope accepted")
	}
}

func TestBackupScopeDigestBindsContentSelections(t *testing.T) {
	include, all := true, false
	scope := &BackupPreflight{Status: "Ready", Protected: true, IncludeDatabase: &include,
		AgentIDs: []string{"backup.local"}, IncludeAllAddons: &all,
		IncludeAddons: json.RawMessage(`["app-b","app-a"]`), IncludeFolders: json.RawMessage(`["ssl","media"]`)}
	original, err := BackupScopeDigest(scope)
	if err != nil {
		t.Fatal(err)
	}
	scope.IncludeAddons = json.RawMessage(`["app-a","app-b"]`)
	scope.IncludeFolders = json.RawMessage(`["media","ssl"]`)
	reordered, err := BackupScopeDigest(scope)
	if err != nil || reordered != original {
		t.Fatalf("selection order changed digest: %v", err)
	}
	scope.IncludeFolders = json.RawMessage(`["ssl"]`)
	changed, err := BackupScopeDigest(scope)
	if err != nil || changed == original {
		t.Fatal("folder change was not bound")
	}
	scope.IncludeFolders = json.RawMessage(`null`)
	nullDigest, err := BackupScopeDigest(scope)
	if err != nil {
		t.Fatal(err)
	}
	scope.IncludeFolders = json.RawMessage(`[]`)
	emptyDigest, err := BackupScopeDigest(scope)
	if err != nil || emptyDigest == nullDigest {
		t.Fatal("null and empty selections were not distinguished")
	}
	scope.IncludeFolders = nil
	if _, err := BackupScopeDigest(scope); err == nil {
		t.Fatal("missing folder selection was accepted")
	}
	scope.IncludeFolders = json.RawMessage(`["ssl","ssl"]`)
	if _, err := BackupScopeDigest(scope); err == nil {
		t.Fatal("duplicate folders were accepted")
	}
}

func TestBackupPreflightReadsHomeAssistantContentSelection(t *testing.T) {
	var config haclient.BackupConfig
	if err := json.Unmarshal([]byte(`{"create_backup":{"include_database":true,"agent_ids":["backup.local"],"password":"key","include_addons":null,"include_all_addons":false,"include_folders":["media"]}}`), &config); err != nil {
		t.Fatal(err)
	}
	reader := &backupReader{config: &config, agents: []haclient.BackupAgent{{ID: "backup.local"}}}
	observed := observeBackupPreflight(context.Background(), reader, "token")
	if observed.Status != "Ready" || string(observed.IncludeAddons) != "null" || string(observed.IncludeFolders) != `["media"]` || observed.IncludeAllAddons == nil || *observed.IncludeAllAddons {
		t.Fatalf("unexpected backup scope: %#v", observed)
	}
}

func TestBackupPreflightRequiresEveryAgentProtected(t *testing.T) {
	include, all, protected, unprotected := true, false, true, false
	password := "private-key"
	reader := &backupReader{noAgentDefaults: true, config: &haclient.BackupConfig{
		CreateBackup: haclient.BackupCreateConfig{IncludeDatabase: &include, AgentIDs: []string{"backup.local", "backup.remote"}, Password: &password,
			IncludeAddons: json.RawMessage(`null`), IncludeAllAddons: &all, IncludeFolders: json.RawMessage(`null`)},
		Agents: map[string]haclient.BackupAgentConfig{"backup.local": {Protected: &protected}, "backup.remote": {Protected: &protected}},
	}, agents: []haclient.BackupAgent{{ID: "backup.local"}, {ID: "backup.remote"}}}
	if got := observeBackupPreflight(context.Background(), reader, "token"); got.Status != "Ready" {
		t.Fatalf("protected agents rejected: %s", got.Reason)
	}
	for _, agent := range []haclient.BackupAgentConfig{{Protected: &unprotected}, {Protected: nil}} {
		reader.config.Agents["backup.remote"] = agent
		if got := observeBackupPreflight(context.Background(), reader, "token"); got.Status != "Blocked" {
			t.Fatal("unprotected or unknown agent accepted")
		}
	}
	delete(reader.config.Agents, "backup.remote")
	if got := observeBackupPreflight(context.Background(), reader, "token"); got.Status != "Blocked" {
		t.Fatal("missing agent protection accepted")
	}
}

func TestBackupPreflightReadsAgentProtectionFromHAConfig(t *testing.T) {
	var config haclient.BackupConfig
	if err := json.Unmarshal([]byte(`{"agents":{"backup.local":{"protected":false}},"create_backup":{"include_database":true,"agent_ids":["backup.local"],"password":"key","include_addons":null,"include_all_addons":false,"include_folders":null}}`), &config); err != nil {
		t.Fatal(err)
	}
	reader := &backupReader{noAgentDefaults: true, config: &config, agents: []haclient.BackupAgent{{ID: "backup.local"}}}
	if got := observeBackupPreflight(context.Background(), reader, "token"); got.Status != "Blocked" || got.Protected {
		t.Fatal("HA's explicit unprotected setting was accepted")
	}
}

func TestVerifyBackupScopeReobservesAndRejectsChangedOrUnavailableSource(t *testing.T) {
	include := true
	password := "sensitive-key"
	reader := &backupReader{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{
		IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password,
	}}, agents: []haclient.BackupAgent{{ID: "backup.local"}}}
	approved := observeBackupPreflight(context.Background(), reader, "token")
	digest, err := BackupScopeDigest(approved)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackupScope(context.Background(), reader, "token", digest); err != nil {
		t.Fatal(err)
	}
	include = false
	if err := VerifyBackupScope(context.Background(), reader, "token", digest); err == nil {
		t.Fatal("changed scope was accepted")
	}
	reader.err = errors.New(password)
	if err := VerifyBackupScope(context.Background(), reader, "token", digest); err == nil ||
		strings.Contains(err.Error(), password) {
		t.Fatalf("unavailable source or leaked key: %v", err)
	}
}

func TestVerifyBackupScopeAndKeyRequiresMatchingKey(t *testing.T) {
	include := true
	password := "private-recovery-key"
	reader := &backupReader{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{
		IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password,
	}}, agents: []haclient.BackupAgent{{ID: "backup.local"}}}
	digest, err := BackupScopeDigest(observeBackupPreflight(context.Background(), reader, "token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackupScopeAndKey(context.Background(), reader, "token", digest, []byte(password)); err != nil {
		t.Fatal(err)
	}
	for _, key := range [][]byte{nil, []byte("wrong-private-key")} {
		err := VerifyBackupScopeAndKey(context.Background(), reader, "token", digest, key)
		if err == nil || strings.Contains(err.Error(), password) || strings.Contains(err.Error(), string(key)) && len(key) > 0 {
			t.Fatalf("invalid key was accepted or leaked: %v", err)
		}
	}
}
