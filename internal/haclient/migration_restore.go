package haclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

// MigrationBackupExpectation binds onboarding evidence to the already verified
// archive. It deliberately contains no secret material.
type MigrationBackupExpectation struct {
	BackupID        string
	AgentID         string
	Size            int64
	RestoreDatabase bool
}

// MigrationOnboardingBackupEvidence is the strictly selected backup returned
// by HA Core 2026.11.0's onboarding backup/info endpoint.
type MigrationOnboardingBackupEvidence struct {
	BackupID              string
	AgentID               string
	Size                  int64
	Protected             bool
	DatabaseIncluded      bool
	HomeAssistantIncluded bool
	HomeAssistantVersion  string
	Folders               []string
}

type onboardingBackupInfoResponse struct {
	Backups []struct {
		BackupID              string          `json:"backup_id"`
		Addons                json.RawMessage `json:"addons"`
		Date                  string          `json:"date"`
		DatabaseIncluded      *bool           `json:"database_included"`
		ExtraMetadata         json.RawMessage `json:"extra_metadata"`
		FailedAddons          json.RawMessage `json:"failed_addons"`
		FailedAgentIDs        json.RawMessage `json:"failed_agent_ids"`
		FailedFolders         json.RawMessage `json:"failed_folders"`
		HomeAssistantIncluded *bool           `json:"homeassistant_included"`
		HomeAssistantVersion  *string         `json:"homeassistant_version"`
		Name                  string          `json:"name"`
		WithAutomaticSettings json.RawMessage `json:"with_automatic_settings"`
		Folders               []string        `json:"folders"`
		Agents                map[string]struct {
			Protected *bool  `json:"protected"`
			Size      *int64 `json:"size"`
		} `json:"agents"`
	} `json:"backups"`
	State           json.RawMessage `json:"state"`
	LastActionEvent json.RawMessage `json:"last_action_event"`
}

// UploadMigrationBackup streams a protected archive to an un-onboarded target.
// The archive is never copied into memory and neither its path nor its contents
// are included in returned errors.
func (c *Client) UploadMigrationBackup(
	ctx context.Context,
	archive io.Reader,
	name string,
	size int64,
	agentID string,
) error {
	if archive == nil || strings.TrimSpace(name) == "" || size < 0 ||
		strings.TrimSpace(agentID) == "" {
		return fmt.Errorf("migration archive, size, and agent ID are required")
	}
	var prefix bytes.Buffer
	writer := multipart.NewWriter(&prefix)
	if _, err := writer.CreateFormFile("file", filepath.Base(name)); err != nil {
		return fmt.Errorf("create migration upload")
	}
	boundary := writer.Boundary()
	suffix := []byte("\r\n--" + boundary + "--\r\n")
	endpoint, err := c.migrationOnboardingURL("/api/onboarding/backup/upload")
	if err != nil {
		return err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid migration upload endpoint")
	}
	query := parsed.Query()
	query.Set("agent_id", agentID)
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		parsed.String(),
		io.MultiReader(bytes.NewReader(prefix.Bytes()), archive, bytes.NewReader(suffix)),
	)
	if err != nil {
		return fmt.Errorf("create migration upload request")
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.ContentLength = int64(prefix.Len()) + size + int64(len(suffix))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{
			Type:    ErrorTypeOutcomeUnknown,
			Message: "migration upload outcome unknown",
			Err:     err,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if resp.StatusCode == http.StatusBadGateway {
			return &Error{Type: ErrorTypeOutcomeUnknown, Message: "migration upload outcome unknown", StatusCode: resp.StatusCode}
		}
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("migration upload rejected: HTTP %d", resp.StatusCode),
			StatusCode: resp.StatusCode,
		}
	}
	return nil
}

func (c *Client) MigrationOnboardingBackupInfo(ctx context.Context, expected MigrationBackupExpectation) (*MigrationOnboardingBackupEvidence, error) {
	if strings.TrimSpace(expected.BackupID) == "" || strings.TrimSpace(expected.AgentID) == "" || expected.Size <= 0 {
		return nil, fmt.Errorf("migration backup identity is required")
	}
	endpoint, err := c.migrationOnboardingURL("/api/onboarding/backup/info")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create migration info request")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{
			Type:    ErrorTypeNotReady,
			Message: "migration onboarding unavailable",
			Err:     err,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("migration info rejected: HTTP %d", resp.StatusCode),
			StatusCode: resp.StatusCode,
		}
	}
	var body onboardingBackupInfoResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || len(body.State) == 0 || len(body.LastActionEvent) == 0 {
		return nil, fmt.Errorf("migration backup info evidence invalid")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("migration backup info evidence invalid")
	}
	var evidence *MigrationOnboardingBackupEvidence
	for _, backup := range body.Backups {
		if backup.BackupID != expected.BackupID {
			continue
		}
		agent, found := backup.Agents[expected.AgentID]
		if !found || agent.Protected == nil || agent.Size == nil || backup.DatabaseIncluded == nil || backup.HomeAssistantIncluded == nil || backup.HomeAssistantVersion == nil || *agent.Size != expected.Size || !*agent.Protected || !*backup.HomeAssistantIncluded || (*backup.DatabaseIncluded != expected.RestoreDatabase) {
			return nil, fmt.Errorf("migration backup info evidence does not match archive scope")
		}
		if evidence != nil {
			return nil, fmt.Errorf("migration backup info evidence is ambiguous")
		}
		evidence = &MigrationOnboardingBackupEvidence{BackupID: backup.BackupID, AgentID: expected.AgentID, Size: *agent.Size, Protected: *agent.Protected, DatabaseIncluded: *backup.DatabaseIncluded, HomeAssistantIncluded: *backup.HomeAssistantIncluded, HomeAssistantVersion: *backup.HomeAssistantVersion, Folders: append([]string(nil), backup.Folders...)}
	}
	if evidence == nil {
		return nil, fmt.Errorf("migration backup is not present in onboarding evidence")
	}
	return evidence, nil
}

// RestoreMigrationOnboardingBackup sends the recovery key only in the JSON body.
func (c *Client) RestoreMigrationOnboardingBackup(
	ctx context.Context,
	backupID, agentID, recoveryKey string, restoreDatabase bool,
) error {
	if strings.TrimSpace(backupID) == "" || strings.TrimSpace(agentID) == "" ||
		recoveryKey == "" {
		return fmt.Errorf("backup ID, agent ID, and recovery key are required")
	}
	endpoint, err := c.migrationOnboardingURL("/api/onboarding/backup/restore")
	if err != nil {
		return err
	}
	body, err := json.Marshal(
		map[string]any{
			"backup_id":        backupID,
			"agent_id":         agentID,
			"password":         recoveryKey,
			"restore_database": restoreDatabase,
		},
	)
	if err != nil {
		return fmt.Errorf("encode migration restore request")
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create migration restore request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{
			Type:    ErrorTypeOutcomeUnknown,
			Message: "migration restore outcome unknown",
			Err:     err,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if resp.StatusCode == http.StatusBadGateway {
			return &Error{Type: ErrorTypeOutcomeUnknown, Message: "migration restore outcome unknown", StatusCode: resp.StatusCode}
		}
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("migration restore rejected: HTTP %d", resp.StatusCode),
			StatusCode: resp.StatusCode,
		}
	}
	return nil
}

func (c *Client) migrationOnboardingURL(path string) (string, error) {
	endpoint, err := url.Parse(c.baseURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" ||
		endpoint.Query().Has("password") {
		return "", fmt.Errorf("invalid migration target URL")
	}
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + path
	endpoint.RawQuery = ""
	return endpoint.String(), nil
}
