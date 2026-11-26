package controller

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const migrationWorkerImagePullSecretsEnv = "MIGRATION_WORKER_IMAGE_PULL_SECRETS"

// migrationWorkerImagePullSecrets reads the image pull secrets propagated by a
// supported installation method. They must exist in each migration namespace.
func migrationWorkerImagePullSecrets() ([]corev1.LocalObjectReference, error) {
	raw := strings.TrimSpace(os.Getenv(migrationWorkerImagePullSecretsEnv))
	if raw == "" {
		return nil, nil
	}
	var secrets []corev1.LocalObjectReference
	if err := json.Unmarshal([]byte(raw), &secrets); err != nil {
		return nil, fmt.Errorf("invalid migration worker image pull secrets")
	}
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		if len(validation.IsDNS1123Subdomain(secret.Name)) != 0 {
			return nil, fmt.Errorf("invalid migration worker image pull secret")
		}
		if _, exists := seen[secret.Name]; exists {
			return nil, fmt.Errorf("duplicate migration worker image pull secret")
		}
		seen[secret.Name] = struct{}{}
	}
	return secrets, nil
}
