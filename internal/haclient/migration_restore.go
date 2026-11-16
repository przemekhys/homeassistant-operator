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
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("migration upload rejected: HTTP %d", resp.StatusCode),
			StatusCode: resp.StatusCode,
		}
	}
	return nil
}

func (c *Client) MigrationOnboardingBackupInfo(ctx context.Context) error {
	endpoint, err := c.migrationOnboardingURL("/api/onboarding/backup/info")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create migration info request")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{
			Type:    ErrorTypeNotReady,
			Message: "migration onboarding unavailable",
			Err:     err,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("migration info rejected: HTTP %d", resp.StatusCode),
			StatusCode: resp.StatusCode,
		}
	}
	return nil
}

// RestoreMigrationOnboardingBackup sends the recovery key only in the JSON body.
func (c *Client) RestoreMigrationOnboardingBackup(
	ctx context.Context,
	backupID, agentID, recoveryKey string,
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
			"restore_database": true,
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
