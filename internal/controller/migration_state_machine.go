package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
)

type migrationStageObserver interface {
	Observe(context.Context, *ha.HomeAssistantMigration, ha.MigrationExecutionRequest) (migrationStageObservation, error)
}

// migrationStageStarter must persist the attempt marker before creating any
// worker. An existing marker is never passed to Start a second time.
type migrationStageStarter interface {
	Start(context.Context, *ha.HomeAssistantMigration, ha.MigrationExecutionRequest) error
}

// migrationStageIsolationPreparer is implemented by executors that need durable
// isolation (a target policy and proxy credentials) before an upload can start.
// It reports true after writing a binding, so the caller re-reads before going on.
type migrationStageIsolationPreparer interface {
	PrepareIsolation(context.Context, *ha.HomeAssistantMigration, ha.MigrationExecutionRequest) (bool, error)
}

// migrationStagePreparer supplies observations only; it must not submit HA
// mutations. The concrete source-stop and archive checks remain fail-closed.
type migrationStagePreparer interface {
	Prepare(context.Context, *ha.HomeAssistantMigration, string) (migrationRequestObservation, bool, error)
}

// migrationModelPhase reads durable requests and attempt markers, never status.
// A missing marker or observation cannot authorize a repeat POST.
func migrationModelPhase(ctx context.Context, op *ha.HomeAssistantMigration, observer migrationStageObserver) (string, string, error) {
	if op == nil || observer == nil || op.Spec.ExecutionProtocol != "v1" {
		return "Blocked", "ExecutionObserverUnavailable", nil
	}
	if op.Spec.CancelRequested || !op.DeletionTimestamp.IsZero() {
		return "Blocked", "CancellationRequested", nil
	}
	if migrationAbortRequested(op) {
		return "Blocked", "AbortRequested", nil
	}
	requests := make(map[string]ha.MigrationExecutionRequest, len(op.Spec.ExecutionRequests))
	verifiedArchive := ""
	var verifiedArchiveRef *ha.MigrationObjectReference
	verifiedArchivePVCUID := ""
	for _, r := range op.Spec.ExecutionRequests {
		if r.OperationUID != string(op.UID) || r.SourceID != op.Spec.SourceID || r.Target != op.Spec.Target || r.RequestID == "" {
			return "Unknown", "RequestBindingInvalid", nil
		}
		if _, exists := requests[r.Stage]; exists {
			return "Unknown", "DuplicateExecutionStage", nil
		}
		requests[r.Stage] = r
	}
	for _, stage := range []string{"backup", "upload", "restore"} {
		request, exists := requests[stage]
		if !exists {
			if stage == "backup" {
				return "AwaitingBackupApproval", "BackupRequestMissing", nil
			}
			if stage == "upload" {
				return "BackupReady", "UploadRequestMissing", nil
			}
			return "RestoreInProgress", "RestoreRequestMissing", nil
		}
		attempt := false
		for _, marker := range op.Spec.ExecutionAttempts {
			if marker.Stage == stage && marker.RequestID == request.RequestID {
				attempt = true
			}
		}
		if !attempt {
			return "Blocked", "ExecutionAttemptNotStarted", nil
		}
		observation, err := observer.Observe(ctx, op, request)
		if err != nil {
			return "Unknown", "ObservationUnavailable", fmt.Errorf("cannot observe migration stage")
		}
		phase, reason := migrationObservedStage(request, observation)
		if phase == "Unknown" || phase == "Blocked" || phase == "BackupInProgress" || (phase == "RestoreInProgress" && reason != "UploadVerified") {
			return phase, reason, nil
		}
		if stage == "backup" {
			if !migrationprotocol.ValidDigest(observation.ArchiveDigest) || !migrationprotocol.ValidDigest(request.ScopeDigest) || observation.ScopeDigest != request.ScopeDigest || observation.VerificationDigest != request.VerificationDigest || !migrationprotocol.ValidDigest(request.VerificationDigest) || observation.ArchiveRef == nil || observation.ArchiveRef.Name == "" || observation.ArchiveRef.UID == "" || observation.ArchivePVCUID == "" {
				return "Unknown", "BackupEvidenceIncomplete", nil
			}
			verifiedArchive = observation.ArchiveDigest
			verifiedArchiveRef = observation.ArchiveRef
			verifiedArchivePVCUID = observation.ArchivePVCUID
		} else if request.ArchiveDigest != verifiedArchive || observation.ArchiveDigest != verifiedArchive || observation.ScopeDigest != request.ScopeDigest || observation.VerificationDigest != request.VerificationDigest || !reflect.DeepEqual(request.ArchiveRef, verifiedArchiveRef) || request.ArchivePVCUID != verifiedArchivePVCUID {
			return "Unknown", "ArchiveEvidenceMismatch", nil
		}
		if stage == "restore" {
			if !observation.AccessVerified || !migrationprotocol.ValidDigest(observation.ResultDigest) || !migrationprotocol.ValidDigest(request.ArchiveDigest) || !migrationprotocol.ValidDigest(request.VerificationDigest) || observation.ArchiveDigest != request.ArchiveDigest || observation.VerificationDigest != request.VerificationDigest {
				return "AwaitingVerification", "VerificationUnavailable", nil
			}
			resume, cutover := false, false
			for _, approval := range op.Spec.Approvals {
				bound := approval.Approved && approval.TargetUID == request.Target.UID && approval.ArchiveDigest == request.ArchiveDigest && approval.VerificationDigest == request.VerificationDigest
				if approval.ID == "resume" && bound && approval.Digest == observation.ResultDigest {
					resume = true
				}
				if approval.ID == "manual-cutover" && bound && strings.TrimSpace(approval.Digest) != "" {
					cutover = true
				}
			}
			if !resume {
				return "AwaitingResumeApproval", "ResumeApprovalRequired", nil
			}
			if !cutover {
				return "AwaitingManualCutover", "ManualCutoverRequired", nil
			}
			return "Completed", "MigrationVerified", nil
		}
	}
	return "Blocked", "VerificationUnavailable", nil
}

