package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const backupDispatchReceiptKey = "receipt.json"

// BackupDispatchReceiptName is the Kubernetes object used to claim a dispatch.
func BackupDispatchReceiptName(operationUID string) string {
	digest := sha256.Sum256([]byte(operationUID))
	return "migration-backup-" + hex.EncodeToString(digest[:16])
}

// ConfigMapBackupDispatchStore persists a single non-idempotent dispatch claim.
// Client must use direct API reads rather than a stale informer cache.
type ConfigMapBackupDispatchStore struct {
	Client    client.Client
	Scheme    *runtime.Scheme
	Operation *hav1alpha1.HomeAssistantMigration
}

func (s *ConfigMapBackupDispatchStore) Claim(ctx context.Context, receipt BackupDispatchReceipt) (bool, error) {
	if s.Client == nil || s.Scheme == nil || s.Operation == nil || s.Operation.UID == "" ||
		receipt.OperationUID != string(s.Operation.UID) || receipt.Phase != "Dispatching" || receipt.JobID != "" {
		return false, fmt.Errorf("invalid dispatch claim")
	}
	current := &hav1alpha1.HomeAssistantMigration{}
	if err := s.Client.Get(ctx, client.ObjectKeyFromObject(s.Operation), current); err != nil {
		return false, fmt.Errorf("read backup approval")
	}
	if current.UID != s.Operation.UID || !current.DeletionTimestamp.IsZero() ||
		current.Spec.Target.UID != receipt.TargetUID || current.Spec.ReportID != receipt.ReportID ||
		current.Spec.BackupScopeDigest != receipt.ScopeDigest || !dispatchBackupApproved(current, receipt) {
		return false, fmt.Errorf("backup approval changed")
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return false, fmt.Errorf("encode dispatch claim")
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: BackupDispatchReceiptName(string(s.Operation.UID)), Namespace: s.Operation.Namespace},
		Data: map[string]string{backupDispatchReceiptKey: string(data)}}
	if err := controllerutil.SetControllerReference(s.Operation, cm, s.Scheme); err != nil {
		return false, fmt.Errorf("set dispatch claim owner")
	}
	if err := s.Client.Create(ctx, cm); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("create dispatch claim")
	}
	return true, nil
}

func dispatchBackupApproved(operation *hav1alpha1.HomeAssistantMigration, receipt BackupDispatchReceipt) bool {
	count := 0
	approved := false
	for _, approval := range operation.Spec.Approvals {
		if approval.ID != "backup" {
			continue
		}
		count++
		approved = approval.Approved && approval.Digest == receipt.ReportID &&
			approval.ScopeDigest == receipt.ScopeDigest && approval.TargetUID == receipt.TargetUID
	}
	return count == 1 && approved
}

func (s *ConfigMapBackupDispatchStore) SaveAccepted(ctx context.Context, receipt BackupDispatchReceipt) error {
	if s.Client == nil || s.Operation == nil || s.Operation.UID == "" || receipt.OperationUID != string(s.Operation.UID) ||
		receipt.Phase != "Accepted" || !nativeBackupIDPattern.MatchString(receipt.JobID) {
		return fmt.Errorf("invalid accepted receipt")
	}
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: s.Operation.Namespace, Name: BackupDispatchReceiptName(string(s.Operation.UID))}
	if err := s.Client.Get(ctx, key, cm); err != nil {
		return fmt.Errorf("read dispatch claim")
	}
	owner := metav1.GetControllerOf(cm)
	var claimed BackupDispatchReceipt
	if owner == nil || owner.UID != s.Operation.UID || json.Unmarshal([]byte(cm.Data[backupDispatchReceiptKey]), &claimed) != nil ||
		claimed.OperationUID != receipt.OperationUID || claimed.TargetUID != receipt.TargetUID ||
		claimed.ReportID != receipt.ReportID || claimed.ScopeDigest != receipt.ScopeDigest ||
		claimed.Phase != "Dispatching" || claimed.JobID != "" {
		return fmt.Errorf("dispatch claim does not match")
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode accepted receipt")
	}
	cm.Data[backupDispatchReceiptKey] = string(data)
	if err := s.Client.Update(ctx, cm); err != nil {
		return fmt.Errorf("persist accepted receipt")
	}
	return nil
}
