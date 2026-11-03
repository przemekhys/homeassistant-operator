package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/coordination"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const backupOperationCondition = "Completed"

const backupObservationInterval = 5 * time.Second

// BackupKeyProtection is the narrowly scoped proof required before a native
// backup command can receive an encryption password. Implementations must not
// return a value unless the current key and required destinations are secured.
type BackupKeyProtection struct {
	Password string
	Verified bool
}

type BackupKeyProtectionProvider interface {
	CurrentBackupKey(
		context.Context,
		*hav1.HomeAssistant,
		*haclient.BackupConfig,
		[]string,
	) (BackupKeyProtection, error)
}

// RetainedBackupKeyProvider reads only the UID-bound retained key selected by
// observation. It deliberately refuses execution until destination protection
// is independently qualified; a retained key alone is not proof that every
// required destination will create an encrypted archive.
type RetainedBackupKeyProvider struct{ Reader client.Reader }

func (p RetainedBackupKeyProvider) CurrentBackupKey(
	ctx context.Context,
	target *hav1.HomeAssistant,
	config *haclient.BackupConfig,
	required []string,
) (BackupKeyProtection, error) {
	if target.Status.Backup == nil || target.Status.Backup.CurrentKeySecretRef == nil || config == nil ||
		len(required) == 0 {
		return BackupKeyProtection{}, fmt.Errorf("backup key or destination protection is unverified")
	}
	ref := target.Status.Backup.CurrentKeySecretRef
	secret := &corev1.Secret{}
	if err := p.Reader.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: ref.Name}, secret); err != nil {
		return BackupKeyProtection{}, fmt.Errorf("retained backup key is unavailable")
	}
	if string(secret.UID) != ref.UID || !isRetainedBackupKeySecret(secret, target.UID) ||
		ref.Key != backupKeySecretDataKey ||
		len(secret.Data[ref.Key]) == 0 {
		return BackupKeyProtection{}, fmt.Errorf("retained backup key is invalid")
	}
	for _, agentID := range required {
		agent, found := config.Agents[agentID]
		if !found || !agent.Protected {
			return BackupKeyProtection{}, fmt.Errorf("required backup destination protection is unverified")
		}
	}
	return BackupKeyProtection{Password: string(secret.Data[ref.Key]), Verified: true}, nil
}

// HomeAssistantBackupReconciler owns one immutable native backup operation.
// It records dispatch intent before calling Home Assistant, so a restart never
// turns an uncertain request into a second command.
type HomeAssistantBackupReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	APIReader      client.Reader
	NewHAClient    func(string) *haclient.Client
	KeyProtection  BackupKeyProtectionProvider
	CapabilityGate BackupCapabilityGate
	eventObservers sync.Map // map[types.UID]struct{}; bounds one observer per operation per process
}

func (r *HomeAssistantBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	operation := &hav1alpha1.HomeAssistantBackup{}
	if err := r.Get(ctx, req.NamespacedName, operation); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if isTerminalBackupPhase(operation.Status.Phase) {
		return ctrl.Result{}, nil
	}

	target := &hav1.HomeAssistant{}
	targetKey := types.NamespacedName{Namespace: operation.Namespace, Name: operation.Spec.HomeAssistantRef.Name}
	if err := r.Get(ctx, targetKey, target); err != nil {
		if apierrors.IsNotFound(err) {
			return r.waiting(
				ctx,
				operation,
				hav1alpha1.BackupPhaseValidating,
				"TargetNotFound",
				"Referenced HomeAssistant does not exist",
			)
		}
		return ctrl.Result{}, err
	}
	if string(target.UID) != operation.Spec.HomeAssistantRef.UID {
		return r.complete(
			ctx,
			operation,
			hav1alpha1.BackupPhaseFailed,
			"TargetUIDMismatch",
			"Referenced HomeAssistant has a different UID",
		)
	}
	if target.Spec.Synchronization != nil && target.Spec.Synchronization.Paused {
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseWaitingForGate,
			"SynchronizationPaused",
			"Backup dispatch is blocked while synchronization is paused",
		)
	}

	token, err := getAPIToken(ctx, r.Client, target)
	if err != nil {
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseValidating,
			"TokenNotAvailable",
			"Home Assistant API token is not available",
		)
	}
	haClient := newHAClientForHA(target, r.NewHAClient)
	if operation.Status.Phase == hav1alpha1.BackupPhaseRunning {
		r.observeBackupEvents(operation, haClient, token)
	}
	if operation.Status.Phase == hav1alpha1.BackupPhaseRunning && backupObservationDue(operation) {
		return r.observeBackup(ctx, operation, target, haClient, token)
	}
	gate := r.CapabilityGate
	if gate == nil {
		gate = NativeBackupCapabilityGate{Reader: r.APIReader}
	}
	if err := gate.AuthorizeBackup(ctx, target); err != nil {
		return r.waiting(ctx, operation, hav1alpha1.BackupPhaseValidating,
			"UnsupportedCapability", "Native backup generation requires a qualified running image")
	}
	return r.prepareBackup(ctx, operation, target, haClient, token)
}

