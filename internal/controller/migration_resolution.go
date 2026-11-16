package controller

import (
	"context"
	"fmt"
	"reflect"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// readMigrationResolutionEvidence treats user-supplied evidence as a hint to
// observe, never as proof of an external result or permission for mutation.
func readMigrationResolutionEvidence(
	ctx context.Context, reader client.Reader, op *ha.HomeAssistantMigration,
	r ha.MigrationExecutionRequest, resolution ha.MigrationResolution,
) (*migrationprotocol.Evidence, error) {
	if ctx == nil || reader == nil || op == nil || resolution.Version != "v1" || resolution.Decision != "ObserveExternalResult" ||
		resolution.Stage != r.Stage || resolution.RequestID != r.RequestID || migrationprotocol.ValidateEvidenceRef(resolution.EvidenceRef) != nil {
		return nil, fmt.Errorf("resolution evidence unavailable")
	}
	ref := resolution.EvidenceRef
	cm := &corev1.ConfigMap{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: ref.Name}, cm); err != nil ||
		string(cm.UID) != ref.UID || cm.Immutable == nil || !*cm.Immutable {
		return nil, fmt.Errorf("resolution evidence unavailable")
	}
	e, err := migrationprotocol.ParseEvidence([]byte(cm.Data[ref.Key]))
	if err != nil || e.OperationUID != string(op.UID) || e.RequestID != r.RequestID || e.Stage != r.Stage ||
		e.SourceID != r.SourceID || e.TargetUID != r.Target.UID {
		return nil, fmt.Errorf("resolution evidence binding invalid")
	}
	if r.Stage != "backup" && (e.ArchiveDigest != "" && e.ArchiveDigest != r.ArchiveDigest ||
		e.ArchiveDescriptorRef != nil && !reflect.DeepEqual(e.ArchiveDescriptorRef, r.ArchiveRef)) {
		return nil, fmt.Errorf("resolution archive binding invalid")
	}
	return e, nil
}

func migrationAbortRequested(op *ha.HomeAssistantMigration) bool {
	return migrationprotocol.AbortRequested(op)
}

// migrationResolutionObserver only reads the indicated external result. Its
// output is still subject to ordinary stage and archive verification gates.
type migrationResolutionObserver interface {
	ObserveExternalResult(context.Context, *ha.HomeAssistantMigration, ha.MigrationExecutionRequest, migrationprotocol.Evidence) (migrationStageObservation, error)
}

type resolutionAwareObserver struct {
	reader   client.Reader
	delegate migrationStageObserver
}

func (o resolutionAwareObserver) Observe(ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) (migrationStageObservation, error) {
	observed, err := o.delegate.Observe(ctx, op, r)
	if err == nil {
		phase, reason := migrationObservedStage(r, observed)
		if phase != "Unknown" && reason != "BackupObservationRetryExhausted" {
			return observed, nil
		}
	}
	resolver, ok := o.delegate.(migrationResolutionObserver)
	if !ok {
		return observed, err
	}
	resolution, resolutionErr := migrationprotocol.ObservationResolution(op, r.Stage, r.RequestID)
	if resolutionErr != nil {
		return migrationStageObservation{}, resolutionErr
	}
	if resolution != nil {
		evidence, readErr := readMigrationResolutionEvidence(ctx, o.reader, op, r, *resolution)
		if readErr != nil {
			return migrationStageObservation{}, fmt.Errorf("resolution evidence unavailable")
		}
		result, observeErr := resolver.ObserveExternalResult(ctx, op, r, *evidence)
		if observeErr != nil {
			return migrationStageObservation{}, fmt.Errorf("external resolution observation unavailable")
		}
		if result.OperationUID != r.OperationUID || result.RequestID != r.RequestID || result.Stage != r.Stage ||
			result.SourceID != r.SourceID || result.TargetUID != r.Target.UID ||
			(evidence.JobID != "" && result.JobID != evidence.JobID) ||
			(result.Verified && evidence.ArchiveDigest != "" && result.ArchiveDigest != evidence.ArchiveDigest) ||
			(result.Verified && evidence.ArchiveDescriptorRef != nil && !reflect.DeepEqual(evidence.ArchiveDescriptorRef, result.ArchiveRef)) {
			return migrationStageObservation{}, fmt.Errorf("external resolution result binding invalid")
		}
		return result, nil
	}
	return observed, err
}
