package utils

import (
	"fmt"
	"net/url"
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
