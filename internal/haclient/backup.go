package haclient

import (
	"context"
	"encoding/json"
)

// ListBackupAgents returns the destinations currently advertised by HA.
func (c *Client) ListBackupAgents(ctx context.Context, token string) ([]BackupAgent, error) {
	raw, err := c.SendWebSocketCommand(ctx, token, "backup/agents/info", nil)
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
	raw, err := c.SendWebSocketCommand(ctx, token, "backup/info", nil)
	if err != nil {
		return nil, err
	}
	var info BackupInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "invalid backup info response", Err: err}
	}
	return &info, nil
}

// GenerateBackupWithAutomaticSettings starts one native backup. It must never
// be retried automatically because a lost response can still mean HA accepted it.
func (c *Client) GenerateBackupWithAutomaticSettings(ctx context.Context, token string) (string, error) {
	raw, err := c.SendWebSocketCommand(ctx, token, "backup/generate_with_automatic_settings", nil)
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
