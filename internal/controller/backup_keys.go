package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

const backupKeyUIDLabel = "ha.homeassistant.io/backup-key-owner-uid"

// Kubernetes limits a Secret payload to 1 MiB. Reserve no hidden capacity:
// refusing an entry before Update preserves every already retained key.
const maxBackupKeySecretBytes = 1 << 20

func backupKeySecretName(ha *hav1.HomeAssistant) string {
	name := strings.ToLower(ha.Name)
	if len(name) > 35 {
		name = name[:35]
	}
	uid := string(ha.UID)
	if len(uid) > 12 {
		uid = uid[:12]
	}
	return fmt.Sprintf("%s-backup-keys-%s", strings.Trim(name, "-"), uid)
}

// protectBackupKey appends a newly observed value without ever mutating HA or
// deleting previous recovery values. It intentionally has no owner reference:
// the retained key must survive deletion of the HomeAssistant instance.
func (r *HomeAssistantReconciler) protectBackupKey(
	ctx context.Context, ha *hav1.HomeAssistant, value *string,
) (*hav1.BackupSecretKeyReference, error) {
	if value == nil || *value == "" {
		return nil, nil
	}
	name := backupKeySecretName(ha)
	key := types.NamespacedName{Namespace: ha.Namespace, Name: name}
	secret := &corev1.Secret{}
	err := r.Get(ctx, key, secret)
	if apierrors.IsNotFound(err) {
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ha.Namespace,
				Labels: map[string]string{backupKeyUIDLabel: string(ha.UID)},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{},
		}
		if err := r.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		if err := r.Get(ctx, key, secret); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if secret.Labels[backupKeyUIDLabel] != string(ha.UID) {
		return nil, fmt.Errorf("conflicting backup key Secret")
	}
	for attempt := 0; attempt < 3; attempt++ {
		if secret.Labels[backupKeyUIDLabel] != string(ha.UID) {
			return nil, fmt.Errorf("conflicting backup key Secret")
		}
		for existingKey, existingValue := range secret.Data {
			if string(existingValue) == *value {
				return &hav1.BackupSecretKeyReference{Name: name, Key: existingKey}, nil
			}
		}
		entry := nextBackupKeyEntry(secret.Data)
		if backupSecretDataSize(secret.Data)+len(entry)+len(*value) > maxBackupKeySecretBytes {
			return nil, fmt.Errorf("backup key Secret size limit reached")
		}
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}
		secret.Data[entry] = []byte(*value)
		if err := r.Update(ctx, secret); err == nil {
			return &hav1.BackupSecretKeyReference{Name: name, Key: entry}, nil
		} else if !apierrors.IsConflict(err) {
			return nil, err
		}
		if err := r.Get(ctx, key, secret); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("backup key Secret update conflicted")
}

func nextBackupKeyEntry(data map[string][]byte) string {
	for n := 1; ; n++ {
		entry := fmt.Sprintf("key-%06d", n)
		if _, found := data[entry]; !found {
			return entry
		}
	}
}

func backupSecretDataSize(data map[string][]byte) int {
	total := 0
	for key, value := range data {
		total += len(key) + len(value)
	}
	return total
}
