package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var recoveryHTTPClient = &http.Client{Timeout: 10 * time.Second}

// NativeBackupDownloadURL builds the native encrypted-archive endpoint. It
// deliberately accepts no recovery key or password: including either in the
// URL would request an unsafe decrypted export and risks process-list leaks.
func NativeBackupDownloadURL(baseURL, backupID, agentID string) (string, error) {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(backupID) == "" || strings.TrimSpace(agentID) == "" {
		return "", fmt.Errorf("backup download requires base URL, backup ID, and agent ID")
	}
	endpoint, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid backup base URL: %w", err)
	}
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + "/api/backup/download/" + url.PathEscape(backupID)
	query := endpoint.Query()
	query.Set("agent_id", agentID)
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

// ValidateRecoveryTarget rejects source-like or externally exposed targets
// before a runner begins native restore onboarding.
func ValidateRecoveryTarget(name, publishedAddress string, internalNetwork bool) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("recovery target name is required")
	}
	if !internalNetwork {
		return fmt.Errorf("recovery target must use an internal network")
	}
	if publishedAddress != "" && !strings.HasPrefix(publishedAddress, "127.0.0.1:") {
		return fmt.Errorf("recovery target may only publish to loopback")
	}
	return nil
}

// RecoveryContainerArgs returns a Docker invocation for an isolated disposable
// target. It intentionally mounts only a fresh writable configuration directory
// and publishes only loopback; callers execute and clean up the resources.
func RecoveryContainerArgs(name, network, configDir, image string) ([]string, error) {
	if err := ValidateRecoveryTarget(name, "127.0.0.1:18124", true); err != nil {
		return nil, err
	}
	if strings.TrimSpace(network) == "" || strings.TrimSpace(configDir) == "" || strings.TrimSpace(image) == "" {
		return nil, fmt.Errorf("recovery network, config directory, and image are required")
	}
	if !filepath.IsAbs(configDir) {
		return nil, fmt.Errorf("recovery config directory must be absolute")
	}
	return []string{
		"run", "--detach", "--name", name,
		"--network", network,
		"--publish", "127.0.0.1:18124:8123",
		"--mount", "type=bind,src=" + configDir + ",dst=/config",
		image,
	}, nil
}

// RecoveryWorkspace is an ephemeral, mode-0700 directory owned by one recovery
// test. Its cleanup never targets a caller-provided parent or source archive.
type RecoveryWorkspace struct {
	Dir       string
	ConfigDir string
	Archive   string
	Key       string
}