// prepareBackup resolves and verifies inputs without granting dispatch authority.
func (r *HomeAssistantBackupReconciler) prepareBackup(
	ctx context.Context, operation *hav1alpha1.HomeAssistantBackup, target *hav1.HomeAssistant,
	haClient *haclient.Client, token string,
) (ctrl.Result, error) {
	config, err := haClient.GetBackupConfig(ctx, token)
	if err != nil {
		var backupErr *haclient.BackupError
		if errors.As(err, &backupErr) && backupErr.Kind == haclient.BackupErrorUnsupportedCapability {
			return r.complete(
				ctx,
				operation,
				hav1alpha1.BackupPhaseFailed,
				"UnsupportedCapability",
				"Home Assistant does not support native backups",
			)
		}
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseValidating,
			"ConfigurationUnavailable",
			"Native backup configuration is unavailable",
		)
	}
	agents := append([]string(nil), operation.Spec.AgentIDs...)
	if len(agents) == 0 {
		agents = append([]string(nil), config.CreateBackup.AgentIDs...)
	}
	if len(agents) == 0 {
		return r.complete(
			ctx,
			operation,
			hav1alpha1.BackupPhaseFailed,
			"NoBackupDestination",
			"No native backup destination is configured",
		)
	}
	available, err := haClient.ListBackupAgents(ctx, token)
	if err != nil {
		var backupErr *haclient.BackupError
		if errors.As(err, &backupErr) && backupErr.Kind == haclient.BackupErrorUnsupportedCapability {
			return r.complete(
				ctx,
				operation,
				hav1alpha1.BackupPhaseFailed,
				"UnsupportedCapability",
				"Home Assistant does not support native backup destinations",
			)
		}
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseValidating,
			"BackupDestinationUnavailable",
			"A required native backup destination is unavailable",
		)
	}
	if validateBackupAgents(agents, available) != nil {
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseValidating,
			"BackupDestinationUnavailable",
			"A required native backup destination is unavailable",
		)
	}
	if r.KeyProtection == nil {
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseValidating,
			"KeyProtectionUnavailable",
			"Backup key protection has not been verified",
		)
	}
	protection, err := r.KeyProtection.CurrentBackupKey(ctx, target, config, agents)
	if err != nil || !protection.Verified || protection.Password == "" {
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseValidating,
			"KeyProtectionUnavailable",
			"Backup key protection has not been verified",
		)
	}

	includeDatabase := config.CreateBackup.IncludeDatabase != nil && *config.CreateBackup.IncludeDatabase
	if operation.Spec.IncludeDatabase != nil {
		includeDatabase = *operation.Spec.IncludeDatabase
	}
	intent, err := json.Marshal(struct {
		TargetUID       string   `json:"targetUID"`
		AgentIDs        []string `json:"agentIDs"`
		IncludeDatabase bool     `json:"includeDatabase"`
		Name            string   `json:"name,omitempty"`
	}{string(target.UID), agents, includeDatabase, operation.Spec.Name})
	if err != nil {
		return ctrl.Result{}, err
	}
	request := haclient.GenerateBackupRequest{
		AgentIDs: agents, IncludeDatabase: includeDatabase, Name: operation.Spec.Name,
		Password: protection.Password, KeyProtectionVerified: true,
	}
	return r.dispatchBackup(ctx, operation, target, haClient, token, intent, request)
}

