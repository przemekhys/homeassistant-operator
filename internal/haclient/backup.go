package haclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// BackupErrorKind is deliberately a small safe classification. Raw HA error
// messages can include user-provided data and must not be propagated to CR
// status or logs.
type BackupErrorKind string

const (
	BackupErrorAuthenticationFailed  BackupErrorKind = "AuthenticationFailed"
	BackupErrorUnsupportedCapability BackupErrorKind = "UnsupportedCapability"
	BackupErrorBusyBeforeAcceptance  BackupErrorKind = "BusyBeforeAcceptance"
	BackupErrorSourceUnavailable     BackupErrorKind = "SourceUnavailable"
	BackupErrorArchiveNotFound       BackupErrorKind = "ArchiveNotFound"
	BackupErrorPasswordIncorrect     BackupErrorKind = "PasswordIncorrect"
	BackupErrorDispatchUnknown       BackupErrorKind = "DispatchOutcomeUnknown"
)

type BackupError struct {
	Kind BackupErrorKind
	Err  error
}

func (e *BackupError) Error() string { return string(e.Kind) }
func (e *BackupError) Unwrap() error { return e.Err }

type BackupAgent struct {
	ID   string `json:"agent_id"`
	Name string `json:"name"`
}
type BackupAgentsResult struct {
	Agents []BackupAgent `json:"agents"`
}
type GenerateBackupRequest struct {
	AgentIDs        []string `json:"agent_ids"`
	IncludeDatabase bool     `json:"include_database"`
	Name            string   `json:"name,omitempty"`
	Password        string   `json:"password"`
}
type GenerateBackupResult struct {
	BackupJobID string `json:"backup_job_id"`
}
type RestoreBackupRequest struct {
	BackupID             string `json:"backup_id"`
	AgentID              string `json:"agent_id"`
	Password             string `json:"password"`
	RestoreHomeAssistant bool   `json:"restore_homeassistant"`
	RestoreDatabase      *bool  `json:"restore_database,omitempty"`
}

func (c *Client) ListBackupAgents(ctx context.Context, token string) ([]BackupAgent, error) {
	raw, code, err := c.sendWebSocketCommandWithCode(ctx, token, "backup/agents/info", nil)
	if err != nil {
		return nil, classifyBackupError(code, err)
	}
	var result BackupAgentsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, &BackupError{Kind: BackupErrorUnsupportedCapability, Err: err}
	}
	return result.Agents, nil
}

func (c *Client) GenerateBackup(ctx context.Context, token string, req GenerateBackupRequest) (*GenerateBackupResult, error) {
	if req.Password == "" {
		return nil, &BackupError{Kind: BackupErrorPasswordIncorrect}
	}
	raw, code, err := c.sendWebSocketCommandWithCode(ctx, token, "backup/generate", map[string]interface{}{
		"agent_ids": req.AgentIDs, "include_database": req.IncludeDatabase, "name": req.Name, "password": req.Password,
	})
	if err != nil {
		return nil, classifyBackupError(code, err)
	}
	var result GenerateBackupResult
	if err := json.Unmarshal(raw, &result); err != nil || result.BackupJobID == "" {
		return nil, &BackupError{Kind: BackupErrorDispatchUnknown, Err: err}
	}
	return &result, nil
}

// RestoreBackup sends exactly one native command. A caller that loses its
// response must preserve its durable dispatch ticket and classify the outcome
// as unknown; this method never retries.
func (c *Client) RestoreBackup(ctx context.Context, token string, req RestoreBackupRequest) error {
	if req.BackupID == "" || req.AgentID == "" || req.Password == "" || !req.RestoreHomeAssistant {
		return &BackupError{Kind: BackupErrorUnsupportedCapability, Err: fmt.Errorf("incomplete native restore request")}
	}
	data := map[string]interface{}{"backup_id": req.BackupID, "agent_id": req.AgentID, "password": req.Password, "restore_homeassistant": req.RestoreHomeAssistant}
	if req.RestoreDatabase != nil {
		data["restore_database"] = *req.RestoreDatabase
	}
	_, code, err := c.sendWebSocketCommandWithCode(ctx, token, "backup/restore", data)
	if err != nil {
		return classifyBackupError(code, err)
	}
	return nil
}

func classifyBackupError(code string, err error) error {
	if err == nil {
		return nil
	}
	if haErr, ok := err.(*Error); ok && haErr.Type == ErrorTypeAuth {
		return &BackupError{Kind: BackupErrorAuthenticationFailed, Err: err}
	}
	switch strings.ToLower(code) {
	case "unsupported", "not_supported", "unknown_command":
		return &BackupError{Kind: BackupErrorUnsupportedCapability, Err: err}
	case "busy", "backup_in_progress":
		return &BackupError{Kind: BackupErrorBusyBeforeAcceptance, Err: err}
	case "not_found", "backup_not_found", "archive_not_found":
		return &BackupError{Kind: BackupErrorArchiveNotFound, Err: err}
	case "invalid_password", "incorrect_password", "invalid_key":
		return &BackupError{Kind: BackupErrorPasswordIncorrect, Err: err}
	case "source_unavailable", "agent_unavailable":
		return &BackupError{Kind: BackupErrorSourceUnavailable, Err: err}
	}
	return &BackupError{Kind: BackupErrorDispatchUnknown, Err: err}
}