func NewRecoveryWorkspace(parent string) (*RecoveryWorkspace, error) {
	if parent == "" {
		parent = os.TempDir()
	}
	dir, err := os.MkdirTemp(parent, "ha-backup-recovery-")
	if err != nil {
		return nil, fmt.Errorf("create recovery workspace: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("secure recovery workspace: %w", err)
	}
	configDir := filepath.Join(dir, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("create recovery config directory: %w", err)
	}
	return &RecoveryWorkspace{
		Dir: dir, ConfigDir: configDir,
		Archive: filepath.Join(dir, "backup.tar"), Key: filepath.Join(dir, "recovery-key"),
	}, nil
}

func (w *RecoveryWorkspace) Cleanup() error {
	if w == nil || w.Dir == "" || filepath.Base(w.Dir) == "." {
		return nil
	}
	return os.RemoveAll(w.Dir)
}

// RecoveredStorageContains checks a restored local-storage file without
// returning its contents. It is limited to the recovery workspace so fixture
// assertions cannot read the source instance or expose user-originated data.
func RecoveredStorageContains(configDir, pattern, value string) (bool, error) {
	if !filepath.IsAbs(configDir) || strings.TrimSpace(pattern) == "" || strings.TrimSpace(value) == "" {
		return false, fmt.Errorf("recovery storage directory, pattern, and value are required")
	}
	files, err := filepath.Glob(filepath.Join(configDir, ".storage", pattern))
	if err != nil {
		return false, fmt.Errorf("resolve recovery storage files: %w", err)
	}
	for _, file := range files {
		contents, readErr := os.ReadFile(file)
		if readErr != nil {
			return false, fmt.Errorf("read recovery storage file: %w", readErr)
		}
		if bytes.Contains(contents, []byte(value)) {
			return true, nil
		}
	}
	return false, nil
}

// DownloadProtectedBackup copies an already-confirmed protected archive to the
// private workspace. Callers supply an Authorization header value; it is never
// logged, returned, or written to disk by this helper.
func DownloadProtectedBackup(
	ctx context.Context, client *http.Client, endpoint, authorization, destination string, protected bool,
) (int64, error) {
	if !protected {
		return 0, fmt.Errorf("refusing to download an archive not confirmed as protected")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Query().Has("password") {
		return 0, fmt.Errorf("invalid protected backup download endpoint")
	}
	if client == nil {
		client = recoveryHTTPClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("create protected backup download request: %w", err)
	}
	req.Header.Set("Authorization", authorization)
	response, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download protected backup: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return 0, fmt.Errorf("protected backup download rejected: HTTP %d", response.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create protected backup file: %w", err)
	}
	defer file.Close()
	bytes, copyErr := io.Copy(file, response.Body)
	if copyErr != nil {
		_ = os.Remove(destination)
		return 0, fmt.Errorf("write protected backup file: %w", copyErr)
	}
	return bytes, nil
}

// UploadBackupForOnboarding uploads an encrypted archive to a new, isolated HA
// target before user onboarding. It is test-runner code only.
func UploadBackupForOnboarding(ctx context.Context, client *http.Client, baseURL, archivePath string) error {
	if client == nil {
		client = recoveryHTTPClient
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open recovery archive: %w", err)
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil {
		return fmt.Errorf("stat recovery archive: %w", err)
	}
	var prefix bytes.Buffer
	multipartWriter := multipart.NewWriter(&prefix)
	if _, err := multipartWriter.CreateFormFile("file", filepath.Base(archivePath)); err != nil {
		return fmt.Errorf("create recovery upload form: %w", err)
	}
	boundary := multipartWriter.Boundary()
	// The writer's opening headers are in prefix. Supply its fixed closing
	// boundary separately so the archive stays a single file stream rather than
	// being copied into memory or a second on-disk artifact.
	suffix := []byte("\r\n--" + boundary + "--\r\n")
	reader := io.MultiReader(bytes.NewReader(prefix.Bytes()), archive, bytes.NewReader(suffix))
	endpoint, err := onboardingBackupURL(baseURL, "/api/onboarding/backup/upload")
	if err != nil {
		return err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid onboarding upload endpoint")
	}
	query := parsed.Query()
	query.Add("agent_id", "backup.local")
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), reader)
	if err != nil {
		return fmt.Errorf("create onboarding upload request: %w", err)
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	req.ContentLength = int64(prefix.Len()) + info.Size() + int64(len(suffix))
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upload recovery archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// HA may include the archive's user-provided metadata in its detailed
		// error text. Consume only enough to classify the failure and never
		// return that body to an E2E log.
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("onboarding upload rejected: HTTP %d (%s)", response.StatusCode, onboardingUploadFailureKind(string(body)))
	}
	return nil
}

func onboardingUploadFailureKind(body string) string {
	if strings.Contains(body, "backup.json") {
		return "invalid archive metadata"
	}
	if strings.Contains(body, "Can't write backup file") {
		return "recovery storage failure"
	}
	if strings.TrimSpace(body) == "" {
		return "server error"
	}
	return "server rejected upload"
}

// OnboardingBackupInfo verifies that a fresh target recognizes an archive. The
// response is intentionally not returned because runner assertions do not need
// to expose its archive metadata.
func OnboardingBackupInfo(ctx context.Context, client *http.Client, baseURL string) error {
	if client == nil {
		client = recoveryHTTPClient
	}
	endpoint, err := onboardingBackupURL(baseURL, "/api/onboarding/backup/info")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create onboarding info request: %w", err)
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("read onboarding backup info: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("onboarding backup info rejected: HTTP %d", response.StatusCode)
	}
	return nil
}

// RestoreOnboardingBackup restores an uploaded archive while the recovery
// target is still in onboarding. The recovery key is confined to the JSON body
// and is not included in errors, URLs, or returned values.
func RestoreOnboardingBackup(ctx context.Context, client *http.Client, baseURL, backupID, agentID, password string) error {
	if strings.TrimSpace(backupID) == "" || strings.TrimSpace(agentID) == "" || password == "" {
		return fmt.Errorf("onboarding restore requires backup ID, agent ID, and recovery key")
	}
	if client == nil {
		client = recoveryHTTPClient
	}
	endpoint, err := onboardingBackupURL(baseURL, "/api/onboarding/backup/restore")
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"backup_id": backupID, "agent_id": agentID, "password": password,
		"restore_database": true,
	})
	if err != nil {
		return fmt.Errorf("encode onboarding restore request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("create onboarding restore request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("restore onboarding backup: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("onboarding restore rejected: HTTP %d", response.StatusCode)
	}
	return nil
}

func onboardingBackupURL(baseURL, endpointPath string) (string, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return "", fmt.Errorf("invalid recovery target URL")
	}
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + endpointPath
	endpoint.RawQuery = ""
	return endpoint.String(), nil
}