// dispatchBackup records each durable boundary before the single native command.
// Keeping the journal and permit transitions together makes their order explicit.
func (r *HomeAssistantBackupReconciler) dispatchBackup(
	ctx context.Context, operation *hav1alpha1.HomeAssistantBackup, target *hav1.HomeAssistant,
	haClient *haclient.Client, token string, intent []byte, request haclient.GenerateBackupRequest,
) (ctrl.Result, error) {
	store := r.store()
	if _, err := store.CreateRoot(ctx, operation.Namespace, target.UID); err != nil {
		return ctrl.Result{}, err
	}
	evidence, err := store.CreateEvidence(ctx, operation.Namespace, operation.UID, target.UID, intent)
	if err != nil {
		return ctrl.Result{}, err
	}
	operation.Status.EvidenceRef = coordination.EvidenceName(operation.UID)

	var journal coordination.Journal
	if len(evidence.Journal) > 0 && json.Unmarshal(evidence.Journal, &journal) == nil &&
		journal.Dispatch != coordination.DispatchNotPrepared {
		operation.Status.BackupJobID = journal.BackupJobID
		operation.Status.BackupID = journal.BackupID
		phase := hav1alpha1.BackupOperationPhase(journal.Result)
		if isTerminalBackupPhase(phase) && phase != hav1alpha1.BackupPhaseOutcomeUnknown && journal.BackupID != "" {
			return r.finishConfirmedBackup(ctx, operation, target, phase,
				"NativeBackupResultRecorded", "Native backup result was recovered from retained evidence")
		}
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseRunning,
			"DispatchRecorded",
			"Native backup dispatch was already recorded and is being observed",
		)
	}
	owner := string(operation.UID)
	permit, err := store.AcquirePermit(ctx, operation.Namespace, target.UID, owner, "Backup", operation.UID)
	if err != nil {
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseWaitingForGate,
			"PermitUnavailable",
			"Another operation holds the native backup gate",
		)
	}
	prepared := coordination.Journal{Dispatch: coordination.DispatchPrepared}
	if _, err := store.AdvanceJournal(ctx, operation.Namespace, operation.UID, prepared); err != nil {
		return ctrl.Result{}, err
	}
	flightErr := store.TransitionPermit(
		ctx, operation.Namespace, target.UID, owner, permit.ID, coordination.PermitInFlight,
	)
	if flightErr != nil {
		return ctrl.Result{}, flightErr
	}
	mayHaveBeenSent := coordination.Journal{Dispatch: coordination.DispatchMayHaveBeenSent}
	if _, err := store.AdvanceJournal(ctx, operation.Namespace, operation.UID, mayHaveBeenSent); err != nil {
		return ctrl.Result{}, err
	}
	result, err := haClient.GenerateBackup(ctx, token, request)
	if err != nil {
		var backupErr *haclient.BackupError
		if errors.As(err, &backupErr) && backupErr.Kind == haclient.BackupErrorBusyBeforeAcceptance {
			return r.waiting(
				ctx,
				operation,
				hav1alpha1.BackupPhaseWaitingForGate,
				"NativeBackupBusy",
				"Home Assistant is busy with another backup operation",
			)
		}
		if errors.As(err, &backupErr) && backupErr.Kind == haclient.BackupErrorUnsupportedCapability {
			return r.complete(
				ctx,
				operation,
				hav1alpha1.BackupPhaseFailed,
				"UnsupportedCapability",
				"Home Assistant does not support native backup generation",
			)
		}
		_ = store.TransitionPermit(ctx, operation.Namespace, target.UID, owner, permit.ID, coordination.PermitUnknown)
		return r.complete(
			ctx,
			operation,
			hav1alpha1.BackupPhaseOutcomeUnknown,
			"DispatchOutcomeUnknown",
			"The native backup command may have been accepted",
		)
	}
	acknowledged := coordination.Journal{Dispatch: coordination.DispatchAcknowledged, BackupJobID: result.BackupJobID}
	if _, err := store.AdvanceJournal(ctx, operation.Namespace, operation.UID, acknowledged); err != nil {
		return ctrl.Result{}, err
	}
	operation.Status.BackupJobID = result.BackupJobID
	if err := r.recordBackupAttempt(ctx, target); err != nil {
		return ctrl.Result{}, err
	}
	resultState, err := r.waiting(
		ctx,
		operation,
		hav1alpha1.BackupPhaseRunning,
		"NativeBackupAccepted",
		"Native backup request was accepted and is being observed",
	)
	if err == nil {
		r.observeBackupEvents(operation, haClient, token)
	}
	return resultState, err
}

