package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	migrationpkg "github.com/przemekhys/homeassistant-operator/internal/migration"
)

const migrationOperationFinalizer = "ha.homeassistant.io/migration-operation-finalizer"

const migrationReceiptAnnotation = "ha.homeassistant.io/migration-receipt"

type migrationReceipt struct {
	Generation int64  `json:"generation"`
	SourceID   string `json:"sourceID"`
	TargetUID  string `json:"targetUID"`
	Stage      string `json:"stage"`
}

// HomeAssistantMigrationReconciler owns exactly the HomeAssistantMigration Kind.
// Destructive backup and restore steps are intentionally not sent until a durable
// approval is present; unknown outcomes are never retried here.
type HomeAssistantMigrationReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	APIReader client.Reader
	// stageObserver is deliberately unset in production until execution,
	// isolation, and cancellation can be enforced together.
	stageObserver migrationStageObserver
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantmigrations,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantmigrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantmigrations/finalizers,verbs=update

func (r *HomeAssistantMigrationReconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	migration := &hav1alpha1.HomeAssistantMigration{}
	if err := r.Get(ctx, req.NamespacedName, migration); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !migration.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(migration, migrationOperationFinalizer) {
			controllerutil.RemoveFinalizer(migration, migrationOperationFinalizer)
			return ctrl.Result{}, r.Update(ctx, migration)
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(migration, migrationOperationFinalizer) {
		controllerutil.AddFinalizer(migration, migrationOperationFinalizer)
		return ctrl.Result{}, r.Update(ctx, migration)
	}
	if migration.Spec.CancelRequested || migrationAbortRequested(migration) {
		changed := migration.Status.Phase != "Blocked"
		migration.Status.Phase = "Blocked"
		reason, message := "CancellationRequested", "new migration work is blocked; cancellation is not yet implemented"
		if migrationAbortRequested(migration) {
			reason = "AbortRequested"
			message = "new migration work is blocked; local worker termination is not yet implemented"
		}
		if meta.SetStatusCondition(&migration.Status.Conditions, metav1.Condition{
			Type: "Progressing", Status: metav1.ConditionFalse, ObservedGeneration: migration.Generation,
			Reason: reason, Message: message,
		}) {
			changed = true
		}
		if changed {
			if err := r.Status().Update(ctx, migration); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	if migration.Spec.ExecutionProtocol == "" {
		reader := r.APIReader
		if reader == nil {
			reader = r.Client
		}
		evidence := migration.Annotations[migrationReceiptAnnotation] != "" || migration.Annotations["ha.homeassistant.io/migration-outcome"] == "unknown"
		for _, stage := range []string{"backup", "upload", "restore"} {
			evidence = evidence || migration.Annotations["ha.homeassistant.io/migration-request-"+stage] != ""
		}
		if migration.UID != "" {
			claim := &corev1.ConfigMap{}
			err := reader.Get(ctx, client.ObjectKey{Namespace: migration.Namespace, Name: migrationpkg.BackupDispatchReceiptName(string(migration.UID))}, claim)
			if err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("cannot read backup dispatch receipt")
			}
			evidence = evidence || err == nil
		}
		phase, reason := "Blocked", "LegacyExecutionProtocol"
		if evidence {
			phase, reason = "Unknown", "LegacyExecutionEvidence"
		}
		changed := migration.Status.Phase != phase
		migration.Status.Phase = phase
		if meta.SetStatusCondition(&migration.Status.Conditions, metav1.Condition{Type: "Progressing", Status: metav1.ConditionUnknown, ObservedGeneration: migration.Generation, Reason: reason, Message: "legacy operation cannot start a new execution; resolve any prior external action before a new migration"}) {
			changed = true
		}
		if changed {
			if err := r.Status().Update(ctx, migration); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	if err := validateMigrationExecutionInputs(migration); err != nil {
		changed := migration.Status.Phase != "Blocked"
		migration.Status.Phase = "Blocked"
		if meta.SetStatusCondition(&migration.Status.Conditions, metav1.Condition{
			Type: "Progressing", Status: metav1.ConditionFalse, ObservedGeneration: migration.Generation,
			Reason: "ExecutionInputsInvalid", Message: "migration execution inputs or an accepted request are invalid",
		}) {
			changed = true
		}
		if changed {
			if err := r.Status().Update(ctx, migration); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	if r.stageObserver != nil {
		reader := r.APIReader
		if reader == nil {
			reader = r.Client
		}
		observer := resolutionAwareObserver{reader: reader, delegate: r.stageObserver}
		phase, reason, err := migrationModelPhase(ctx, migration, observer)
		if err != nil {
			phase, reason = "Unknown", "ObservationUnavailable"
		}
		if reason == "BackupRequestMissing" {
			for _, approval := range migration.Spec.Approvals {
				if approval.ID == "backup" && approval.Approved {
					reader := r.APIReader
					if reader == nil {
						reader = r.Client
					}
					if _, err := acceptBackupRequest(ctx, r.Client, reader, migration); err != nil {
						phase, reason = "Blocked", "BackupRequestNotAccepted"
					} else {
						return ctrl.Result{RequeueAfter: time.Second}, nil
					}
				}
			}
		}
		if reason == "UploadRequestMissing" || reason == "RestoreRequestMissing" {
			stage := "upload"
			if reason == "RestoreRequestMissing" {
				stage = "restore"
			}
			restoreApproved, stopApproved := false, false
			for _, approval := range migration.Spec.Approvals {
				if approval.ID == "restore" && approval.Approved {
					restoreApproved = true
				}
				if approval.ID == "source-stop" && approval.Approved {
					stopApproved = true
				}
			}
			if !restoreApproved {
				phase, reason = "BackupReady", "RestoreApprovalRequired"
			} else if !stopApproved {
				phase, reason = "AwaitingSourceStop", "SourceStopApprovalRequired"
			} else if preparer, ok := r.stageObserver.(migrationStagePreparer); !ok {
				phase, reason = "Blocked", "RestorePreflightUnavailable"
			} else {
				observation, stopped, prepareErr := preparer.Prepare(ctx, migration, stage)
				if prepareErr != nil || !stopped {
					phase, reason = "AwaitingSourceStop", "SourceStopUnverified"
				} else {
					reader := r.APIReader
					if reader == nil {
						reader = r.Client
					}
					if _, err := acceptMigrationRequest(ctx, r.Client, reader, migration, stage, observation); err != nil {
						phase, reason = "Blocked", "RestoreRequestNotAccepted"
					} else {
						return ctrl.Result{RequeueAfter: time.Second}, nil
					}
				}
			}
		}
		if reason == "ExecutionAttemptNotStarted" {
			starter, available := r.stageObserver.(migrationStageStarter)
			if available {
			attemptStages:
				for _, stage := range []string{"backup", "upload", "restore"} {
					for _, request := range migration.Spec.ExecutionRequests {
						if request.Stage != stage {
							continue
						}
						started := false
						for _, attempt := range migration.Spec.ExecutionAttempts {
							if attempt.Stage == stage {
								started = true
							}
						}
						if !started {
							if preparer, isolates := r.stageObserver.(migrationStageIsolationPreparer); isolates && stage == "upload" {
								requeue, prepErr := preparer.PrepareIsolation(ctx, migration, request)
								if prepErr != nil {
									phase, reason = "Blocked", "OnboardingIsolationPreparationUnavailable"
									break attemptStages
								}
								if requeue {
									return ctrl.Result{RequeueAfter: time.Second}, nil
								}
							}
							if err := starter.Start(ctx, migration, request); err != nil {
								if errors.Is(err, errMigrationStartPrecondition) {
									// Refused before the attempt marker: nothing was
									// started and it will be evaluated again.
									phase, reason = "Blocked", "OnboardingStartPreconditionUnmet"
									break attemptStages
								}
								migration.Status.Phase = "Unknown"
								meta.SetStatusCondition(&migration.Status.Conditions, metav1.Condition{Type: "Progressing", Status: metav1.ConditionUnknown, ObservedGeneration: migration.Generation, Reason: "AttemptStartOutcomeUnknown", Message: "execution attempt creation could not be confirmed"})
								if err := r.Status().Update(ctx, migration); err != nil {
									return ctrl.Result{}, err
								}
								return ctrl.Result{RequeueAfter: time.Minute}, nil
							}
							return ctrl.Result{RequeueAfter: time.Second}, nil
						}
					}
				}
			} else {
				phase, reason = "Blocked", "ExecutionStarterUnavailable"
			}
		}
		changed := migration.Status.Phase != phase
		migration.Status.Phase = phase
		message := "migration progress is derived from execution observations"
		// Include result digest in message when awaiting resume approval
		if reason == "ResumeApprovalRequired" {
			// Try to get the result digest from observation
			var restoreRequest *hav1alpha1.MigrationExecutionRequest
			for i := range migration.Spec.ExecutionRequests {
				if migration.Spec.ExecutionRequests[i].Stage == "restore" {
					restoreRequest = &migration.Spec.ExecutionRequests[i]
					break
				}
			}
			if restoreRequest != nil && r.stageObserver != nil {
				obs, err := observer.Observe(ctx, migration, *restoreRequest)
				if err == nil && obs.ResultDigest != "" {
					message = fmt.Sprintf("awaiting resume approval with digest %s matching verified result", obs.ResultDigest)
				}
			}
		}
		if meta.SetStatusCondition(&migration.Status.Conditions, metav1.Condition{Type: "Progressing", Status: metav1.ConditionUnknown, ObservedGeneration: migration.Generation, Reason: reason, Message: message}) {
			changed = true
		}
		if changed {
			if err := r.Status().Update(ctx, migration); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	if err := r.ensureRestorePVC(ctx, migration); err != nil {
		return ctrl.Result{}, err
	}

	phase, reason := migrationWaitingPhase(migration)
	if migration.UID != "" {
		reader := r.APIReader
		if reader == nil {
			reader = r.Client
		}
		claim := &corev1.ConfigMap{}
		key := client.ObjectKey{Namespace: migration.Namespace, Name: migrationpkg.BackupDispatchReceiptName(string(migration.UID))}
		err := reader.Get(ctx, key, claim)
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("cannot read backup dispatch receipt")
		}
		if err == nil {
			phase, reason = "Unknown", "BackupDispatchOutcomeUnknown"
		}
	}
	// Once an execution receipt exists, changing approvals or losing access to
	// the source cannot prove that the external operation did not happen.
	// Preserve the ambiguous outcome independently of the current spec.
	if migration.Annotations[migrationReceiptAnnotation] != "" {
		if err := validateMigrationExecutionReceipt(migration); err != nil {
			phase, reason = "Unknown", "ReceiptInvalid"
		} else {
			phase, reason = "Unknown", "ExecutionOutcomeUnknown"
		}
	}
	// Without an execution observer, durable attempt or dispatch markers can
	// outlive a Job or receipt. Their absence cannot prove that HA was untouched.
	if len(migration.Spec.ExecutionAttempts) != 0 || len(migration.Spec.ExecutionDispatches) != 0 {
		phase, reason = "Unknown", "ExecutionOutcomeUnknown"
	}
	if reason == "BackupExecutorUnavailable" {
		ref := migration.Spec.SourceTokenSecretRef
		secret := &corev1.Secret{}
		reader := r.APIReader
		if reader == nil {
			reader = r.Client
		}
		err := reader.Get(ctx, client.ObjectKey{Namespace: migration.Namespace, Name: ref.Name}, secret)
		if err != nil && !apierrors.IsNotFound(err) {
			// Do not expose API errors that may contain sensitive details.
			return ctrl.Result{}, fmt.Errorf("cannot read source token Secret")
		}
		if apierrors.IsNotFound(err) || len(secret.Data[ref.Key]) == 0 {
			reason = "SourceTokenUnavailable"
		}
	}
	if phase == "ReadyToExecute" {
		reader := r.APIReader
		if reader == nil {
			reader = r.Client
		}
		stopped, err := sourceStopVerified(ctx, reader, migration)
		if err != nil {
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		if !stopped {
			phase, reason = "AwaitingSourceStop", "SourceStopUnverified"
		}
	}
	if phase == "ReadyToExecute" && migration.Annotations[migrationReceiptAnnotation] == "" {
		// The target onboarding endpoint is not isolated yet, and there is no
		// durable executor. Do not write an intent receipt for an action that
		// cannot be sent: after restart that would falsely imply an unknown result.
		phase, reason = "Blocked", "OnboardingIsolationUnavailable"
	}
	// Provisioning the candidate is deliberately separate from authorizing a
	// target start. Its presence is not evidence of effective isolation.
	if r.APIReader != nil {
		for _, request := range migration.Spec.ExecutionRequests {
			if request.Stage != "upload" {
				continue
			}
			target := &hav1.HomeAssistant{}
			key := client.ObjectKey{Namespace: migration.Namespace, Name: migration.Spec.Target.Name}
			if err := r.APIReader.Get(ctx, key, target); err != nil ||
				ensureStoppedTargetPolicy(ctx, r.Client, r.APIReader, r.Scheme, migration, target, request) != nil ||
				observeTargetPolicySnapshot(ctx, r.APIReader, r.Scheme, migration, target, request) != nil {
				if phase != "Unknown" {
					phase, reason = "Blocked", "OnboardingIsolationPreparationUnavailable"
				}
			} else if migration.Spec.ProxyCredentialBinding == nil {
				binding, bindErr := createProxyCredentialBundle(ctx, r.Client, r.APIReader, r.Scheme, migration, request, time.Now())
				if bindErr != nil || bindProxyCredentialBundle(ctx, r.Client, r.APIReader, migration, *binding, time.Now()) != nil {
					phase, reason = "Blocked", "OnboardingCredentialBindingUnavailable"
				} else {
					// The spec update is the durable boundary. Re-read on the next
					// reconcile rather than writing a status from stale memory.
					return ctrl.Result{RequeueAfter: time.Second}, nil
				}
			}
			break
		}
	}
	changed := migration.Status.Phase != phase
	migration.Status.Phase = phase
	if meta.SetStatusCondition(&migration.Status.Conditions, metav1.Condition{
		Type: "Progressing", Status: metav1.ConditionUnknown, ObservedGeneration: migration.Generation,
		Reason: reason, Message: "migration is waiting for a durable approval or implementation evidence",
	}) {
		changed = true
	}
	if changed {
		if err := r.Status().Update(ctx, migration); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// ensureRestorePVC creates the fresh target PVC under the migration owner.
// An existing PVC is never adopted: it might contain unrelated data and would
// violate the migration lifecycle contract.
func (r *HomeAssistantMigrationReconciler) ensureRestorePVC(
	ctx context.Context,
	migration *hav1alpha1.HomeAssistantMigration,
) error {
	target := &hav1.HomeAssistant{}
	key := client.ObjectKey{Namespace: migration.Namespace, Name: migration.Spec.Target.Name}
	if err := r.Get(ctx, key, target); err != nil {
		return fmt.Errorf("read migration target for restore PVC: %w", err)
	}
	if string(target.UID) != migration.Spec.Target.UID {
		return fmt.Errorf("migration target UID changed before restore PVC preparation")
	}
	if !migrationRestorePreparationEnabled(target) {
		return nil
	}
	pvcName := target.Name + "-data"
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: migration.Namespace, Name: pvcName}, pvc); err == nil {
		for _, owner := range pvc.OwnerReferences {
			if owner.UID == migration.UID && owner.Controller != nil && *owner.Controller {
				return nil
			}
		}
		return fmt.Errorf("restore PVC already exists and is not owned by this migration")
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("read restore PVC: %w", err)
	}
	pvc = (&HomeAssistantReconciler{Scheme: r.Scheme}).buildPVC(target, pvcName)
	if err := controllerutil.SetControllerReference(migration, pvc, r.Scheme); err != nil {
		return fmt.Errorf("set restore PVC owner: %w", err)
	}
	if err := r.Create(ctx, pvc); err != nil {
		return fmt.Errorf("create restore PVC: %w", err)
	}
	return nil
}

func validateMigrationExecutionReceipt(
	migration *hav1alpha1.HomeAssistantMigration,
) error {
	var receipt migrationReceipt
	if err := json.Unmarshal([]byte(migration.Annotations[migrationReceiptAnnotation]), &receipt); err != nil {
		return err
	}
	if receipt.Generation != migration.Generation ||
		receipt.SourceID != migration.Spec.SourceID ||
		receipt.TargetUID != migration.Spec.Target.UID ||
		receipt.Stage == "" {
		return fmt.Errorf("receipt does not bind current migration intent")
	}
	return nil
}

func migrationWaitingPhase(migration *hav1alpha1.HomeAssistantMigration) (string, string) {
	if migration.Annotations["ha.homeassistant.io/migration-outcome"] == "unknown" {
		return "Unknown", "OutcomeUnknown"
	}
	approvals := make(map[string]bool, len(migration.Spec.Approvals))
	var restoreApproval *hav1alpha1.MigrationApproval
	var backupApproval *hav1alpha1.MigrationApproval
	for _, approval := range migration.Spec.Approvals {
		if _, exists := approvals[approval.ID]; exists {
			return "Blocked", "DuplicateApproval"
		}
		approvals[approval.ID] = approval.Approved
		if approval.ID == "restore" {
			copy := approval
			restoreApproval = &copy
		}
		if approval.ID == "backup" {
			copy := approval
			backupApproval = &copy
		}
	}
	if !approvals["backup"] {
		return "AwaitingBackupApproval", "AwaitingBackupApproval"
	}
	if !backupApprovalBound(migration, backupApproval) {
		return "Blocked", "BackupApprovalNotBound"
	}
	if migration.Spec.Archive == nil {
		ref := migration.Spec.SourceTokenSecretRef
		if ref == nil || strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.Key) == "" ||
			(ref.Optional != nil && *ref.Optional) {
			return "Blocked", "SourceTokenReferenceMissing"
		}
		return "Blocked", "BackupExecutorUnavailable"
	}
	if !approvals["restore"] {
		return "AwaitingRestoreApproval", "AwaitingRestoreApproval"
	}
	if !restoreApprovalBound(migration, restoreApproval) {
		return "Blocked", "RestoreApprovalNotBound"
	}
	if !approvals["source-stop"] {
		return "AwaitingSourceStop", "AwaitingSourceStop"
	}
	return "ReadyToExecute", "ExecutionReceiptRequired"
}

// The report identifier covers source identity, target and observed backup
// scope. A backup approval cannot be reused for a different report or target.
func backupApprovalBound(migration *hav1alpha1.HomeAssistantMigration, approval *hav1alpha1.MigrationApproval) bool {
	return approval != nil && approval.Approved && strings.TrimSpace(migration.Spec.ReportID) != "" &&
		strings.TrimSpace(migration.Spec.BackupScopeDigest) != "" &&
		migration.Spec.Target.UID != "" && approval.Digest == migration.Spec.ReportID &&
		approval.TargetUID == migration.Spec.Target.UID && approval.ScopeDigest == migration.Spec.BackupScopeDigest
}

func restoreApprovalBound(
	migration *hav1alpha1.HomeAssistantMigration,
	approval *hav1alpha1.MigrationApproval,
) bool {
	return approval != nil && approval.Approved && strings.TrimSpace(approval.Digest) != "" &&
		migration.Spec.Target.UID != "" &&
		migration.Spec.Archive != nil &&
		migration.Spec.Archive.BackupID != "" &&
		migration.Spec.Archive.AgentID != "" &&
		migration.Spec.Archive.Digest != "" &&
		migration.Spec.RestoreScopeDigest != "" &&
		approval.TargetUID == migration.Spec.Target.UID &&
		approval.ArchiveDigest == migration.Spec.Archive.Digest &&
		approval.ScopeDigest == migration.Spec.RestoreScopeDigest
}

func (r *HomeAssistantMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1alpha1.HomeAssistantMigration{}).
		Complete(r)
}
