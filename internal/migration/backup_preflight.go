package migration

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// BackupScopeDigest identifies a complete, protected observed backup scope.
// It excludes key material and presentation-only fields. The observation must
// be repeated immediately before dispatch; a saved digest alone is not proof
// of current source configuration or recoverability of its key.
func BackupScopeDigest(preflight *BackupPreflight) (string, error) {
	if preflight == nil || preflight.Status != "Ready" || !preflight.Protected ||
		preflight.IncludeDatabase == nil || len(preflight.AgentIDs) == 0 {
		return "", fmt.Errorf("backup scope is not verified")
	}
	agents := append([]string(nil), preflight.AgentIDs...)
	sort.Strings(agents)
	for i, id := range agents {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id || (i > 0 && id == agents[i-1]) {
			return "", fmt.Errorf("backup destinations are invalid")
		}
	}
	encoded, err := json.Marshal(struct {
		Version         string   `json:"version"`
		IncludeDatabase bool     `json:"includeDatabase"`
		AgentIDs        []string `json:"agentIDs"`
		Protected       bool     `json:"protected"`
	}{"v1", *preflight.IncludeDatabase, agents, true})
	if err != nil {
		return "", fmt.Errorf("encode backup scope")
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

type backupPreflightReader interface {
	GetBackupConfig(context.Context, string) (*haclient.BackupConfig, error)
	ListBackupAgents(context.Context, string) ([]haclient.BackupAgent, error)
}

type recoveryKeyCheckingReader struct {
	backupPreflightReader
	key     []byte
	matched bool
}

func (r *recoveryKeyCheckingReader) GetBackupConfig(ctx context.Context, token string) (*haclient.BackupConfig, error) {
	config, err := r.backupPreflightReader.GetBackupConfig(ctx, token)
	if err == nil && config != nil && config.CreateBackup.Password != nil {
		r.matched = subtle.ConstantTimeCompare([]byte(*config.CreateBackup.Password), r.key) == 1
	}
	return config, err
}

// VerifyBackupScopeAndKey compares the separately supplied recovery key with
// the currently observed native backup settings as well as checking the scope.
// Matching settings are not proof that an already-created archive can restore.
func VerifyBackupScopeAndKey(ctx context.Context, reader backupPreflightReader, token, approvedDigest string, key []byte) error {
	if reader == nil || len(key) == 0 {
		return fmt.Errorf("backup recovery key is unavailable")
	}
	checking := &recoveryKeyCheckingReader{backupPreflightReader: reader, key: key}
	if err := VerifyBackupScope(ctx, checking, token, approvedDigest); err != nil {
		return err
	}
	if !checking.matched {
		return fmt.Errorf("backup recovery key does not match current source settings")
	}
	return nil
}

// VerifyBackupScope re-observes HA and compares the current protected scope
// with the approved digest. It does not send a backup request and does not
// establish that the operator has the recovery key.
func VerifyBackupScope(ctx context.Context, reader backupPreflightReader, token, approvedDigest string) error {
	if reader == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(approvedDigest) == "" {
		return fmt.Errorf("backup scope verification requires source access and an approved digest")
	}
	current := observeBackupPreflight(ctx, reader, token)
	digest, err := BackupScopeDigest(current)
	if err != nil {
		return fmt.Errorf("current backup scope is unavailable or unprotected")
	}
	if digest != approvedDigest {
		return fmt.Errorf("current backup scope differs from the approved scope")
	}
	return nil
}

// observeBackupPreflight is read-only. It deliberately projects no password,
// remote error text, or raw backup configuration into the public report.
func observeBackupPreflight(ctx context.Context, reader backupPreflightReader, token string) *BackupPreflight {
	result := &BackupPreflight{Status: "Blocked", Limitations: []string{
		"external databases and additional volumes require separate migration",
		"archive recovery key must be retained independently",
	}}
	config, err := reader.GetBackupConfig(ctx, token)
	if err != nil || config == nil {
		result.Reason = "backup configuration unavailable"
		return result
	}
	if config.CreateBackup.IncludeDatabase != nil {
		include := *config.CreateBackup.IncludeDatabase
		result.IncludeDatabase = &include
	}
	result.Protected = config.CreateBackup.Password != nil && *config.CreateBackup.Password != ""
	result.AgentIDs = append([]string(nil), config.CreateBackup.AgentIDs...)
	sort.Strings(result.AgentIDs)
	if result.IncludeDatabase == nil || len(result.AgentIDs) == 0 {
		result.Reason = "backup scope or destination is incomplete"
		return result
	}
	for i, id := range result.AgentIDs {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id || (i > 0 && id == result.AgentIDs[i-1]) {
			result.Reason = "backup destination is incomplete or ambiguous"
			return result
		}
	}
	if !result.Protected {
		result.Reason = "protected backup key unavailable"
		return result
	}
	agents, err := reader.ListBackupAgents(ctx, token)
	if err != nil {
		result.Reason = "backup destination observation unavailable"
		return result
	}
	available := make(map[string]int, len(agents))
	for _, agent := range agents {
		available[agent.ID]++
	}
	for _, id := range result.AgentIDs {
		if available[id] != 1 {
			result.Reason = "backup destination unavailable or ambiguous"
			return result
		}
	}
	result.Status = "Ready"
	return result
}