// observeBackup polls the details for the native job after the accepted request
// has had time to produce an archive. Backup events may be used by a caller to
// associate a job with its archive; details are still authoritative for the
// per-destination result.
func (r *HomeAssistantBackupReconciler) observeBackup(
	ctx context.Context,
	operation *hav1alpha1.HomeAssistantBackup,
	target *hav1.HomeAssistant,
	haClient *haclient.Client,
	token string,
) (ctrl.Result, error) {
	archiveID := operation.Status.BackupID
	if archiveID == "" && operation.Status.BackupJobID != "" {
		if resolver, ok := r.CapabilityGate.(BackupArchiveResolver); ok {
			// Native events need not carry operation IDs. Only an explicitly
			// qualified Container job/archive contract may supply this lookup.
			archiveID, _ = resolver.ArchiveForBackupJob(ctx, target, operation.Status.BackupJobID)
		}
	}
	if archiveID == "" {
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseRunning,
			"AwaitingBackupArchive",
			"Native backup is awaiting correlated archive metadata",
		)
	}
	idle, err := haClient.IsBackupManagerIdle(ctx, token)
	if err != nil || !idle {
		return r.waiting(ctx, operation, hav1alpha1.BackupPhaseRunning,
			"NativeBackupRunning", "Waiting for the native backup manager to finish pending work")
	}
	details, err := haClient.GetBackupDetails(ctx, token, archiveID)
	if err != nil {
		var backupErr *haclient.BackupError
		if errors.As(err, &backupErr) &&
			(backupErr.Kind == haclient.BackupErrorArchiveNotFound || backupErr.Kind == haclient.BackupErrorSourceUnavailable) {
			return r.waiting(
				ctx,
				operation,
				hav1alpha1.BackupPhaseRunning,
				"AwaitingNativeBackup",
				"Native backup is still being observed",
			)
		}
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseRunning,
			"BackupObservationUnavailable",
			"Native backup outcome is not yet available",
		)
	}
	if len(details.Agents) == 0 {
		return r.waiting(
			ctx,
			operation,
			hav1alpha1.BackupPhaseRunning,
			"AwaitingDestinationResults",
			"Native backup has not reported destination results",
		)
	}

	required, err := r.requiredBackupAgents(ctx, operation)
	if err != nil {
		return ctrl.Result{}, err
	}
	phase, reason, message, _ := requiredDestinationOutcome(required, details.Agents)
	operation.Status.BackupID = details.BackupID
	journal := coordination.Journal{
		Dispatch: coordination.DispatchAcknowledged, BackupJobID: operation.Status.BackupJobID,
		BackupID: details.BackupID, Result: string(phase),
	}
	if _, err := r.store().AdvanceJournal(ctx, operation.Namespace, operation.UID, journal); err != nil {
		return ctrl.Result{}, err
	}
	return r.finishConfirmedBackup(ctx, operation, target, phase, reason, message)
}

