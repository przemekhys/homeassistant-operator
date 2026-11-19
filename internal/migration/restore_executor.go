package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const onboardingReceiptKey = "receipt.json"

// OnboardingReceipt is durable evidence of one request transmission. A receipt
// is never success evidence by itself; specifically, Accepted restore remains
// pending technical verification of target access and data consistency.
type OnboardingReceipt struct {
	Version         string `json:"version"`
	OperationUID    string `json:"operationUID"`
	RequestID       string `json:"requestID"`
	Stage           string `json:"stage"`
	UploadRequestID string `json:"uploadRequestID,omitempty"`
	ArchiveDigest   string `json:"archiveDigest"`
	BackupID        string `json:"backupID"`
	AgentID         string `json:"agentID"`
	ScopeDigest     string `json:"scopeDigest"`
	Phase           string `json:"phase"`
}

func OnboardingReceiptName(requestID string) string {
	if !migrationprotocol.ValidDigest(requestID) {
		return ""
	}
	return "migration-onboarding-" + strings.TrimPrefix(requestID, "sha256:")[:40]
}

// ReadOnboardingReceipt returns the durable receipt for an accepted upload or
// restore request. Absence is not evidence that no POST happened for a request
// whose attempt marker exists; callers must treat it as unknown.
func ReadOnboardingReceipt(ctx context.Context, reader client.Reader, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) (*OnboardingReceipt, error) {
	if reader == nil || op == nil || op.UID == "" || (r.Stage != "upload" && r.Stage != "restore") || r.OperationUID != string(op.UID) {
		return nil, fmt.Errorf("onboarding receipt binding unavailable")
	}
	e := &RestoreExecutor{Client: nil, Operation: op}
	cm := &corev1.ConfigMap{}
	name := OnboardingReceiptName(r.RequestID)
	if name == "" || reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: name}, cm) != nil {
		return nil, fmt.Errorf("onboarding receipt unavailable")
	}
	receipt, err := e.decode(cm, r.RequestID)
	if err != nil || receipt.Stage != r.Stage || receipt.ArchiveDigest != r.ArchiveDigest || receipt.ScopeDigest != r.ScopeDigest || receipt.UploadRequestID != r.UploadRequestID {
		return nil, fmt.Errorf("onboarding receipt binding invalid")
	}
	return receipt, nil
}

type OnboardingClient interface {
	UploadMigrationBackup(context.Context, io.Reader, string, int64, string) error
	MigrationOnboardingBackupInfo(context.Context, haclient.MigrationBackupExpectation) (*haclient.MigrationOnboardingBackupEvidence, error)
	RestoreMigrationOnboardingBackup(context.Context, string, string, string, bool) error
}

// RestoreExecutor intentionally has no retry loop. An existing receipt means a
// POST may have happened and is returned to the observer rather than resent.
type RestoreExecutor struct {
	Client     client.Client
	Scheme     *runtime.Scheme
	Operation  *ha.HomeAssistantMigration
	Onboarding OnboardingClient
}

// RunOnboardingWorker re-reads every Kubernetes binding immediately before a
// single upload or restore POST. It never derives restore success from exit.
func RunOnboardingWorker(ctx context.Context, kube client.Client, scheme *runtime.Scheme, namespace, name, stage, requestID string, onboarding OnboardingClient) error {
	if kube == nil || scheme == nil || onboarding == nil || (stage != "upload" && stage != "restore") {
		return fmt.Errorf("onboarding worker dependencies unavailable")
	}
	request, err := ReadWorkerRequest(ctx, kube, namespace, name, stage, requestID)
	if err != nil {
		return err
	}
	op := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, op); err != nil || string(op.UID) != request.OperationUID {
		return fmt.Errorf("worker operation unavailable or changed")
	}
	if op.Spec.ArchiveStorage == nil || request.ArchiveRef == nil {
		return fmt.Errorf("archive binding unavailable")
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: op.Spec.ArchiveStorage.PVCName}, pvc); err != nil || string(pvc.UID) != request.ArchivePVCUID {
		return fmt.Errorf("archive PVC unavailable or changed")
	}
	archive, err := ReadArchiveDescriptor(ctx, kube, op, pvc, "/archive", request.ArchiveRef.Name, request.ArchiveRef.UID)
	if err != nil {
		return fmt.Errorf("verified archive unavailable")
	}
	executor := &RestoreExecutor{Client: kube, Scheme: scheme, Operation: op, Onboarding: onboarding}
	if stage == "upload" {
		file, err := os.Open(filepath.Join("/archive", archive.Filename))
		if err != nil {
			return fmt.Errorf("archive unavailable")
		}
		defer func() { _ = file.Close() }()
		_, err = executor.Upload(ctx, *request, *archive, file, archive.DatabaseIncluded)
		return err
	}
	key, err := ReadPinnedCredential(ctx, kube, namespace, request.RecoveryKey)
	if err != nil {
		return err
	}
	_, err = executor.Restore(ctx, *request, *archive, string(key), archive.DatabaseIncluded)
	return err
}

