package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type backupArchiveObservation struct {
	kube    client.Client
	source  backupObservationReader
	op      *ha.HomeAssistantMigration
	request ha.MigrationExecutionRequest
	pvc     *corev1.PersistentVolumeClaim
	receipt *BackupDispatchReceipt
	scheme  *runtime.Scheme
	key     []byte
	dir     string
	poll    time.Duration
}

// observeDispatchedBackup has no dispatch capability in its source interface.
// Persistent source evidence is published before opening a download stream.
func observeDispatchedBackup(
	ctx context.Context, kube client.Client, source backupObservationReader,
	namespace, name, requestID, dir string, poll time.Duration,
) error {
	if ctx == nil || kube == nil || source == nil || poll <= 0 {
		return fmt.Errorf("backup observation unavailable")
	}
	r, err := ReadWorkerRequest(ctx, kube, namespace, name, "backup", requestID)
	if err != nil {
		return err
	}
	op := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, op); err != nil {
		return fmt.Errorf("operation unavailable")
	}
	if string(op.UID) != r.OperationUID || r.ArchiveStorage == nil {
		return fmt.Errorf("operation unavailable")
	}
	if !acceptedBackupDispatched(op, *r) {
		return fmt.Errorf("backup outcome unknown")
	}
	receipt, err := observedBackupReceipt(ctx, kube, op, *r)
	if err != nil || receipt.Phase != "Accepted" || !nativeBackupIDPattern.MatchString(receipt.JobID) {
		return fmt.Errorf("backup outcome unknown")
	}
	if isTerminalObservationReason(receipt.BlockedReason) {
		return fmt.Errorf("%s", receipt.BlockedReason)
	}
	pvc := &corev1.PersistentVolumeClaim{}
	pvcKey := client.ObjectKey{Namespace: namespace, Name: r.ArchiveStorage.PVCName}
	if err := kube.Get(ctx, pvcKey, pvc); err != nil || pvc.UID == "" {
		return fmt.Errorf("archive volume unavailable")
	}
	owner := metav1.GetControllerOf(pvc)
	if owner == nil || owner.UID != op.UID {
		return fmt.Errorf("ArchiveProvenanceUnavailable")
	}
	key, err := ReadPinnedCredential(ctx, kube, namespace, r.RecoveryKey)
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := ha.AddToScheme(scheme); err != nil {
		return err
	}
	o := &backupArchiveObservation{
		kube: kube, source: source, op: op, request: *r, pvc: pvc, receipt: receipt,
		scheme: scheme, key: key, dir: dir, poll: poll,
	}
	e, err := o.sourceEvidence(ctx)
	if err != nil {
		return err
	}
	if validateSourceEvidence(*e, op, *r, pvc, receipt) != nil {
		return fmt.Errorf("ArchiveProvenanceUnavailable")
	}
	if err := o.ensureFile(ctx, *e); err != nil {
		return err
	}
	return o.publishValidatedFile(ctx, *e)
}