func (r *HomeAssistantBackupReconciler) finishConfirmedBackup(
	ctx context.Context, operation *hav1alpha1.HomeAssistantBackup, target *hav1.HomeAssistant,
	phase hav1alpha1.BackupOperationPhase, reason, message string,
) (ctrl.Result, error) {
	store := r.store()
	root, err := store.GetRoot(ctx, operation.Namespace, target.UID)
	if err != nil {
		return ctrl.Result{}, err
	}
	permit := root.ActivePermit
	if permit != nil && permit.OperationUID == string(operation.UID) && permit.Class == "Backup" {
		if permit.State == coordination.PermitInFlight {
			if err := store.TransitionPermit(ctx, operation.Namespace, target.UID,
				permit.Owner, permit.ID, coordination.PermitConfirmed); err != nil {
				return ctrl.Result{}, err
			}
		}
		// ReleasePermit refuses unknown or merely prepared work. A persisted
		// native archive result never grants permission to release another writer.
		if err := store.ReleasePermit(ctx, operation.Namespace, target.UID, permit.Owner, permit.ID); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.recordBackupOutcome(ctx, target, phase == hav1alpha1.BackupPhaseSucceeded); err != nil {
		return ctrl.Result{}, err
	}
	return r.complete(ctx, operation, phase, reason, message)
}

func isTerminalBackupPhase(phase hav1alpha1.BackupOperationPhase) bool {
	return phase == hav1alpha1.BackupPhaseSucceeded || phase == hav1alpha1.BackupPhasePartiallySucceeded ||
		phase == hav1alpha1.BackupPhaseFailed ||
		phase == hav1alpha1.BackupPhaseOutcomeUnknown
}

func backupObservationDue(operation *hav1alpha1.HomeAssistantBackup) bool {
	condition := meta.FindStatusCondition(operation.Status.Conditions, backupOperationCondition)
	return condition != nil && time.Since(condition.LastTransitionTime.Time) >= backupObservationInterval
}

func (r *HomeAssistantBackupReconciler) requiredBackupAgents(
	ctx context.Context,
	operation *hav1alpha1.HomeAssistantBackup,
) ([]string, error) {
	evidence, err := r.store().GetEvidence(ctx, operation.Namespace, operation.UID)
	if err != nil {
		return nil, err
	}
	var intent struct {
		AgentIDs []string `json:"agentIDs"`
	}
	if err := json.Unmarshal(evidence.Intent, &intent); err != nil {
		return nil, fmt.Errorf("decode backup operation intent: %w", err)
	}
	if len(intent.AgentIDs) == 0 {
		return nil, fmt.Errorf("backup operation intent has no required destinations")
	}
	return intent.AgentIDs, nil
}

// backupEventMatches correlates a native event only when HA included the job
// identifier returned by GenerateBackup. Names and timestamps are not safe
// correlation keys and must never be used to infer a result.
func backupEventMatches(operation *hav1alpha1.HomeAssistantBackup, event haclient.BackupEvent) bool {
	return operation.Status.BackupJobID != "" && event.BackupJobID != "" &&
		operation.Status.BackupJobID == event.BackupJobID
}

// observeBackupEvents is intentionally only an accelerator: restart-safe
// progress still comes from reconnecting and polling the correlated archive.
// It never dispatches a native command and only accepts an archive identifier
// from an event bearing the exact accepted job identifier.
func (r *HomeAssistantBackupReconciler) observeBackupEvents(
	operation *hav1alpha1.HomeAssistantBackup,
	haClient *haclient.Client,
	token string,
) {
	if operation.Status.BackupJobID == "" {
		return
	}
	if _, loaded := r.eventObservers.LoadOrStore(operation.UID, struct{}{}); loaded {
		return
	}
	go func() {
		defer r.eventObservers.Delete(operation.UID)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		_ = haClient.SubscribeBackupEvents(ctx, token, func(event haclient.BackupEvent) {
			if event.BackupID == "" {
				return
			}
			current := &hav1alpha1.HomeAssistantBackup{}
			key := types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name}
			if err := r.Get(ctx, key, current); err != nil ||
				isTerminalBackupPhase(current.Status.Phase) ||
				!backupEventMatches(current, event) {
				return
			}
			if current.Status.BackupID == event.BackupID {
				return
			}
			current.Status.BackupID = event.BackupID
			_ = r.Status().Update(ctx, current)
		})
	}()
}

