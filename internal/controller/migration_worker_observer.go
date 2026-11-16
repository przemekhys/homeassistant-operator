package controller

import (
	"context"
	"fmt"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// migrationWorkerObserver is not registered in the production manager until
// network isolation and worker cancellation are implemented.
type migrationWorkerObserver struct{ reader client.Reader }

func (o *migrationWorkerObserver) Observe(ctx context.Context, op *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest) (migrationStageObservation, error) {
	if o == nil || o.reader == nil || op == nil || request.Stage != "backup" || !migrationprotocol.ValidDigest(request.RequestID) {
		return migrationStageObservation{}, fmt.Errorf("worker observation unavailable")
	}
	marked := false
	for _, marker := range op.Spec.ExecutionDispatches {
		if marker.Stage == request.Stage && marker.RequestID == request.RequestID {
			marked = true
		}
	}
	if !marked {
		return migrationStageObservation{}, fmt.Errorf("worker dispatch evidence unavailable")
	}
	receipt, err := migration.ReadWorkerBackupReceipt(ctx, o.reader, op, request)
	if err != nil {
		return migrationStageObservation{}, fmt.Errorf("worker receipt unavailable")
	}
	observation := migrationStageObservation{OperationUID: receipt.OperationUID, RequestID: receipt.RequestID, Stage: request.Stage, SourceID: request.SourceID, TargetUID: receipt.TargetUID, Phase: receipt.Phase, JobID: receipt.JobID}
	if receipt.BlockedReason != "" {
		if receipt.BlockedReason != "StoredArchiveAuthenticationUnavailable" || !migrationprotocol.ValidDigest(receipt.ArchiveDigest) || receipt.ArchiveSize <= 0 || receipt.ArchivePVCUID == "" {
			return migrationStageObservation{}, fmt.Errorf("worker blocker evidence invalid")
		}
		observation.Phase, observation.BlockedReason = "Blocked", receipt.BlockedReason
		return observation, nil
	}
	if receipt.Phase == "Dispatching" {
		job := &batchv1.Job{}
		if err := o.reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: migrationWorkerJobName(request.RequestID)}, job); err != nil {
			return observation, nil
		}
		owner := metav1.GetControllerOf(job)
		if owner == nil || owner.UID != op.UID {
			return migrationStageObservation{}, fmt.Errorf("worker job binding invalid")
		}
		observation.Active = job.Status.Active > 0
	}
	return observation, nil
}