func (o *backupArchiveObservation) sourceEvidence(ctx context.Context) (*BackupSourceEvidence, error) {
	e, err := readSourceEvidence(ctx, o.kube, o.op, o.request)
	if err == nil {
		return e, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("ArchiveProvenanceUnavailable")
	}
	// A final filename without earlier source evidence is never adopted, even
	// if the source currently offers similar metadata or a matching recovery key.
	if _, err := os.Lstat(filepath.Join(o.dir, "backup.tar")); !os.IsNotExist(err) {
		return nil, fmt.Errorf("ArchiveProvenanceUnavailable")
	}
	if !workerSettingsStable(o.op, o.request) {
		return nil, fmt.Errorf("BackupSettingsChanged")
	}
	token, err := ReadPinnedCredential(ctx, o.kube, o.op.Namespace, o.request.SourceToken)
	if err != nil {
		return nil, err
	}
	if err := observeWorkerProfile(ctx, o.source, string(token)); err != nil {
		return nil, err
	}
	if VerifyBackupScopeAndKey(ctx, o.source, string(token), o.request.ScopeDigest, o.key) != nil {
		return nil, fmt.Errorf("BackupSettingsChanged")
	}
	preflight := observeBackupPreflight(ctx, o.source, string(token))
	for {
		if _, err := ReadWorkerRequest(ctx, o.kube, o.op.Namespace, o.op.Name, "backup", o.request.RequestID); err != nil {
			return nil, err
		}
		details, err := o.source.GetBackupDetails(ctx, string(token), o.receipt.JobID)
		if err == nil && details != nil {
			if details.Backup.ID != o.receipt.JobID {
				return nil, fmt.Errorf("BackupResultMismatch")
			}
			candidate, err := finalBackupEvidence(details, preflight, o.op, o.request, o.pvc, o.receipt)
			if err == nil {
				if o.source.CanDecryptBackup(ctx, string(token), candidate.BackupID, candidate.AgentID, string(o.key)) != nil {
					return nil, fmt.Errorf("BackupResultMismatch")
				}
				_, err := createImmutableEvidence(
					ctx, o.kube, o.scheme, o.op, sourceEvidenceName(o.request.RequestID), "evidence.json", candidate,
				)
				return &candidate, err
			}
			// A populated final result that fails the profile is terminal.
			for _, a := range details.Backup.Agents {
				if a.Size > 0 {
					return nil, fmt.Errorf("BackupResultMismatch")
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("backup result unavailable")
		case <-time.After(o.poll):
		}
	}
}

func (o *backupArchiveObservation) ensureFile(ctx context.Context, e BackupSourceEvidence) error {
	_, err := os.Lstat(filepath.Join(o.dir, "backup.tar"))
	if err == nil {
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("archive storage unavailable")
	}
	token, err := ReadPinnedCredential(ctx, o.kube, o.op.Namespace, o.request.SourceToken)
	if err != nil {
		return err
	}
	if err := observeWorkerProfile(ctx, o.source, string(token)); err != nil {
		return err
	}
	current := &ha.HomeAssistantMigration{}
	if err := o.kube.Get(ctx, client.ObjectKeyFromObject(o.op), current); err != nil {
		return fmt.Errorf("operation unavailable")
	}
	if !workerSettingsStable(current, o.request) ||
		VerifyBackupScopeAndKey(ctx, o.source, string(token), o.request.ScopeDigest, o.key) != nil {
		return fmt.Errorf("BackupSettingsChanged")
	}
	response, err := o.source.DownloadBackup(ctx, string(token), e.BackupID, e.AgentID)
	if err != nil || response == nil || response.Body == nil {
		return fmt.Errorf("backup archive unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 || (response.ContentLength >= 0 && response.ContentLength != e.Size) {
		return fmt.Errorf("BackupResultMismatch")
	}
	if _, err := StoreProtectedArchive(ctx, o.dir, "backup.tar", response.Body, e.Size, true); err != nil {
		return err
	}
	if VerifyBackupScopeAndKey(ctx, o.source, string(token), o.request.ScopeDigest, o.key) != nil {
		return fmt.Errorf("BackupSettingsChanged")
	}
	final, err := o.source.GetBackupDetails(ctx, string(token), e.BackupID)
	if err != nil {
		return fmt.Errorf("backup final observation unavailable")
	}
	preflight := observeBackupPreflight(ctx, o.source, string(token))
	again, err := finalBackupEvidence(final, preflight, o.op, o.request, o.pvc, o.receipt)
	if err != nil || again != e {
		return fmt.Errorf("BackupResultMismatch")
	}
	return nil
}

func (o *backupArchiveObservation) publishValidatedFile(ctx context.Context, e BackupSourceEvidence) error {
	stored, err := validateStoredProfile(ctx, o.dir, e, o.key)
	if err != nil {
		return err
	}
	ed, err := migrationprotocol.Digest(e)
	if err != nil {
		return err
	}
	d := ArchiveDescriptor{
		Version: "v1", OperationUID: string(o.op.UID), RequestID: o.request.RequestID, SourceID: o.request.SourceID,
		JobID: e.JobID, BackupID: e.BackupID, AgentID: e.AgentID, PVCUID: string(o.pvc.UID), Filename: "backup.tar",
		Size: stored.Size, Digest: stored.Digest, ScopeDigest: o.request.ScopeDigest, DatabaseIncluded: e.IncludeDatabase, Protected: true,
		Validation: &ArchiveValidation{
			Profile: ArchiveValidationProfile, ValidatorVersion: ArchiveValidatorVersion, CiphertextDigest: stored.Digest,
			SourceEvidenceDigest: ed, KeyRef: *o.request.RecoveryKey, PayloadAuthenticated: true, StructureValid: true,
		},
	}
	if _, err := ReadWorkerRequest(ctx, o.kube, o.op.Namespace, o.op.Name, "backup", o.request.RequestID); err != nil {
		return err
	}
	_, err = PublishArchiveDescriptor(
		ctx, o.kube, o.scheme, o.op, o.pvc, o.dir, o.request.ArchiveStorage.DescriptorName, d,
	)
	return err
}

// Only fixed reason codes are persisted. Transport errors and response bodies
// never enter status, logs, receipts or Events.
func recordBackupObservationError(
	ctx context.Context, kube client.Client, namespace, name, requestID string, cause error,
) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	reason := "BackupObservationUnavailable"
	if isTerminalObservationReason(cause.Error()) {
		reason = cause.Error()
	}
	if cause.Error() == "evidence publication collision" {
		reason = "ArchiveDescriptorConflict"
	}
	cm := &corev1.ConfigMap{}
	if kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: WorkerReceiptName(requestID)}, cm) != nil {
		return
	}
	op := &ha.HomeAssistantMigration{}
	if kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, op) != nil {
		return
	}
	owner := metav1.GetControllerOf(cm)
	var receipt BackupDispatchReceipt
	if owner == nil || owner.UID != op.UID ||
		migrationprotocol.Decode([]byte(cm.Data[backupDispatchReceiptKey]), &receipt) != nil {
		return
	}
	if receipt.RequestID != requestID || receipt.OperationUID != string(op.UID) || receipt.Phase != "Accepted" {
		return
	}
	receipt.BlockedReason = reason
	b, err := json.Marshal(receipt)
	if err != nil {
		return
	}
	cm.Data[backupDispatchReceiptKey] = string(b)
	_ = kube.Update(ctx, cm)
}

func isTerminalObservationReason(reason string) bool {
	switch reason {
	case "ArchiveValidationFailed", "ArchiveValidationLimitExceeded", "ArchiveProvenanceUnavailable",
		"BackupResultMismatch", "BackupSettingsChanged", "CredentialVersionChanged", "ArchiveDescriptorConflict",
		"UnsupportedArchiveProfile":
		return true
	}
	return false
}
