package migration

import (
	"context"
	"fmt"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// observedBackupReceipt supplies an in-memory observation hint only. A missing
// or dispatching receipt is never recreated or updated: append-only dispatch
// history still prevents the hint from becoming permission for another POST.
//
//nolint:gocyclo // Validation and lifecycle branches are intentionally fail-closed.
func observedBackupReceipt(ctx context.Context, reader client.Reader, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) (*BackupDispatchReceipt, error) {
	cm := &corev1.ConfigMap{}
	err := reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: WorkerReceiptName(r.RequestID)}, cm)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("backup receipt unavailable")
	}
	if err == nil {
		receipt, err := ReadWorkerBackupReceipt(ctx, reader, op, r)
		if err != nil {
			return nil, err
		}
		if receipt.Phase == "Accepted" {
			return receipt, nil
		}
		if receipt.Phase != "Dispatching" || receipt.JobID != "" {
			return nil, fmt.Errorf("backup receipt invalid")
		}
	}
	if !acceptedBackupDispatched(op, r) || migrationprotocol.ValidateAccepted(op) != nil {
		return nil, fmt.Errorf("backup dispatch binding invalid")
	}
	resolution, err := migrationprotocol.ObservationResolution(op, "backup", r.RequestID)
	if err != nil {
		return nil, fmt.Errorf("ambiguous resolution evidence")
	}
	if resolution != nil && resolution.Version == "v1" && migrationprotocol.ValidateEvidenceRef(resolution.EvidenceRef) == nil {
		ref := resolution.EvidenceRef
		evidenceCM := &corev1.ConfigMap{}
		if reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: ref.Name}, evidenceCM) != nil || string(evidenceCM.UID) != ref.UID || evidenceCM.Immutable == nil || !*evidenceCM.Immutable {
			return nil, fmt.Errorf("resolution evidence unavailable")
		}
		e, err := migrationprotocol.ParseEvidence([]byte(evidenceCM.Data[ref.Key]))
		if err != nil || e.OperationUID != string(op.UID) || e.RequestID != r.RequestID || e.Stage != r.Stage || e.SourceID != r.SourceID || e.TargetUID != r.Target.UID {
			return nil, fmt.Errorf("resolution evidence invalid")
		}
		jobID := e.JobID
		if jobID == "" {
			jobID = e.BackupID
		}
		if jobID == "" && e.ArchiveDescriptorRef != nil {
			descriptor := &corev1.ConfigMap{}
			ref := e.ArchiveDescriptorRef
			if r.ArchiveStorage == nil || ref.Name != r.ArchiveStorage.DescriptorName ||
				reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: ref.Name}, descriptor) != nil ||
				string(descriptor.UID) != ref.UID || !ownedImmutableEvidence(descriptor, op) {
				return nil, fmt.Errorf("archive resolution unavailable")
			}
			var d ArchiveDescriptor
			source, sourceErr := readSourceEvidence(ctx, reader, op, r)
			if sourceErr != nil || migrationprotocol.Decode([]byte(descriptor.Data["descriptor.json"]), &d) != nil ||
				!validatedDescriptorMatches(d, *source, r) || (e.ArchiveDigest != "" && e.ArchiveDigest != d.Digest) {
				return nil, fmt.Errorf("archive resolution invalid")
			}
			jobID = d.JobID
		}
		if !nativeBackupIDPattern.MatchString(jobID) || (e.BackupID != "" && e.BackupID != jobID) {
			return nil, fmt.Errorf("resolution job identity invalid")
		}
		return &BackupDispatchReceipt{RequestID: r.RequestID, OperationUID: r.OperationUID, TargetUID: r.Target.UID, ReportID: r.ReportID, ScopeDigest: r.ScopeDigest, Phase: "Accepted", JobID: jobID}, nil
	}
	return nil, fmt.Errorf("backup outcome unknown")
}
