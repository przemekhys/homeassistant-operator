package controller

import (
	"context"
	"fmt"
	"reflect"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// migrationRequestObservation contains already observed archive identity. These
// inputs do not prove readiness; the executor must verify bytes and source stop.
type migrationRequestObservation struct {
	ArchiveRef    *ha.MigrationObjectReference
	ArchiveDigest string
	ArchivePVCUID string
}

func buildMigrationRequest(ctx context.Context, reader client.Reader, op *ha.HomeAssistantMigration, stage string, observed migrationRequestObservation) (*ha.MigrationExecutionRequest, error) {
	vd, err := migrationprotocol.VerificationDigest(op.Spec.Verification)
	if err != nil {
		return nil, err
	}
	r := ha.MigrationExecutionRequest{Version: "v1", Stage: stage, OperationUID: string(op.UID), SourceID: op.Spec.SourceID, ReportID: op.Spec.ReportID, Target: op.Spec.Target, VerificationDigest: vd}
	if stage == "backup" {
		r.SourceEndpoint = op.Spec.SourceEndpoint
		r.ScopeDigest = op.Spec.BackupScopeDigest
		r.ArchiveStorage = op.Spec.ArchiveStorage
		token, err := migrationSecretRefIdentity(ctx, reader, op.Namespace, op.Spec.SourceTokenSecretRef)
		if err != nil {
			return nil, err
		}
		key, err := migrationSecretRefIdentity(ctx, reader, op.Namespace, op.Spec.RecoveryKeySecretRef)
		if err != nil {
			return nil, err
		}
		r.SourceToken = &ha.MigrationSecretIdentity{Name: token.Name, Key: token.Key, UID: token.UID, ResourceVersion: token.ResourceVersion}
		r.RecoveryKey = &ha.MigrationSecretIdentity{Name: key.Name, Key: key.Key, UID: key.UID, ResourceVersion: key.ResourceVersion}
	} else {
		r.ScopeDigest = op.Spec.RestoreScopeDigest
		r.ArchiveRef = observed.ArchiveRef
		r.ArchiveDigest = observed.ArchiveDigest
		r.ArchivePVCUID = observed.ArchivePVCUID
		r.SourceWorkload = op.Spec.SourceWorkload
		if stage == "restore" {
			for _, u := range op.Spec.ExecutionRequests {
				if u.Stage == "upload" {
					r.UploadRequestID = u.RequestID
				}
			}
			key, err := migrationSecretRefIdentity(ctx, reader, op.Namespace, op.Spec.RecoveryKeySecretRef)
			if err != nil {
				return nil, err
			}
			r.RecoveryKey = &ha.MigrationSecretIdentity{Name: key.Name, Key: key.Key, UID: key.UID, ResourceVersion: key.ResourceVersion}
		}
	}
	r.RequestID, err = migrationprotocol.RequestID(r)
	if err != nil {
		return nil, err
	}
	if err := migrationprotocol.ValidateNewRequest(op, r); err != nil {
		return nil, err
	}
	return &r, nil
}

// acceptMigrationRequest uses the operation resourceVersion read with approval.
// An uncertain write is recovered only by observing the exact persisted snapshot.
func acceptMigrationRequest(ctx context.Context, writer client.Client, reader client.Reader, op *ha.HomeAssistantMigration, stage string, observation migrationRequestObservation) (*ha.MigrationExecutionRequest, error) {
	if writer == nil || reader == nil || op == nil || op.UID == "" || op.ResourceVersion == "" {
		return nil, fmt.Errorf("request acceptance inputs are missing")
	}
	if err := migrationprotocol.ValidateAccepted(op); err != nil {
		return nil, err
	}
	for _, r := range op.Spec.ExecutionRequests {
		if r.Stage == stage {
			copy := r
			return &copy, nil
		}
	}
	if op.Spec.ExecutionProtocol != "v1" {
		return nil, fmt.Errorf("LegacyExecutionProtocol")
	}
	for _, legacyStage := range []string{"backup", "upload", "restore"} {
		if op.Annotations["ha.homeassistant.io/migration-request-"+legacyStage] != "" || op.Annotations[migrationReceiptAnnotation] != "" {
			return nil, fmt.Errorf("LegacyExecutionEvidence")
		}
	}
	r, err := buildMigrationRequest(ctx, reader, op, stage, observation)
	if err != nil {
		return nil, err
	}
	updated := op.DeepCopy()
	updated.Spec.ExecutionRequests = append(updated.Spec.ExecutionRequests, *r)
	if err := writer.Update(ctx, updated); err != nil {
		current := &ha.HomeAssistantMigration{}
		if reader.Get(ctx, client.ObjectKeyFromObject(op), current) == nil && current.UID == op.UID {
			for _, accepted := range current.Spec.ExecutionRequests {
				if reflect.DeepEqual(accepted, *r) {
					return r, nil
				}
			}
		}
		return nil, fmt.Errorf("execution request acceptance not confirmed")
	}
	return r, nil
}
