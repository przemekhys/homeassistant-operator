package haclient

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/gorilla/websocket"
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
	AgentIDs              []string `json:"agent_ids"`
	IncludeDatabase       bool     `json:"include_database"`
	Name                  string   `json:"name,omitempty"`
	Password              string   `json:"password"`
	KeyProtectionVerified bool     `json:"-"`
}
type GenerateBackupResult struct {
	BackupJobID string `json:"backup_job_id"`
}

// BackupDetails is the safe, observable subset of native backup metadata.
// It contains no encryption key or agent credentials.
type BackupDetails struct {
	BackupID string                    `json:"backup_id"`
	Agents   []BackupDestinationResult `json:"agents,omitempty"`
}

type BackupDestinationResult struct {
	AgentID   string          `json:"agent_id"`
	Protected bool            `json:"protected,omitempty"`
	Error     BackupErrorKind `json:"-"`
}

func (r *BackupDestinationResult) UnmarshalJSON(raw []byte) error {
	var value struct {
		AgentID   string `json:"agent_id"`
		Protected bool   `json:"protected,omitempty"`
		Error     string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	r.AgentID, r.Protected = value.AgentID, value.Protected
	if value.Error != "" {
		r.Error = destinationErrorKind(value.Error)
	}
	return nil
}

func destinationErrorKind(code string) BackupErrorKind {
	switch strings.ToLower(code) {
	case "source_unavailable", "agent_unavailable":
		return BackupErrorSourceUnavailable
	case "invalid_password", "incorrect_password", "invalid_key":
		return BackupErrorPasswordIncorrect
	default:
		return BackupErrorDispatchUnknown
	}
}

type RestoreBackupRequest struct {
	BackupID             string `json:"backup_id"`
	AgentID              string `json:"agent_id"`
	Password             string `json:"password"`
	RestoreHomeAssistant bool   `json:"restore_homeassistant"`
	RestoreDatabase      *bool  `json:"restore_database,omitempty"`
}

// BackupEvent is the redacted subset of a native backup event useful for
// operation correlation. It deliberately excludes raw event payloads because
// Home Assistant event data can contain user-defined values.
type BackupEvent struct {
	Type        string
	State       string
	BackupID    string
	BackupJobID string
	AgentID     string
	Stage       string
}

const maxBackupEventReconnects = 1

// SubscribeBackupEvents invokes handler for recognized native backup events
// until ctx is cancelled. A lost subscription is re-established at most once.
// Reconnects only repeat the idempotent subscription command; native backup or
// restore commands are never retried by this method.
func (c *Client) SubscribeBackupEvents(ctx context.Context, token string, handler func(BackupEvent)) error {
	for attempt := 0; ; attempt++ {
		conn, err := c.wsAuthConnect(ctx, token)
		var stop func() bool
		if conn != nil {
			stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
		}
		if err == nil {
			err = subscribeBackupEvents(ctx, conn, handler)
		}
		if err == nil {
			err = readBackupEvents(ctx, conn, handler)
		}
		if conn != nil {
			stop()
			_ = conn.Close()
		}
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			return nil
		}
		if attempt >= maxBackupEventReconnects {
			return &BackupError{Kind: BackupErrorDispatchUnknown, Err: err}
		}
	}
}

func subscribeBackupEvents(ctx context.Context, conn *websocket.Conn, handler func(BackupEvent)) error {
	if err := conn.WriteJSON(map[string]interface{}{"id": 1, "type": "backup/subscribe_events"}); err != nil {
		return fmt.Errorf("write backup event subscription: %w", err)
	}
	for ctx.Err() == nil {
		var frame json.RawMessage
		if err := conn.ReadJSON(&frame); err != nil {
			return fmt.Errorf("read backup event subscription: %w", err)
		}
		var response WebSocketResponse
		if err := json.Unmarshal(frame, &response); err != nil {
			return fmt.Errorf("invalid backup subscription envelope")
		}
		if response.Type == "result" {
			if response.ID != 1 || !response.Success {
				return fmt.Errorf("backup event subscription rejected")
			}
			return nil
		}
		if err := deliverBackupEvent(frame, handler); err != nil {
			return err
		}
	}
	return nil
}

func deliverBackupEvent(frame json.RawMessage, handler func(BackupEvent)) error {
	event, recognized, err := parseBackupEvent(frame)
	if err != nil {
		return err
	}
	if recognized && handler != nil {
		handler(event)
	}
	return nil
}