func requiredDestinationOutcome(
	required []string,
	reported []haclient.BackupDestinationResult,
) (hav1alpha1.BackupOperationPhase, string, string, bool) {
	byAgent := make(map[string]haclient.BackupDestinationResult, len(reported))
	for _, result := range reported {
		byAgent[result.AgentID] = result
	}
	succeeded := 0
	for _, agentID := range required {
		result, found := byAgent[agentID]
		if found && result.Error == "" && result.Protected {
			succeeded++
		}
	}
	if succeeded == len(required) && len(required) != 0 {
		return hav1alpha1.BackupPhaseSucceeded, "RequiredDestinationsSucceeded",
			"Native backup completed at every required destination", true
	}
	if succeeded > 0 {
		return hav1alpha1.BackupPhasePartiallySucceeded, "RequiredDestinationFailed",
			"Native backup did not complete at every required destination", false
	}
	return hav1alpha1.BackupPhaseFailed, "RequiredDestinationsFailed",
		"Native backup did not complete at any required destination", false
}

func (r *HomeAssistantBackupReconciler) recordBackupAttempt(ctx context.Context, target *hav1.HomeAssistant) error {
	now := metav1.Now()
	if target.Status.Backup == nil {
		target.Status.Backup = &hav1.BackupStatus{}
	}
	target.Status.Backup.LastAttempt = &now
	setBackupHealth(
		target,
		metav1.ConditionUnknown,
		"BackupOutcomePending",
		"Native backup was accepted and is awaiting destination results",
	)
	return r.Status().Update(ctx, target)
}

func (r *HomeAssistantBackupReconciler) recordBackupOutcome(
	ctx context.Context,
	target *hav1.HomeAssistant,
	succeeded bool,
) error {
	if target.Status.Backup == nil {
		target.Status.Backup = &hav1.BackupStatus{}
	}
	if succeeded {
		now := metav1.Now()
		target.Status.Backup.LastSuccess = &now
		setBackupHealth(
			target,
			metav1.ConditionTrue,
			"RequiredDestinationsSucceeded",
			"Native backup completed at every required destination",
		)
	} else {
		setBackupHealth(target, metav1.ConditionFalse, "RequiredDestinationFailed",
			"Native backup did not complete at every required destination")
	}
	return r.Status().Update(ctx, target)
}

func (r *HomeAssistantBackupReconciler) store() coordination.Store {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	return coordination.Store{Reader: reader, Client: r.Client}
}

func (r *HomeAssistantBackupReconciler) waiting(
	ctx context.Context,
	operation *hav1alpha1.HomeAssistantBackup,
	phase hav1alpha1.BackupOperationPhase,
	reason, message string,
) (ctrl.Result, error) {
	operation.Status.Phase = phase
	operation.Status.ObservedGeneration = operation.Generation
	meta.SetStatusCondition(
		&operation.Status.Conditions,
		metav1.Condition{
			Type:               backupOperationCondition,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: operation.Generation,
			Reason:             reason,
			Message:            message,
		},
	)
	return ctrl.Result{RequeueAfter: 10 * time.Second}, r.Status().Update(ctx, operation)
}

func (r *HomeAssistantBackupReconciler) complete(
	ctx context.Context,
	operation *hav1alpha1.HomeAssistantBackup,
	phase hav1alpha1.BackupOperationPhase,
	reason, message string,
) (ctrl.Result, error) {
	operation.Status.Phase = phase
	operation.Status.ObservedGeneration = operation.Generation
	meta.SetStatusCondition(
		&operation.Status.Conditions,
		metav1.Condition{
			Type:               backupOperationCondition,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: operation.Generation,
			Reason:             reason,
			Message:            message,
		},
	)
	return ctrl.Result{}, r.Status().Update(ctx, operation)
}

func (r *HomeAssistantBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Scheme == nil {
		return fmt.Errorf("HomeAssistantBackup reconciler requires a scheme")
	}
	return ctrl.NewControllerManagedBy(mgr).For(&hav1alpha1.HomeAssistantBackup{}).Complete(r)
}
