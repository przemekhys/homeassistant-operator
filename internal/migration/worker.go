package migration

import (
	"context"
	"errors"
	"fmt"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadWorkerRequest uses a direct API reader and does not permit an unmarked,
// cancelled, foreign, or malformed request to execute.
func ReadWorkerRequest(ctx context.Context, reader client.Reader, namespace, name, stage, requestID string) (*ha.MigrationExecutionRequest, error) {
	if ctx == nil || reader == nil || namespace == "" || name == "" || !migrationprotocol.ValidDigest(requestID) || !migrationprotocol.ValidStage(stage) {
		return nil, fmt.Errorf("invalid worker request")
	}
	op := &ha.HomeAssistantMigration{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, op); err != nil {
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("worker operation permission denied")
		}
		return nil, fmt.Errorf("worker operation API unavailable")
	}
	if op.UID == "" || op.Spec.ExecutionProtocol != "v1" || op.Spec.CancelRequested || migrationprotocol.AbortRequested(op) ||
		!op.DeletionTimestamp.IsZero() || migrationprotocol.ValidateAccepted(op) != nil {
		return nil, fmt.Errorf("worker operation unavailable or invalid")
	}
	for _, request := range op.Spec.ExecutionRequests {
		if request.Stage != stage || request.RequestID != requestID {
			continue
		}
		for _, attempt := range op.Spec.ExecutionAttempts {
			if attempt.Stage == stage && attempt.RequestID == requestID {
				copy := request
				return &copy, nil
			}
		}
	}
	return nil, fmt.Errorf("worker request is not accepted and attempted")
}

// RunBackupWorker performs a backup submission using a create-only receipt.
// It must not be wired into a runnable worker until durable attempt ownership
// prevents a new sender after receipt or Job loss. It does not establish readiness.
type BackupWorkerSource interface {
	BackupDispatcher
	ReadMigrationVerification(context.Context, string, string, string) ([]byte, error)
	GetConfig(context.Context, string) (*haclient.ConfigResponse, error)
}

type backupProfileReader interface {
	GetConfig(context.Context, string) (*haclient.ConfigResponse, error)
}

func RunBackupWorker(ctx context.Context, kube client.Client, scheme *runtime.Scheme, namespace, name, requestID string, source BackupWorkerSource) error {
	if kube == nil || scheme == nil || source == nil {
		return fmt.Errorf("backup worker dependencies unavailable")
	}
	request, err := ReadWorkerRequest(ctx, kube, namespace, name, "backup", requestID)
	if err != nil {
		return err
	}
	token, err := ReadPinnedCredential(ctx, kube, namespace, request.SourceToken)
	if err != nil {
		return err
	}
	key, err := ReadPinnedCredential(ctx, kube, namespace, request.RecoveryKey)
	if err != nil {
		return err
	}
	op := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, op); err != nil || string(op.UID) != request.OperationUID {
		return fmt.Errorf("worker operation unavailable or changed")
	}
	if !workerSettingsStable(op, *request) {
		return fmt.Errorf("backup settings stability not confirmed")
	}
	if err := observeWorkerProfile(ctx, source, string(token)); err != nil {
		return err
	}
	for _, item := range op.Spec.Verification {
		projection, err := source.ReadMigrationVerification(ctx, string(token), item.Kind, item.ID)
		if err != nil {
			return fmt.Errorf("source verification unavailable")
		}
		digest, err := migrationprotocol.ProjectionDigest(item.Kind, item.ID, projection)
		if err != nil || digest != item.ExpectedDigest {
			return fmt.Errorf("source verification mismatch")
		}
	}
	store := &workerReceiptStore{kube: kube, scheme: scheme, operation: op, request: *request}
	receipt := BackupDispatchReceipt{RequestID: request.RequestID, OperationUID: request.OperationUID, TargetUID: request.Target.UID, ReportID: request.ReportID, ScopeDigest: request.ScopeDigest}
	if _, err := DispatchBackupOnce(ctx, store, source, string(token), key, receipt); err != nil {
		return fmt.Errorf("backup worker did not complete dispatch: %w", err)
	}
	return nil
}

// A new Pod can precede CNI policy convergence. Only this read-only preflight
// is retried, before claiming a dispatch permit; POST is never retried.
func observeWorkerProfile(ctx context.Context, source backupProfileReader, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var last error
	for {
		config, err := source.GetConfig(ctx, token)
		if err == nil {
			if config == nil || config.Version != "2026.11.0" {
				return fmt.Errorf("UnsupportedArchiveProfile")
			}
			return nil
		}
		last = fmt.Errorf("source profile observation unavailable")
		var response *haclient.Error
		if errors.As(err, &response) {
			last = fmt.Errorf("source profile observation unavailable (%s, HTTP %d)", response.Type, response.StatusCode)
			if response.Type == haclient.ErrorTypeInvalidResponse {
				return last
			}
			if response.StatusCode == 401 || response.StatusCode == 403 {
				return last
			}
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(time.Second):
		}
	}
}

func workerSettingsStable(op *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest) bool {
	if op == nil || migrationprotocol.ValidateApprovals(op) != nil {
		return false
	}
	for _, a := range op.Spec.Approvals {
		if a.ID == "backup-settings-stable" {
			return a.Approved && a.Digest == request.ReportID && a.ScopeDigest == request.ScopeDigest && a.TargetUID == request.Target.UID
		}
	}
	return false
}

// ReadPinnedCredential returns bytes from the same directly read Secret whose
// immutable identity was checked. It never serializes the credential in errors.
func ReadPinnedCredential(ctx context.Context, reader client.Reader, namespace string, identity *ha.MigrationSecretIdentity) ([]byte, error) {
	if ctx == nil || reader == nil || namespace == "" || identity == nil {
		return nil, fmt.Errorf("CredentialVersionChanged")
	}
	secret := &corev1.Secret{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: identity.Name}, secret); err != nil || migrationprotocol.ValidateCredentialVersion(identity, secret) != nil {
		return nil, fmt.Errorf("CredentialVersionChanged")
	}
	return append([]byte(nil), secret.Data[identity.Key]...), nil
}