func readBackupEvents(ctx context.Context, conn *websocket.Conn, handler func(BackupEvent)) error {
	for ctx.Err() == nil {
		var frame json.RawMessage
		if err := conn.ReadJSON(&frame); err != nil {
			return fmt.Errorf("read backup event: %w", err)
		}
		if err := deliverBackupEvent(frame, handler); err != nil {
			return err
		}
	}
	return nil
}

func parseBackupEvent(frame json.RawMessage) (BackupEvent, bool, error) {
	var envelope struct {
		Type  string `json:"type"`
		Event struct {
			Type         string          `json:"event_type"`
			Data         json.RawMessage `json:"data"`
			ManagerState string          `json:"manager_state"`
			State        string          `json:"state"`
			Stage        string          `json:"stage"`
			AgentID      string          `json:"agent_id"`
		} `json:"event"`
	}
	if err := json.Unmarshal(frame, &envelope); err != nil {
		return BackupEvent{}, false, fmt.Errorf("decode backup event envelope: %w", err)
	}
	if envelope.Type != "event" {
		return BackupEvent{}, false, nil
	}
	if envelope.Event.ManagerState != "" {
		switch envelope.Event.ManagerState {
		case "idle", "blocked", "create_backup", "receive_backup", "restore_backup":
			return BackupEvent{Type: envelope.Event.ManagerState, State: envelope.Event.State,
				Stage: envelope.Event.Stage, AgentID: envelope.Event.AgentID}, true, nil
		default:
			return BackupEvent{}, false, nil
		}
	}
	if !strings.HasPrefix(envelope.Event.Type, "backup_") {
		return BackupEvent{}, false, nil
	}
	var data struct {
		BackupID    string `json:"backup_id"`
		BackupJobID string `json:"backup_job_id"`
		AgentID     string `json:"agent_id"`
		Stage       string `json:"stage"`
	}
	if len(envelope.Event.Data) != 0 {
		if err := json.Unmarshal(envelope.Event.Data, &data); err != nil {
			return BackupEvent{}, false, fmt.Errorf("decode backup event data: %w", err)
		}
	}
	return BackupEvent{
		Type:        envelope.Event.Type,
		BackupID:    data.BackupID,
		BackupJobID: data.BackupJobID,
		AgentID:     data.AgentID,
		Stage:       data.Stage,
	}, true, nil
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

// IsBackupManagerIdle is observation only. A visible local archive can precede
// completion of remote distribution, so details alone must not release a writer.
func (c *Client) IsBackupManagerIdle(ctx context.Context, token string) (bool, error) {
	raw, code, err := c.sendWebSocketCommandWithCode(ctx, token, "backup/info", nil)
	if err != nil {
		return false, classifyBackupError(code, err)
	}
	var info struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return false, &BackupError{Kind: BackupErrorUnsupportedCapability}
	}
	switch info.State {
	case "idle":
		return true, nil
	case "blocked", "create_backup", "receive_backup", "restore_backup":
		return false, nil
	default:
		return false, &BackupError{Kind: BackupErrorUnsupportedCapability}
	}
}

// GetBackupDetails retrieves a single backup's observable native metadata.
func (c *Client) GetBackupDetails(ctx context.Context, token, backupID string) (*BackupDetails, error) {
	if backupID == "" {
		return nil, &BackupError{Kind: BackupErrorArchiveNotFound}
	}
	raw, code, err := c.sendWebSocketCommandWithCode(
		ctx,
		token,
		"backup/details",
		map[string]interface{}{"backup_id": backupID},
	)
	if err != nil {
		return nil, classifyBackupError(code, err)
	}
	return decodeNativeBackupDetails(raw, backupID)
}