func (e *RestoreExecutor) Upload(ctx context.Context, request ha.MigrationExecutionRequest, archive ArchiveDescriptor, source io.Reader, restoreDatabase bool) (*OnboardingReceipt, error) {
	if err := e.validRequest(request, "upload"); err != nil || source == nil || restoreDatabase != archive.DatabaseIncluded || !e.archiveBound(request, archive) {
		return nil, fmt.Errorf("upload inputs are not bound to verified archive")
	}
	receipt := OnboardingReceipt{Version: "v1", OperationUID: string(e.Operation.UID), RequestID: request.RequestID, Stage: "upload", ArchiveDigest: archive.Digest, BackupID: archive.BackupID, AgentID: archive.AgentID, ScopeDigest: request.ScopeDigest, Phase: "Dispatching"}
	claimed, existing, err := e.claim(ctx, receipt)
	if err != nil || !claimed {
		return existing, err
	}
	if err := e.Onboarding.UploadMigrationBackup(ctx, source, archive.Filename, archive.Size, archive.AgentID); err != nil {
		return e.saveOutcome(ctx, receipt, err)
	}
	evidence, err := e.Onboarding.MigrationOnboardingBackupInfo(ctx, haclient.MigrationBackupExpectation{BackupID: archive.BackupID, AgentID: archive.AgentID, Size: archive.Size, RestoreDatabase: restoreDatabase})
	if err != nil {
		return e.saveOutcome(ctx, receipt, err)
	}
	if evidence.BackupID != archive.BackupID || evidence.AgentID != archive.AgentID || evidence.Size != archive.Size || !evidence.Protected || evidence.DatabaseIncluded != restoreDatabase || !evidence.HomeAssistantIncluded {
		return e.save(ctx, receipt, "EvidenceMismatch")
	}
	return e.save(ctx, receipt, "Verified")
}

// Restore sends one restore POST only after an immutable, verified upload
// receipt. HTTP success records Accepted, not successful restore.
func (e *RestoreExecutor) Restore(ctx context.Context, request ha.MigrationExecutionRequest, archive ArchiveDescriptor, recoveryKey string, restoreDatabase bool) (*OnboardingReceipt, error) {
	if err := e.validRequest(request, "restore"); err != nil || recoveryKey == "" || request.UploadRequestID == "" || request.RecoveryKey == nil || restoreDatabase != archive.DatabaseIncluded || !e.archiveBound(request, archive) || *request.RecoveryKey != archive.Validation.KeyRef {
		return nil, fmt.Errorf("restore inputs are not bound to verified archive")
	}
	upload, err := e.read(ctx, request.UploadRequestID)
	if err != nil || upload.Stage != "upload" || upload.Phase != "Verified" || upload.ArchiveDigest != archive.Digest || upload.ScopeDigest != request.ScopeDigest || upload.BackupID != archive.BackupID || upload.AgentID != archive.AgentID {
		return nil, fmt.Errorf("verified upload receipt unavailable")
	}
	receipt := OnboardingReceipt{Version: "v1", OperationUID: string(e.Operation.UID), RequestID: request.RequestID, Stage: "restore", UploadRequestID: request.UploadRequestID, ArchiveDigest: archive.Digest, BackupID: archive.BackupID, AgentID: archive.AgentID, ScopeDigest: request.ScopeDigest, Phase: "Dispatching"}
	claimed, existing, err := e.claim(ctx, receipt)
	if err != nil || !claimed {
		return existing, err
	}
	if err := e.Onboarding.RestoreMigrationOnboardingBackup(ctx, archive.BackupID, archive.AgentID, recoveryKey, restoreDatabase); err != nil {
		return e.saveOutcome(ctx, receipt, err)
	}
	return e.save(ctx, receipt, "Accepted")
}