// migrationStageObservation is a point-in-time observation supplied by an
// executor. It is not durable state and never authorizes a second submission.
type migrationStageObservation struct {
	OperationUID       string
	RequestID          string
	Stage              string
	SourceID           string
	TargetUID          string
	Phase              string
	JobID              string
	Active             bool
	Verified           bool
	ArchiveDigest      string
	ArchiveRef         *ha.MigrationObjectReference
	ArchivePVCUID      string
	VerificationDigest string
	ScopeDigest        string
	ResultDigest       string
	AccessVerified     bool
	BlockedReason      string
}

func migrationObservedStage(request ha.MigrationExecutionRequest, observed migrationStageObservation) (string, string) {
	if request.OperationUID == "" || request.RequestID == "" || request.SourceID == "" || request.Target.UID == "" ||
		request.OperationUID != observed.OperationUID || request.RequestID != observed.RequestID || request.Stage != observed.Stage ||
		request.SourceID != observed.SourceID || request.Target.UID != observed.TargetUID {
		return "Unknown", "ReceiptBindingInvalid"
	}
	progress := "RestoreInProgress"
	if request.Stage == "backup" {
		progress = "BackupInProgress"
	}
	if request.Stage != "backup" && request.Stage != "upload" && request.Stage != "restore" {
		return "Unknown", "ReceiptStageInvalid"
	}
	switch observed.Phase {
	case "Blocked":
		if request.Stage == "backup" && (backupTerminalReason(observed.BlockedReason) || observed.BlockedReason == "BackupObservationRetryExhausted") && strings.TrimSpace(observed.JobID) != "" {
			return "Blocked", observed.BlockedReason
		}
		if observed.BlockedReason == "StoredArchiveAuthenticationUnavailable" && request.Stage == "backup" && strings.TrimSpace(observed.JobID) != "" {
			return "Blocked", observed.BlockedReason
		}
		return "Unknown", "ReceiptPhaseInvalid"
	case "Dispatching":
		if observed.Active && observed.JobID == "" && !observed.Verified {
			return progress, "DispatchInProgress"
		}
		return "Unknown", "DispatchOutcomeUnknown"
	case "Accepted":
		if strings.TrimSpace(observed.JobID) != "" && !observed.Verified {
			return progress, "ExternalJobAccepted"
		}
		return "Unknown", "ReceiptIncomplete"
	case "Verified":
		if observed.Verified && (request.Stage != "backup" || strings.TrimSpace(observed.JobID) != "") {
			if request.Stage == "backup" {
				return "BackupReady", "BackupVerified"
			}
			if request.Stage == "upload" {
				return "RestoreInProgress", "UploadVerified"
			}
			return "AwaitingVerification", "RestoreVerified"
		}
		return "Unknown", "VerificationIncomplete"
	default:
		return "Unknown", "ReceiptPhaseInvalid"
	}
}
