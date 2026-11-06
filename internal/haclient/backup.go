package haclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const backupCommandTimeout = 10 * time.Second

// sendBackupCommand confines every backup command to a short-lived connection.
// It preserves HA's machine-readable code without copying an HA response into an
// error, because backup configuration responses can contain recovery material.
func (c *Client) sendBackupCommand(
	ctx context.Context, token, command string, data map[string]interface{},
) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, backupCommandTimeout)
	defer cancel()
	result, code, err := c.sendWebSocketCommandWithCode(ctx, token, command, data)
	if err != nil {
		if code != "" {
			return nil, &Error{Type: ErrorTypeHTTP, Code: code, Message: fmt.Sprintf("backup command rejected (%s)", code)}
		}
		return nil, &Error{Type: ErrorTypeHTTP, Message: "backup command transport failed", Err: err}
	}
	return result, nil
}

// ListBackupAgents returns the destinations currently advertised by HA.
func (c *Client) ListBackupAgents(ctx context.Context, token string) ([]BackupAgent, error) {
	raw, err := c.sendBackupCommand(ctx, token, "backup/agents/info", nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Agents []BackupAgent `json:"agents"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "invalid backup agents response", Err: err}
	}
	return response.Agents, nil
}

// GetBackupInfo reads HA's current inventory and latest observable backup data.
func (c *Client) GetBackupInfo(ctx context.Context, token string) (*BackupInfo, error) {
	raw, err := c.sendBackupCommand(ctx, token, "backup/info", nil)
	if err != nil {
		return nil, err
	}
	var info BackupInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "invalid backup info response", Err: err}
	}
	return &info, nil
}

// GetBackupDetails reads the final metadata for one backup only.
func (c *Client) GetBackupDetails(ctx context.Context, token, backupID string) (*BackupDetails, error) {
	raw, err := c.sendBackupCommand(ctx, token, "backup/details", map[string]interface{}{"backup_id": backupID})
	if err != nil {
		return nil, err
	}
	var details BackupDetails
	if err := json.Unmarshal(raw, &details); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "invalid backup details response", Err: err}
	}
	return &details, nil
}

// GetBackupConfig reads the complete HA configuration. The value is only for
// comparison and key protection; callers must use ConfigureBackup to write.
func (c *Client) GetBackupConfig(ctx context.Context, token string) (*BackupConfig, error) {
	raw, err := c.sendBackupCommand(ctx, token, "backup/config/info", nil)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "invalid backup config response"}
	}
	if nested, ok := object["config"]; ok {
		if err := json.Unmarshal(nested, &object); err != nil || object == nil {
			return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "invalid backup config response"}
		}
		raw = nested
	}
	var createBackup map[string]json.RawMessage
	if err := json.Unmarshal(object["create_backup"], &createBackup); err != nil || createBackup == nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "invalid backup config response"}
	}
	var config BackupConfig
	if err := json.Unmarshal(raw, &config); err != nil || config.Schedule.Recurrence == "" {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "invalid backup config response"}
	}
	return &config, nil
}

// ConfigureBackup updates only fields owned by the operator. In particular it
// never serializes a recovery password returned by HA.
func (c *Client) ConfigureBackup(ctx context.Context, token string, req *BackupConfigRequest) error {
	data := map[string]interface{}{}
	if req.Schedule != nil {
		data["schedule"] = req.Schedule
	}
	if req.Retention != nil {
		data["retention"] = req.Retention
	}
	if req.CreateBackup != nil {
		data["create_backup"] = map[string]interface{}{
			"agent_ids": req.CreateBackup.AgentIDs, "include_database": req.CreateBackup.IncludeDatabase,
		}
	}
	_, err := c.sendBackupCommand(ctx, token, "backup/config/update", data)
	return err
}

// GenerateBackupWithAutomaticSettings starts one native backup. It must never
// be retried automatically because a lost response can still mean HA accepted it.
func (c *Client) GenerateBackupWithAutomaticSettings(ctx context.Context, token string) (string, error) {
	raw, err := c.sendBackupCommand(ctx, token, "backup/generate_with_automatic_settings", nil)
	if err != nil {
		return "", err
	}
	var result BackupGenerateResult
	if err := json.Unmarshal(raw, &result); err != nil || result.JobID == "" {
		if err == nil {
			err = &Error{Type: ErrorTypeInvalidResponse, Message: "empty backup job id"}
		}
		return "", &Error{Type: ErrorTypeInvalidResponse, Message: "invalid backup generate response", Err: err}
	}
	return result.JobID, nil
}

// DownloadBackup opens a stream for an archive held by a native HA backup
// agent. Callers own and must close the response body. The method deliberately
// has no password argument: protected archive downloads must remain encrypted.
func (c *Client) DownloadBackup(ctx context.Context, token, backupID, agentID string) (*http.Response, error) {
	endpoint := fmt.Sprintf("%s/api/backup/download/%s?agent_id=%s",
		c.baseURL, url.PathEscape(backupID), url.QueryEscape(agentID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to create backup download request", Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "backup download failed", Err: err}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_ = resp.Body.Close()
		return nil, &Error{Type: ErrorTypeHTTP, StatusCode: resp.StatusCode, Message: "backup download was rejected"}
	}
	return resp, nil
}