// archiveBound ties the descriptor to the accepted backup request that produced
// it. The descriptor carries the backup request ID and backup scope; the
// upload/restore request carries its own restore scope and the archive identity.
func (e *RestoreExecutor) archiveBound(request ha.MigrationExecutionRequest, archive ArchiveDescriptor) bool {
	if !validatedArchiveForRestore(archive) || !archive.Protected || archive.Size <= 0 || request.ArchiveRef == nil ||
		archive.Digest != request.ArchiveDigest || archive.PVCUID != request.ArchivePVCUID ||
		archive.OperationUID != string(e.Operation.UID) || archive.SourceID != request.SourceID {
		return false
	}
	// The administrator approved this exact native backup and agent.
	approved := e.Operation.Spec.Archive
	if approved == nil || approved.BackupID != archive.BackupID || approved.AgentID != archive.AgentID || approved.Digest != archive.Digest {
		return false
	}
	for _, accepted := range e.Operation.Spec.ExecutionRequests {
		if accepted.Stage == "backup" && accepted.RequestID == archive.RequestID && accepted.ScopeDigest == archive.ScopeDigest {
			return true
		}
	}
	return false
}

func validatedArchiveForRestore(archive ArchiveDescriptor) bool {
	v := archive.Validation
	return v != nil && v.Profile == "ha-container-2026.11.0-securetar-v3" && v.ValidatorVersion != "" && v.CiphertextDigest == archive.Digest && migrationprotocol.ValidDigest(v.SourceEvidenceDigest) && v.PayloadAuthenticated && v.StructureValid && v.KeyRef.Name != "" && v.KeyRef.Key != "" && v.KeyRef.UID != "" && v.KeyRef.ResourceVersion != ""
}

func (e *RestoreExecutor) validRequest(r ha.MigrationExecutionRequest, stage string) error {
	if e == nil || e.Client == nil || e.Scheme == nil || e.Operation == nil || e.Operation.UID == "" || e.Onboarding == nil || r.Stage != stage || r.OperationUID != string(e.Operation.UID) || migrationprotocol.ValidateRequest(r) != nil || e.Operation.Spec.CancelRequested || !e.Operation.DeletionTimestamp.IsZero() || migrationprotocol.AbortRequested(e.Operation) {
		return fmt.Errorf("onboarding executor unavailable")
	}
	for _, accepted := range e.Operation.Spec.ExecutionRequests {
		if reflect.DeepEqual(accepted, r) {
			return nil
		}
	}
	return fmt.Errorf("onboarding request is not accepted")
}

// claimDispatch spends the operation's one POST permit for this stage before a
// receipt exists. spec.executionDispatches is append-only through the CRD
// schema and CEL transition rules, so its presence forbids a second submission
// even after the receipt ConfigMap or the worker Job has been lost, and an
// uncertain write here is reported as ambiguous instead of as permission.
func (e *RestoreExecutor) claimDispatch(ctx context.Context, r OnboardingReceipt) (bool, *OnboardingReceipt, error) {
	if e == nil || e.Client == nil || e.Operation == nil || (r.Stage != "upload" && r.Stage != "restore") {
		return false, nil, fmt.Errorf("onboarding dispatch claim unavailable")
	}
	current := &ha.HomeAssistantMigration{}
	if err := e.Client.Get(ctx, client.ObjectKey{Namespace: e.Operation.Namespace, Name: e.Operation.Name}, current); err != nil ||
		string(current.UID) != r.OperationUID || current.Spec.ExecutionProtocol != "v1" ||
		current.Spec.CancelRequested || !current.DeletionTimestamp.IsZero() || migrationprotocol.AbortRequested(current) {
		return false, nil, fmt.Errorf("onboarding dispatch claim unavailable")
	}
	attempted, dispatched := false, false
	for _, marker := range current.Spec.ExecutionAttempts {
		if marker.Stage == r.Stage && marker.RequestID == r.RequestID {
			attempted = true
		}
	}
	for _, marker := range current.Spec.ExecutionDispatches {
		if marker.Stage == r.Stage {
			dispatched = true
		}
	}
	if dispatched {
		// The permit is already spent. Only the recorded outcome may be read
		// back; a missing receipt stays ambiguous rather than becoming a new
		// permission to POST after a worker restart.
		existing, err := e.read(ctx, r.RequestID)
		if err != nil {
			return false, nil, fmt.Errorf("onboarding dispatch already claimed")
		}
		return false, existing, nil
	}
	// A dispatch marker must answer to the controller's attempt marker; the
	// CRD rejects it otherwise, so it is refused here before any write.
	if !attempted {
		return false, nil, fmt.Errorf("onboarding attempt marker unavailable")
	}
	updated := current.DeepCopy()
	updated.Spec.ExecutionDispatches = append(updated.Spec.ExecutionDispatches, ha.MigrationExecutionAttempt{Stage: r.Stage, RequestID: r.RequestID})
	if err := e.Client.Update(ctx, updated); err != nil {
		return false, nil, fmt.Errorf("onboarding dispatch claim outcome unknown")
	}
	return true, nil, nil
}

