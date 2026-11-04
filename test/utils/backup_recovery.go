package utils

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

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
		client = http.DefaultClient
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