func decodeNativeBackupDetails(raw json.RawMessage, backupID string) (*BackupDetails, error) {
	var response struct {
		Backup *struct {
			BackupID string `json:"backup_id"`
			Agents   map[string]struct {
				Protected bool `json:"protected"`
			} `json:"agents"`
			FailedAgentIDs []string `json:"failed_agent_ids"`
		} `json:"backup"`
		AgentErrors map[string]json.RawMessage `json:"agent_errors"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, &BackupError{Kind: BackupErrorUnsupportedCapability}
	}
	if response.Backup == nil {
		return nil, &BackupError{Kind: BackupErrorArchiveNotFound}
	}
	if response.Backup.BackupID != backupID {
		return nil, &BackupError{Kind: BackupErrorUnsupportedCapability}
	}
	byAgent := make(map[string]BackupDestinationResult)
	for id, status := range response.Backup.Agents {
		byAgent[id] = BackupDestinationResult{AgentID: id, Protected: status.Protected}
	}
	for _, id := range response.Backup.FailedAgentIDs {
		byAgent[id] = BackupDestinationResult{AgentID: id, Error: BackupErrorSourceUnavailable}
	}
	for id, failure := range response.AgentErrors {
		if len(failure) > 0 && string(failure) != "null" {
			byAgent[id] = BackupDestinationResult{AgentID: id, Error: BackupErrorSourceUnavailable}
		}
	}
	ids := make([]string, 0, len(byAgent))
	for id := range byAgent {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	details := &BackupDetails{BackupID: backupID, Agents: make([]BackupDestinationResult, 0, len(ids))}
	for _, id := range ids {
		details.Agents = append(details.Agents, byAgent[id])
	}
	return details, nil
}

// MergeBackupConfig overlays fields owned by the operator while retaining
// Home Assistant-owned password and agent-protection settings.
func MergeBackupConfig(observed BackupConfig, requested BackupConfigRequest) BackupConfig {
	merged := observed
	if requested.Schedule != nil {
		merged.Schedule = *requested.Schedule
	}
	if requested.Retention != nil {
		merged.Retention = *requested.Retention
	}
	if requested.CreateBackup != nil {
		merged.CreateBackup.IncludeDatabase = requested.CreateBackup.IncludeDatabase
		merged.CreateBackup.AgentIDs = normalizeBackupAgentIDs(requested.CreateBackup.AgentIDs)
		if requested.CreateBackup.Password != nil {
			merged.CreateBackup.Password = requested.CreateBackup.Password
		}
	}
	merged.CreateBackup.AgentIDs = normalizeBackupAgentIDs(merged.CreateBackup.AgentIDs)
	return merged
}

// BackupConfigEqual compares operator-relevant configuration after canonical
// agent ordering so HA's set-like agent representation does not cause churn.
func BackupConfigEqual(left, right BackupConfig) bool {
	left.CreateBackup.AgentIDs = normalizeBackupAgentIDs(left.CreateBackup.AgentIDs)
	right.CreateBackup.AgentIDs = normalizeBackupAgentIDs(right.CreateBackup.AgentIDs)
	return reflect.DeepEqual(left, right)
}

// ReconcileBackupConfig reads the native configuration, applies only the
// requested operator-owned fields, and writes it back only when it changed.
// The observed password remains private to this client and is included solely
// to avoid clearing HA-owned encryption settings during a replacement update.
func (c *Client) ReconcileBackupConfig(ctx context.Context, token string, requested BackupConfigRequest) (bool, error) {
	observed, err := c.GetBackupConfig(ctx, token)
	if err != nil {
		return false, err
	}
	merged := MergeBackupConfig(*observed, requested)
	if BackupConfigEqual(*observed, merged) {
		return false, nil
	}
	update := BackupConfigRequest{
		Schedule:     &merged.Schedule,
		Retention:    &merged.Retention,
		CreateBackup: &merged.CreateBackup,
	}
	if err := c.ConfigureBackup(ctx, token, &update); err != nil {
		return false, err
	}
	return true, nil
}

func normalizeBackupAgentIDs(agentIDs []string) []string {
	if agentIDs == nil {
		return nil
	}
	result := append([]string(nil), agentIDs...)
	sort.Strings(result)
	result = slicesCompact(result)
	return result
}

func slicesCompact(values []string) []string {
	if len(values) < 2 {
		return values
	}
	write := 1
	for _, value := range values[1:] {
		if value != values[write-1] {
			values[write] = value
			write++
		}
	}
	return values[:write]
}

func (c *Client) GenerateBackup(
	ctx context.Context,
	token string,
	req GenerateBackupRequest,
) (*GenerateBackupResult, error) {
	if req.Password == "" || !req.KeyProtectionVerified {
		return nil, &BackupError{Kind: BackupErrorPasswordIncorrect}
	}
	raw, code, err := c.sendWebSocketCommandWithCode(ctx, token, "backup/generate", map[string]interface{}{
		"agent_ids": req.AgentIDs, "include_database": req.IncludeDatabase, "name": req.Name, "password": req.Password,
		"include_homeassistant": true,
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
		return &BackupError{
			Kind: BackupErrorUnsupportedCapability,
			Err:  fmt.Errorf("incomplete native restore request"),
		}
	}
	data := map[string]interface{}{
		"backup_id":             req.BackupID,
		"agent_id":              req.AgentID,
		"password":              req.Password,
		"restore_homeassistant": req.RestoreHomeAssistant,
	}
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