func (e *RestoreExecutor) claim(ctx context.Context, r OnboardingReceipt) (bool, *OnboardingReceipt, error) {
	permitted, existing, err := e.claimDispatch(ctx, r)
	if err != nil || !permitted {
		return permitted, existing, err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: OnboardingReceiptName(r.RequestID), Namespace: e.Operation.Namespace}, Data: map[string]string{}}
	b, _ := json.Marshal(r)
	cm.Data[onboardingReceiptKey] = string(b)
	if err := controllerutil.SetControllerReference(e.Operation, cm, e.Scheme); err != nil {
		return false, nil, fmt.Errorf("set onboarding receipt owner")
	}
	if err := e.Client.Create(ctx, cm); err == nil {
		return true, &r, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return false, nil, fmt.Errorf("onboarding receipt create outcome unknown")
	}
	existing, err = e.read(ctx, r.RequestID)
	if err != nil {
		return false, nil, err
	}
	if existing.OperationUID != r.OperationUID || existing.RequestID != r.RequestID || existing.Stage != r.Stage || existing.ArchiveDigest != r.ArchiveDigest || existing.ScopeDigest != r.ScopeDigest {
		return false, nil, fmt.Errorf("onboarding receipt conflicts")
	}
	return false, existing, nil
}
func (e *RestoreExecutor) read(ctx context.Context, id string) (*OnboardingReceipt, error) {
	cm := &corev1.ConfigMap{}
	if OnboardingReceiptName(id) == "" || e.Client.Get(ctx, client.ObjectKey{Namespace: e.Operation.Namespace, Name: OnboardingReceiptName(id)}, cm) != nil {
		return nil, fmt.Errorf("onboarding receipt unavailable")
	}
	return e.decode(cm, id)
}

func (e *RestoreExecutor) decode(cm *corev1.ConfigMap, id string) (*OnboardingReceipt, error) {
	owner := metav1.GetControllerOf(cm)
	var r OnboardingReceipt
	if owner == nil || owner.UID != e.Operation.UID || json.Unmarshal([]byte(cm.Data[onboardingReceiptKey]), &r) != nil || r.Version != "v1" || r.OperationUID != string(e.Operation.UID) || r.RequestID != id || (r.Stage != "upload" && r.Stage != "restore") {
		return nil, fmt.Errorf("onboarding receipt invalid")
	}
	return &r, nil
}
func (e *RestoreExecutor) save(ctx context.Context, r OnboardingReceipt, phase string) (*OnboardingReceipt, error) {
	if phase != "Verified" && phase != "Accepted" && phase != "EvidenceMismatch" && phase != "OutcomeUnknown" && phase != "Rejected" {
		return nil, fmt.Errorf("invalid onboarding receipt phase")
	}
	cm := &corev1.ConfigMap{}
	if e.Client.Get(ctx, client.ObjectKey{Namespace: e.Operation.Namespace, Name: OnboardingReceiptName(r.RequestID)}, cm) != nil {
		return nil, fmt.Errorf("onboarding receipt unavailable")
	}
	current, err := e.read(ctx, r.RequestID)
	if err != nil || current.Phase != "Dispatching" {
		return current, fmt.Errorf("onboarding receipt cannot be updated")
	}
	r.Phase = phase
	b, _ := json.Marshal(r)
	cm.Data[onboardingReceiptKey] = string(b)
	if err := e.Client.Update(ctx, cm); err != nil {
		return nil, fmt.Errorf("onboarding receipt outcome unknown")
	}
	return &r, nil
}
func (e *RestoreExecutor) saveOutcome(ctx context.Context, r OnboardingReceipt, callErr error) (*OnboardingReceipt, error) {
	phase := "Rejected"
	if e, ok := callErr.(*haclient.Error); ok && e.Type == haclient.ErrorTypeOutcomeUnknown {
		phase = "OutcomeUnknown"
	}
	saved, err := e.save(ctx, r, phase)
	if err != nil {
		return saved, err
	}
	return saved, callErr
}
