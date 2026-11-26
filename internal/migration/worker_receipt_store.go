package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type workerReceiptStore struct {
	kube      client.Client
	scheme    *runtime.Scheme
	operation *ha.HomeAssistantMigration
	request   ha.MigrationExecutionRequest
}

func (s *workerReceiptStore) name() string {
	return "migration-receipt-" + strings.TrimPrefix(s.request.RequestID, "sha256:")[:40]
}

// ReadWorkerBackupReceipt reads only the receipt for this accepted request.
// Callers must interpret accepted, blocked and missing evidence separately.
func ReadWorkerBackupReceipt(ctx context.Context, reader client.Reader, op *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest) (*BackupDispatchReceipt, error) {
	if ctx == nil || reader == nil || op == nil || request.Stage != "backup" || !migrationprotocol.ValidDigest(request.RequestID) || request.OperationUID != string(op.UID) {
		return nil, fmt.Errorf("worker receipt binding invalid")
	}
	store := &workerReceiptStore{operation: op, request: request}
	cm := &corev1.ConfigMap{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: store.name()}, cm); err != nil {
		return nil, fmt.Errorf("worker receipt unavailable")
	}
	owner := metav1.GetControllerOf(cm)
	var receipt BackupDispatchReceipt
	if owner == nil || owner.UID != op.UID || migrationprotocol.Decode([]byte(cm.Data[backupDispatchReceiptKey]), &receipt) != nil || receipt.RequestID != request.RequestID || receipt.OperationUID != request.OperationUID || receipt.TargetUID != request.Target.UID || receipt.ReportID != request.ReportID || receipt.ScopeDigest != request.ScopeDigest {
		return nil, fmt.Errorf("worker receipt binding invalid")
	}
	return &receipt, nil
}

func (s *workerReceiptStore) Claim(ctx context.Context, receipt BackupDispatchReceipt) (bool, error) {
	request, err := ReadWorkerRequest(ctx, s.kube, s.operation.Namespace, s.operation.Name, "backup", s.request.RequestID)
	if err != nil || request.OperationUID != string(s.operation.UID) || receipt.OperationUID != request.OperationUID || receipt.TargetUID != request.Target.UID || receipt.ReportID != request.ReportID || receipt.ScopeDigest != request.ScopeDigest || receipt.RequestID != request.RequestID || receipt.Phase != "Dispatching" || receipt.JobID != "" {
		return false, fmt.Errorf("worker receipt binding invalid")
	}
	current := &ha.HomeAssistantMigration{}
	// The controller writes migration status concurrently, so a plain Update can
	// conflict between Get and Update. Re-fetch and re-validate on conflict; the
	// dispatch marker is append-only, so a retry cannot create a second permit.
	claimed := false
	var invalid error
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.kube.Get(ctx, client.ObjectKeyFromObject(s.operation), current); err != nil {
			invalid = fmt.Errorf("worker operation unavailable")
			return nil
		}
		if current.UID != s.operation.UID || current.Spec.CancelRequested || migrationprotocol.AbortRequested(current) || !current.DeletionTimestamp.IsZero() || !workerSettingsStable(current, *request) {
			invalid = fmt.Errorf("worker operation unavailable")
			return nil
		}
		for _, marker := range current.Spec.ExecutionDispatches {
			if marker.Stage == "backup" {
				claimed = false
				return nil
			}
		}
		// Claim the only POST permit in append-only spec before writing a receipt.
		// A lost update response is ambiguous and can never authorize another POST.
		current.Spec.ExecutionDispatches = append(current.Spec.ExecutionDispatches, ha.MigrationExecutionAttempt{Stage: "backup", RequestID: request.RequestID})
		if err := s.kube.Update(ctx, current); err != nil {
			return err
		}
		claimed = true
		return nil
	}); err != nil {
		return false, fmt.Errorf("worker dispatch claim outcome unknown")
	}
	if invalid != nil {
		return false, invalid
	}
	if !claimed {
		return false, nil
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return false, fmt.Errorf("cannot encode receipt")
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: s.name(), Namespace: s.operation.Namespace}, Data: map[string]string{backupDispatchReceiptKey: string(data)}}
	if err := controllerutil.SetControllerReference(s.operation, cm, s.scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
		return false, fmt.Errorf("cannot set receipt owner")
	}
	if err := s.kube.Create(ctx, cm); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("cannot create receipt")
	}
	return true, nil
}

func (s *workerReceiptStore) SaveAccepted(ctx context.Context, receipt BackupDispatchReceipt) error {
	if receipt.RequestID != s.request.RequestID || receipt.OperationUID != string(s.operation.UID) || receipt.TargetUID != s.request.Target.UID || receipt.ReportID != s.request.ReportID || receipt.ScopeDigest != s.request.ScopeDigest || receipt.Phase != "Accepted" || !nativeBackupIDPattern.MatchString(receipt.JobID) {
		return fmt.Errorf("accepted receipt binding invalid")
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("cannot encode receipt")
	}
	retryable := func(err error) bool {
		return apierrors.IsConflict(err) || apierrors.IsServerTimeout(err) || apierrors.IsTimeout(err) ||
			apierrors.IsTooManyRequests(err) || apierrors.IsServiceUnavailable(err)
	}
	if err := retry.OnError(retry.DefaultBackoff, retryable, func() error {
		cm := &corev1.ConfigMap{}
		if err := s.kube.Get(ctx, client.ObjectKey{Namespace: s.operation.Namespace, Name: s.name()}, cm); err != nil {
			return err
		}
		owner := metav1.GetControllerOf(cm)
		var previous BackupDispatchReceipt
		if owner == nil || owner.UID != s.operation.UID || json.Unmarshal([]byte(cm.Data[backupDispatchReceiptKey]), &previous) != nil || previous.RequestID != receipt.RequestID || previous.OperationUID != receipt.OperationUID || previous.TargetUID != receipt.TargetUID || previous.ReportID != receipt.ReportID || previous.ScopeDigest != receipt.ScopeDigest {
			return fmt.Errorf("receipt binding invalid")
		}
		if previous.Phase == "Accepted" && previous.JobID == receipt.JobID {
			return nil
		}
		if previous.Phase != "Dispatching" || previous.JobID != "" {
			return fmt.Errorf("receipt binding invalid")
		}
		cm.Data[backupDispatchReceiptKey] = string(data)
		return s.kube.Update(ctx, cm)
	}); err != nil {
		return fmt.Errorf("cannot persist accepted receipt")
	}
	return nil
}
