package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

const (
	backupKeySecretDataKey       = "password"
	backupKeyInstanceUIDLabelKey = "ha.homeassistant.io/instance-uid"
	backupKeyPurposeLabelKey     = "ha.homeassistant.io/purpose"
	backupKeyHistoryPurpose      = "backup-key-history"
)

var (
	ErrBackupKeyEmpty         = errors.New("backup key is empty")
	ErrBackupKeyPlanMissing   = errors.New("backup key Secret name has not been preselected")
	ErrBackupKeyTargetInvalid = errors.New("backup key target has no UID")
)

// BackupKeySecretPlan is the durable part of a pending key archival. The
// caller must persist Name before calling Retain: after an uncertain Create,
// the same name is read back rather than allocating another Secret.
type BackupKeySecretPlan struct {
	Name string
}

// NewBackupKeySecretPlan selects a collision-resistant name. Persist the plan
// before calling Retain; this helper intentionally does not create a Secret.
func NewBackupKeySecretPlan() BackupKeySecretPlan {
	return BackupKeySecretPlan{Name: "backup-key-" + uuid.NewString()}
}

// BackupKeyHistory retains observed backup-key values. It deliberately has no
// delete operation: history Secrets require an explicit, separately authorised
// cleanup workflow.
type BackupKeyHistory struct {
	Client client.Client
}

// BackupKeySecretCleanupEligible makes the retention boundary explicit.
// Automatic reconciliation always passes false; a future, authorised cleanup
// path must both request cleanup explicitly and match the target UID.
func BackupKeySecretCleanupEligible(secret *corev1.Secret, targetUID types.UID, explicitlyRequested bool) bool {
	return explicitlyRequested && isRetainedBackupKeySecret(secret, targetUID)
}

// Retain stores password once for target's UID and returns an existing Secret
// when that value has already been observed. Password equality is performed
// only on Secret bytes held in memory; no derived value is written to metadata.
func (h BackupKeyHistory) Retain(
	ctx context.Context,
	target *hav1.HomeAssistant,
	password string,
	plan BackupKeySecretPlan,
) (*corev1.Secret, error) {
	if target == nil || target.UID == "" {
		return nil, ErrBackupKeyTargetInvalid
	}
	if password == "" {
		return nil, ErrBackupKeyEmpty
	}
	if plan.Name == "" {
		return nil, ErrBackupKeyPlanMissing
	}

	secrets := &corev1.SecretList{}
	if err := h.Client.List(ctx, secrets,
		client.InNamespace(target.Namespace),
		client.MatchingLabels{
			backupKeyInstanceUIDLabelKey: string(target.UID),
			backupKeyPurposeLabelKey:     backupKeyHistoryPurpose,
		},
	); err != nil {
		return nil, fmt.Errorf("list retained backup-key Secrets: %w", err)
	}
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		if retainedBackupKeySecretMatches(secret, target.UID, password) {
			return secret, nil
		}
	}

	desired := newRetainedBackupKeySecret(target, password, plan.Name)
	createErr := h.Client.Create(ctx, desired)
	if createErr == nil {
		return desired, nil
	}

	// A response can be lost after the API server accepted Create. Reconcile the
	// durable preselected name only; never create a second Secret for that case.
	existing := &corev1.Secret{}
	getErr := h.Client.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: plan.Name}, existing)
	if getErr == nil && retainedBackupKeySecretMatches(existing, target.UID, password) {
		return existing, nil
	}
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return nil, fmt.Errorf("read preselected backup-key Secret: %w", getErr)
	}
	return nil, fmt.Errorf("create retained backup-key Secret: %w", createErr)
}

func newRetainedBackupKeySecret(target *hav1.HomeAssistant, password, name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: target.Namespace,
			Labels: map[string]string{
				backupKeyInstanceUIDLabelKey: string(target.UID),
				backupKeyPurposeLabelKey:     backupKeyHistoryPurpose,
			},
		},
		Immutable: ptr.To(true),
		Type:      corev1.SecretTypeOpaque,
		Data:      map[string][]byte{backupKeySecretDataKey: []byte(password)},
	}
}

func retainedBackupKeySecretMatches(secret *corev1.Secret, targetUID types.UID, password string) bool {
	return isRetainedBackupKeySecret(secret, targetUID) &&
		bytes.Equal(secret.Data[backupKeySecretDataKey], []byte(password))
}

func isRetainedBackupKeySecret(secret *corev1.Secret, targetUID types.UID) bool {
	return secret != nil && secret.Immutable != nil && *secret.Immutable &&
		secret.Type == corev1.SecretTypeOpaque &&
		len(secret.OwnerReferences) == 0 &&
		secret.Labels[backupKeyInstanceUIDLabelKey] == string(targetUID) &&
		secret.Labels[backupKeyPurposeLabelKey] == backupKeyHistoryPurpose
}
