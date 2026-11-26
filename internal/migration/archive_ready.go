package migration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadValidatedBackupEvidence checks persistent bindings, not just a historical
// protected flag. It is used by the controller only after an owned validation
// Job has completed; the worker reopens and verifies bytes on the actual PVC.
func ReadValidatedBackupEvidence(
	ctx context.Context, reader client.Reader, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest,
) (*ArchiveDescriptor, *corev1.ConfigMap, error) {
	if op == nil || r.ArchiveStorage == nil || r.RecoveryKey == nil {
		return nil, nil, fmt.Errorf("archive binding unavailable")
	}
	if migrationprotocol.ValidateAccepted(op) != nil {
		return nil, nil, fmt.Errorf("archive request invalid")
	}
	if !acceptedBackupDispatched(op, r) {
		return nil, nil, fmt.Errorf("archive execution binding invalid")
	}
	if _, err := ReadPinnedCredential(ctx, reader, op.Namespace, r.RecoveryKey); err != nil {
		return nil, nil, err
	}
	receipt, err := observedBackupReceipt(ctx, reader, op, r)
	if err != nil {
		return nil, nil, err
	}
	if receipt.Phase != "Accepted" || isTerminalObservationReason(receipt.BlockedReason) {
		return nil, nil, fmt.Errorf("archive observation blocked")
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: r.ArchiveStorage.PVCName}, pvc); err != nil {
		return nil, nil, fmt.Errorf("archive PVC unavailable")
	}
	owner := metav1.GetControllerOf(pvc)
	if owner == nil || owner.UID != op.UID || pvc.UID == "" {
		return nil, nil, fmt.Errorf("archive PVC binding invalid")
	}
	e, err := readSourceEvidence(ctx, reader, op, r)
	if err != nil || validateSourceEvidence(*e, op, r, pvc, receipt) != nil {
		return nil, nil, fmt.Errorf("ArchiveProvenanceUnavailable")
	}
	cm := &corev1.ConfigMap{}
	cmKey := client.ObjectKey{Namespace: op.Namespace, Name: r.ArchiveStorage.DescriptorName}
	if err := reader.Get(ctx, cmKey, cm); err != nil {
		return nil, nil, err
	}
	var d ArchiveDescriptor
	if cm.UID == "" || !ownedImmutableEvidence(cm, op) ||
		migrationprotocol.Decode([]byte(cm.Data["descriptor.json"]), &d) != nil {
		return nil, nil, fmt.Errorf("archive descriptor invalid")
	}
	if !validatedDescriptorMatches(d, *e, r) {
		return nil, nil, fmt.Errorf("archive descriptor binding invalid")
	}
	return &d, cm, nil
}

func acceptedBackupDispatched(op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) bool {
	accepted, dispatched := false, false
	for _, request := range op.Spec.ExecutionRequests {
		if reflect.DeepEqual(request, r) {
			accepted = true
		}
	}
	for _, marker := range op.Spec.ExecutionDispatches {
		if marker.Stage == "backup" && marker.RequestID == r.RequestID {
			dispatched = true
		}
	}
	return accepted && dispatched
}

func validatedDescriptorMatches(d ArchiveDescriptor, e BackupSourceEvidence, r ha.MigrationExecutionRequest) bool {
	digest, err := migrationprotocol.Digest(e)
	if err != nil || d.Version != "v1" || d.OperationUID != e.OperationUID || d.RequestID != r.RequestID ||
		d.SourceID != r.SourceID || d.PVCUID != e.PVCUID || d.JobID != e.JobID || d.BackupID != e.BackupID ||
		d.AgentID != e.AgentID || d.ScopeDigest != r.ScopeDigest || d.Size != e.Size || d.Filename != "backup.tar" ||
		d.DatabaseIncluded != e.IncludeDatabase || !d.Protected || !migrationprotocol.ValidDigest(d.Digest) || d.Validation == nil {
		return false
	}
	want := ArchiveValidation{
		Profile: ArchiveValidationProfile, ValidatorVersion: ArchiveValidatorVersion, CiphertextDigest: d.Digest,
		SourceEvidenceDigest: digest, KeyRef: *r.RecoveryKey, PayloadAuthenticated: true, StructureValid: true,
	}
	return reflect.DeepEqual(*d.Validation, want)
}

// validateStoredProfile authenticates, parses and hashes the same regular file
// descriptor. A producer must be terminated before the observer mounts the PVC.
func validateStoredProfile(
	ctx context.Context, dir string, e BackupSourceEvidence, key []byte,
) (ArchiveReceipt, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ArchiveReceipt{}, fmt.Errorf("archive storage unavailable")
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat("backup.tar")
	if err != nil || !info.Mode().IsRegular() || info.Size() != e.Size {
		return ArchiveReceipt{}, fmt.Errorf("ArchiveValidationFailed")
	}
	f, err := root.Open("backup.tar")
	if err != nil {
		return ArchiveReceipt{}, fmt.Errorf("archive unavailable")
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return ArchiveReceipt{}, fmt.Errorf("ArchiveValidationFailed")
	}
	expected := archiveMetadataExpectation{Slug: e.BackupID, IncludeDatabase: e.IncludeDatabase, Date: e.Date}
	digest, size, err := validateArchiveProfile(ctx, f, key, expected)
	if ctx.Err() != nil {
		return ArchiveReceipt{}, fmt.Errorf("archive validation interrupted")
	}
	if errors.Is(err, errArchiveValidationLimit) {
		return ArchiveReceipt{}, errArchiveValidationLimit
	}
	if errors.Is(err, errUnsupportedArchiveProfile) {
		return ArchiveReceipt{}, errUnsupportedArchiveProfile
	}
	if err != nil || size != e.Size {
		return ArchiveReceipt{}, fmt.Errorf("ArchiveValidationFailed")
	}
	after, err := f.Stat()
	if err != nil || after.Size() != opened.Size() || after.ModTime() != opened.ModTime() {
		return ArchiveReceipt{}, fmt.Errorf("ArchiveValidationFailed")
	}
	return ArchiveReceipt{Digest: digest, Size: size}, nil
}