// RestoreNativeBackup invokes HA's native restore command on an already
// isolated, authenticated recovery target. It belongs to the test runner only:
// production reconciliation must never restore an instance. The password stays
// in the WebSocket frame and is never added to a URL, error, or returned value.
func RestoreNativeBackup(ctx context.Context, baseURL, token, backupID, agentID, password string) error {
	if strings.TrimSpace(backupID) == "" || strings.TrimSpace(agentID) == "" || password == "" {
		return fmt.Errorf("restore requires backup ID, agent ID, and recovery key")
	}
	endpoint, err := nativeBackupWebSocketURL(baseURL)
	if err != nil {
		return err
	}
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("connect recovery target: %w", err)
	}
	defer connection.Close()
	var authRequired struct {
		Type string `json:"type"`
	}
	if err := connection.ReadJSON(&authRequired); err != nil || authRequired.Type != "auth_required" {
		return fmt.Errorf("recovery target did not request authentication")
	}
	if err := connection.WriteJSON(map[string]any{"type": "auth", "access_token": token}); err != nil {
		return fmt.Errorf("authenticate recovery target: %w", err)
	}
	var auth struct {
		Type string `json:"type"`
	}
	if err := connection.ReadJSON(&auth); err != nil || auth.Type != "auth_ok" {
		return fmt.Errorf("recovery target authentication failed")
	}
	const commandID = 1
	if err := connection.WriteJSON(map[string]any{
		"id": commandID, "type": "backup/restore", "backup_id": backupID, "agent_id": agentID,
		"password": password, "restore_database": true, "restore_homeassistant": true,
	}); err != nil {
		return fmt.Errorf("send native restore command: %w", err)
	}
	var result struct {
		ID      int    `json:"id"`
		Type    string `json:"type"`
		Success bool   `json:"success"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := connection.ReadJSON(&result); err != nil {
		return fmt.Errorf("read native restore result: %w", err)
	}
	if result.ID != commandID || result.Type != "result" || !result.Success {
		if result.Error.Code != "" {
			return fmt.Errorf("native restore rejected (%s)", result.Error.Code)
		}
		return fmt.Errorf("native restore was not accepted")
	}
	return nil
}

// SetNativeBackupPassword is a test-fixture operation for preparing a protected
// archive. Production code deliberately has no equivalent method.
func SetNativeBackupPassword(ctx context.Context, baseURL, token, password string) error {
	if password == "" {
		return fmt.Errorf("backup encryption key is required")
	}
	endpoint, err := nativeBackupWebSocketURL(baseURL)
	if err != nil {
		return err
	}
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("connect backup fixture: %w", err)
	}
	defer connection.Close()
	var greeting struct {
		Type string `json:"type"`
	}
	if err := connection.ReadJSON(&greeting); err != nil || greeting.Type != "auth_required" {
		return fmt.Errorf("backup fixture did not request authentication")
	}
	if err := connection.WriteJSON(map[string]any{"type": "auth", "access_token": token}); err != nil {
		return fmt.Errorf("authenticate backup fixture: %w", err)
	}
	var auth struct {
		Type string `json:"type"`
	}
	if err := connection.ReadJSON(&auth); err != nil || auth.Type != "auth_ok" {
		return fmt.Errorf("backup fixture authentication failed")
	}
	if err := connection.WriteJSON(map[string]any{
		"id": 1, "type": "backup/config/update", "create_backup": map[string]string{"password": password},
	}); err != nil {
		return fmt.Errorf("set backup encryption key: %w", err)
	}
	var result struct {
		ID      int    `json:"id"`
		Type    string `json:"type"`
		Success bool   `json:"success"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := connection.ReadJSON(&result); err != nil || result.ID != 1 || result.Type != "result" || !result.Success {
		if result.Error.Code != "" {
			return fmt.Errorf("backup encryption key rejected (%s)", result.Error.Code)
		}
		return fmt.Errorf("backup encryption key was not accepted")
	}
	return nil
}

func nativeBackupWebSocketURL(baseURL string) (string, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return "", fmt.Errorf("invalid recovery target URL")
	}
	if endpoint.Query().Has("password") {
		return "", fmt.Errorf("recovery target URL must not contain a password")
	}
	switch endpoint.Scheme {
	case "http":
		endpoint.Scheme = "ws"
	case "https":
		endpoint.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("invalid recovery target URL")
	}
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + "/api/websocket"
	endpoint.RawQuery = ""
	return endpoint.String(), nil
}
