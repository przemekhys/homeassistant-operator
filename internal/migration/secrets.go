package migration

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

// CreateImportedSecret creates the one immutable Secret that an approved
// import may need. Values are accepted only from the caller's already verified
// source and are never included in errors. Existing Secrets are never updated.
func CreateImportedSecret(
	ctx context.Context,
	kube client.Client,
	scheme *runtime.Scheme,
	migration *hav1alpha1.HomeAssistantMigration,
	values map[string][]byte,
) (*corev1.Secret, error) {
	if kube == nil || scheme == nil || migration == nil ||
		strings.TrimSpace(migration.Name) == "" ||
		len(values) == 0 {
		return nil, fmt.Errorf("imported Secret requires a migration and available values")
	}
	data := make(map[string][]byte, len(values))
	for key, value := range values {
		if strings.TrimSpace(key) == "" || len(validation.IsConfigMapKey(key)) > 0 ||
			len(value) == 0 {
			return nil, fmt.Errorf("imported Secret has an unavailable value")
		}
		data[key] = append([]byte(nil), value...)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      migration.Name + "-import",
			Namespace: migration.Namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	if err := controllerutil.SetControllerReference(migration, secret, scheme); err != nil {
		return nil, fmt.Errorf("set imported Secret owner: %w", err)
	}
	if err := kube.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("imported Secret name already exists")
		}
		return nil, fmt.Errorf("create imported Secret: %w", err)
	}
	return secret, nil
}
