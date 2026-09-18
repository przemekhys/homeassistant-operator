/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/communityrepo"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const (
	communityRepositoryFinalizerName     = "ha.homeassistant.io/community-repository-finalizer"
	communityRepositoryHashAnnotationKey = "ha.homeassistant.io/community-repository-hash"

	communityRepositoriesConfigMapSuffix = "-community-repositories"
	communityRepositoriesConfigMapKey    = "repositories.json"

	// Condition reasons for the Ready condition.
	reasonRepoValidating            = "Validating"
	reasonRepoUnreachable           = "RepositoryUnreachable"
	reasonRepoCategoryMismatch      = "CategoryMismatch"
	reasonRepoStructureInvalid      = "StructureInvalid"
	reasonRepoTargetConflict        = "TargetConflict"
	reasonRepoInstalling            = "Installing"
	reasonRepoMaterializing         = "Materializing"
	reasonRepoMaterializationFailed = "MaterializationFailed"
	reasonRepoActivationTimeout     = "ActivationTimeout"
	reasonRepoInstalled             = "Installed"
	reasonRepoHomeAssistantNotReady = "HomeAssistantNotReady"

	// Event reasons.
	eventRepositoryValidated     = "RepositoryValidated"
	eventRepositoryInstalled     = "RepositoryInstalled"
	eventRepositoryInstallFailed = "RepositoryInstallFailed"
	eventRepositoryConflict      = "RepositoryConflict"
	eventRepositoryRemoved       = "RepositoryRemoved"

	// activationRetryInterval/activationRetryWindow: 6 attempts x 20s (~2 min)
	// before giving up with ActivationTimeout.
	activationRetryInterval = 20 * time.Second
	activationRetryWindow   = 6 * activationRetryInterval

	integrationMaterializationPollInterval = 5 * time.Second
)

// activationSettleDelay is how long reconcileInstalling waits, from entering
// Installing, before attempting any hot-reload category's first activation call —
// see the call site's comment for why this exists. A var (not const) so envtest
// specs — which call Reconcile back-to-back with no real wall-clock delay between
// calls — can set it to 0 and exercise activation immediately.
var activationSettleDelay = 90 * time.Second

// communityRepositoryConfigMapEntry is one entry in the aggregate ConfigMap's
// repositories.json.
type communityRepositoryConfigMapEntry struct {
	Category       string `json:"category"`
	Repository     string `json:"repository"`
	Ref            string `json:"ref"`
	ResolvedTarget string `json:"resolvedTarget"`
	SourcePath     string `json:"sourcePath"`
}

type communityRepositoriesConfigMapPayload struct {
	Repositories []communityRepositoryConfigMapEntry `json:"repositories"`
}

// HomeAssistantCommunityRepositoryReconciler reconciles a HomeAssistantCommunityRepository object.
type HomeAssistantCommunityRepositoryReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Recorder    events.EventRecorder
	NewHAClient func(baseURL string) *haclient.Client // overridable for testing
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantcommunityrepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantcommunityrepositories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantcommunityrepositories/finalizers,verbs=update
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop.
func (r *HomeAssistantCommunityRepositoryReconciler) Reconcile(
	ctx context.Context, req ctrl.Request,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	repo := &hav1alpha1.HomeAssistantCommunityRepository{}
	if err := r.Get(ctx, req.NamespacedName, repo); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// --- DELETION ---
	if !repo.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(repo, communityRepositoryFinalizerName) {
			complete, err := r.handleDeletion(ctx, repo)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !complete {
				return ctrl.Result{RequeueAfter: integrationMaterializationPollInterval}, nil
			}
			controllerutil.RemoveFinalizer(repo, communityRepositoryFinalizerName)
			if err := r.Update(ctx, repo); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// --- FINALIZER ---
	if !controllerutil.ContainsFinalizer(repo, communityRepositoryFinalizerName) {
		controllerutil.AddFinalizer(repo, communityRepositoryFinalizerName)
		if err := r.Update(ctx, repo); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// --- FETCH HomeAssistant CR ---
	haRef := types.NamespacedName{Name: repo.Spec.HomeAssistantRef.Name, Namespace: repo.Namespace}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		log.Info("HomeAssistant not found, requeueing", "name", haRef.Name)
		return r.setPhase(ctx, repo, hav1alpha1.PhasePending, reasonRepoHomeAssistantNotReady,
			fmt.Sprintf("HomeAssistant %s not found", haRef.Name), 5*time.Second)
	}

	switch repo.Status.Phase {
	case hav1alpha1.PhaseInstalling:
		return r.reconcileInstalling(ctx, ha, repo)
	case hav1alpha1.PhaseInstalled:
		if repo.Status.ObservedGeneration != repo.Generation {
			return r.reconcileValidating(ctx, ha, repo)
		}
		return ctrl.Result{}, nil
	case hav1alpha1.PhaseFailed:
		cond := meta.FindStatusCondition(repo.Status.Conditions, conditionTypeReady)
		// RepositoryUnreachable and TargetConflict are potentially transient
		// (network recovers, or a conflicting sibling is removed/changed) — retry
		// them even without a spec change. Other Failed reasons (CategoryMismatch,
		// StructureInvalid) only change if the user edits spec.
		retryable := cond != nil && (cond.Reason == reasonRepoUnreachable || cond.Reason == reasonRepoTargetConflict)
		if repo.Status.ObservedGeneration != repo.Generation || retryable {
			return r.reconcileValidating(ctx, ha, repo)
		}
		return ctrl.Result{}, nil
	default: // "", Pending, Validating
		return r.reconcileValidating(ctx, ha, repo)
	}
}

// reconcileValidating fetches the source repository, validates its HACS structure
// against the requested category, checks for a conflicting sibling, and (on success)
// writes the aggregate ConfigMap entry and transitions to Installing.
//
// The first call for a given generation only persists Phase=Validating and returns,
// so the (possibly slow) network fetch happens on the following reconcile — this
// keeps status.phase observable as its own transient step in the state machine
// rather than silently skipping over it within a single reconcile.
func (r *HomeAssistantCommunityRepositoryReconciler) reconcileValidating(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	repo *hav1alpha1.HomeAssistantCommunityRepository,
) (ctrl.Result, error) {
	if repo.Status.Phase != hav1alpha1.PhaseValidating {
		return r.setPhase(ctx, repo, hav1alpha1.PhaseValidating, reasonRepoValidating,
			"Fetching and validating repository", 0)
	}

	extracted, err := communityrepo.FetchTarball(ctx, repo.Spec.Repository, repo.Spec.Ref)
	if err != nil {
		return r.setPhase(ctx, repo, hav1alpha1.PhaseFailed, reasonRepoUnreachable, err.Error(), 30*time.Second)
	}

	resolved, err := communityrepo.ValidateAndResolve(extracted, communityrepo.Category(repo.Spec.Category))
	if err != nil {
		reason := reasonRepoStructureInvalid
		if errors.Is(err, communityrepo.ErrCategoryMismatch) {
			reason = reasonRepoCategoryMismatch
		}
		return r.setPhase(ctx, repo, hav1alpha1.PhaseFailed, reason, err.Error(), 0)
	}

	conflictOwner, err := r.findConflictingOwner(ctx, repo, ha.Name, resolved)
	if err != nil {
		return ctrl.Result{}, err
	}
	if conflictOwner != nil {
		msg := fmt.Sprintf("target %q (category %s) is already installed by %s",
			resolved.ResolvedTarget, repo.Spec.Category, conflictOwner.Name)
		r.emitEvent(repo, corev1.EventTypeWarning, eventRepositoryConflict, msg)
		return r.setPhase(ctx, repo, hav1alpha1.PhaseFailed, reasonRepoTargetConflict, msg, 0)
	}

	previousResolvedTarget := repo.Status.ResolvedTarget
	repo.Status.ResolvedTarget = resolved.ResolvedTarget
	// Persist ResolvedTarget durably before mutating the shared aggregate ConfigMap.
	// Ordering matters for crash recovery: if the reconciler were to restart between
	// the ConfigMap write and the status write, an update-in-place (this field
	// changing on an already-Installed resource) could otherwise leave the
	// ConfigMap holding a new entry while this CR's own durable status still
	// reports the old target — the next reconcile would then compute the wrong
	// previousResolvedTarget and fail to clean up the stale ConfigMap key.
	if err := r.Status().Update(ctx, repo); err != nil {
		return ctrl.Result{}, err
	}

	r.emitEvent(repo, corev1.EventTypeNormal, eventRepositoryValidated,
		fmt.Sprintf("Repository validated, resolved target %q", resolved.ResolvedTarget))

	if _, err := r.upsertConfigMapEntry(ctx, ha, repo, resolved, previousResolvedTarget); err != nil {
		return ctrl.Result{}, err
	}

	return r.setPhase(ctx, repo, hav1alpha1.PhaseInstalling, reasonRepoInstalling,
		fmt.Sprintf("Installing %s %q", repo.Spec.Category, resolved.ResolvedTarget), 0)
}

// reconcileInstalling performs (and retries) category-specific activation in Home
// Assistant.
func (r *HomeAssistantCommunityRepositoryReconciler) reconcileInstalling(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	repo *hav1alpha1.HomeAssistantCommunityRepository,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if repo.Spec.Category == hav1alpha1.CategoryIntegration {
		content, err := r.currentConfigMapContent(ctx, ha, repo.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if _, err := r.bumpStatefulSetHash(ctx, ha, content); err != nil {
			return ctrl.Result{}, err
		}

		desiredHash := calculateIntegrationRepositoryHash(content)
		confirmed, reason, message, err := r.integrationMaterializationConfirmed(ctx, ha, desiredHash)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !confirmed {
			return r.setPhase(ctx, repo, hav1alpha1.PhaseInstalling, reason, message,
				integrationMaterializationPollInterval)
		}
		return r.markInstalled(ctx, repo,
			fmt.Sprintf("integration %q materialized and loaded by Home Assistant", repo.Status.ResolvedTarget))
	}

	if r.activationTimedOut(repo) {
		return r.setPhase(ctx, repo, hav1alpha1.PhaseFailed, reasonRepoActivationTimeout,
			fmt.Sprintf("activation of %q did not confirm within the retry window", repo.Status.ResolvedTarget), 0)
	}

	// Home Assistant's reload_themes/python_script.reload/reload_custom_templates
	// services all return success unconditionally, whether or not the target file
	// actually exists yet — they are not a real confirmation signal by themselves.
	// The operator has no way to check the pod's filesystem directly (no pods/exec),
	// so the only available mitigation is not calling reload at all until the
	// ConfigMap has plausibly finished propagating to the pod's mounted volume
	// (kubelet's periodic sync, ~60-90s) and the sidecar has had a poll cycle
	// (~30s). Calling reload before that risks a false "Installed": the reload API
	// call succeeds and Ready flips True even while the file does not exist yet on
	// the pod's filesystem.
	if elapsed := r.installingElapsed(repo); elapsed < activationSettleDelay {
		return ctrl.Result{RequeueAfter: activationSettleDelay - elapsed}, nil
	}

	token, tokenErr := getAPIToken(ctx, r.Client, ha)
	if tokenErr != nil {
		log.Info("API token not available yet, will retry activation")
		return ctrl.Result{RequeueAfter: activationRetryInterval}, nil
	}
	haClient := newHAClientForHA(ha, r.NewHAClient)

	var activationErr error
	switch repo.Spec.Category {
	case hav1alpha1.CategoryTheme:
		activationErr = haClient.ReloadThemes(ctx, token)
	case hav1alpha1.CategoryPythonScript:
		activationErr = haClient.ReloadPythonScripts(ctx, token)
	case hav1alpha1.CategoryTemplate:
		activationErr = haClient.ReloadCustomTemplates(ctx, token)
	case hav1alpha1.CategoryPlugin:
		// The sidecar materializes plugin files under /config/www/community/, which
		// Home Assistant serves at the "/local/community/" URL prefix. Only a
		// single ".js" file per plugin is currently supported (internal/communityrepo
		// resolvePlugin), so the URL is deterministic from resolvedTarget alone —
		// no need to persist sourcePath in status just for this.
		resourceURL := fmt.Sprintf("/local/community/%s.js", repo.Status.ResolvedTarget)
		activationErr = haClient.RegisterLovelaceResource(ctx, token, resourceURL, "module")
		if errors.Is(activationErr, haclient.ErrLovelaceYAMLMode) {
			return r.markInstalled(ctx, repo, fmt.Sprintf(
				"plugin %q installed; dashboards are YAML-managed so the Lovelace resource must be registered manually",
				repo.Status.ResolvedTarget))
		}
	default:
		return ctrl.Result{}, fmt.Errorf("unsupported category %q", repo.Spec.Category)
	}

	if activationErr != nil {
		log.Info("Activation not yet confirmed, will retry",
			"category", repo.Spec.Category, "target", repo.Status.ResolvedTarget, "error", activationErr)
		return ctrl.Result{RequeueAfter: activationRetryInterval}, nil
	}

	return r.markInstalled(ctx, repo,
		fmt.Sprintf("%s %q installed and reload confirmed", repo.Spec.Category, repo.Status.ResolvedTarget))
}

// integrationMaterializationConfirmed waits for the StatefulSet pod carrying the
// exact desired repository hash to report a successful init container and become
// Ready. The init container's termination message is the hash it materialized,
// making an older pod or an optional/missing ConfigMap projection unable to
// acknowledge newer desired content.
func (r *HomeAssistantCommunityRepositoryReconciler) integrationMaterializationConfirmed(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	desiredHash string,
) (bool, string, string, error) {
	pod := &corev1.Pod{}
	podKey := types.NamespacedName{Name: ha.Name + "-0", Namespace: ha.Namespace}
	if err := r.Get(ctx, podKey, pod); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, reasonRepoMaterializing,
				fmt.Sprintf("Waiting for Home Assistant pod %s to materialize integrations", podKey.Name), nil
		}
		return false, "", "", err
	}

	if pod.Annotations[communityRepositoryHashAnnotationKey] != desiredHash {
		return false, reasonRepoMaterializing,
			fmt.Sprintf("Waiting for Home Assistant pod %s with repository configuration %s", pod.Name, desiredHash), nil
	}

	var initStatus *corev1.ContainerStatus
	for i := range pod.Status.InitContainerStatuses {
		if pod.Status.InitContainerStatuses[i].Name == communityRepositoryInitContainerName {
			initStatus = &pod.Status.InitContainerStatuses[i]
			break
		}
	}
	if initStatus == nil {
		return false, reasonRepoMaterializing,
			fmt.Sprintf("Waiting for integration materializer in Home Assistant pod %s", pod.Name), nil
	}

	terminated := initStatus.State.Terminated
	if terminated == nil && initStatus.LastTerminationState.Terminated != nil &&
		initStatus.LastTerminationState.Terminated.ExitCode != 0 {
		terminated = initStatus.LastTerminationState.Terminated
	}
	if terminated != nil && terminated.ExitCode != 0 {
		detail := terminated.Message
		if detail == "" {
			detail = terminated.Reason
		}
		if detail == "" {
			detail = fmt.Sprintf("exit code %d", terminated.ExitCode)
		}
		return false, reasonRepoMaterializationFailed,
			fmt.Sprintf("Integration materialization failed in pod %s: %s; Kubernetes will retry", pod.Name, detail), nil
	}
	if terminated == nil || terminated.ExitCode != 0 || terminated.Message != desiredHash {
		return false, reasonRepoMaterializing,
			fmt.Sprintf("Waiting for integration materializer in Home Assistant pod %s", pod.Name), nil
	}

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true, reasonRepoInstalling, "", nil
		}
	}
	return false, reasonRepoHomeAssistantNotReady,
		fmt.Sprintf("Integrations are materialized; waiting for Home Assistant pod %s to become Ready", pod.Name), nil
}

// installingElapsed returns how long the Installing phase has been active, or 0 if
// not currently in it (status.installingSince is unset). Anchored on this explicit
// timestamp rather than the Ready condition's LastTransitionTime: meta.SetStatusCondition
// only bumps LastTransitionTime when Status changes, not Reason, so a Validating ->
// Installing transition (both report ConditionFalse) would leave it unchanged —
// anchoring on it would make the retry/settle window start from whenever the
// condition's Status last flipped, which can be arbitrarily earlier than actually
// entering Installing.
func (r *HomeAssistantCommunityRepositoryReconciler) installingElapsed(
	repo *hav1alpha1.HomeAssistantCommunityRepository,
) time.Duration {
	if repo.Status.Phase != hav1alpha1.PhaseInstalling || repo.Status.InstallingSince == nil {
		return 0
	}
	return time.Since(repo.Status.InstallingSince.Time)
}

// activationTimedOut reports whether the Installing phase has been retrying
// activation for longer than activationRetryWindow. The budget is measured after
// activationSettleDelay, not from InstallingSince directly: both are anchored on
// the same timestamp, so without this offset the settle wait would eat into the
// retry window and leave far fewer than the documented 6 attempts to actually
// confirm activation.
func (r *HomeAssistantCommunityRepositoryReconciler) activationTimedOut(
	repo *hav1alpha1.HomeAssistantCommunityRepository,
) bool {
	return r.installingElapsed(repo) > activationSettleDelay+activationRetryWindow
}

// markInstalled records a successfully activated installation.
func (r *HomeAssistantCommunityRepositoryReconciler) markInstalled(
	ctx context.Context,
	repo *hav1alpha1.HomeAssistantCommunityRepository,
	message string,
) (ctrl.Result, error) {
	repo.Status.InstalledVersion = repo.Spec.Ref
	r.emitEvent(repo, corev1.EventTypeNormal, eventRepositoryInstalled, message)
	return r.setPhase(ctx, repo, hav1alpha1.PhaseInstalled, reasonRepoInstalled, message, 0)
}

// findConflictingOwner returns the sibling HomeAssistantCommunityRepository (if
// any, other than repo itself) already occupying the same conflict key — see
// internal/communityrepo.ConflictKey.
func (r *HomeAssistantCommunityRepositoryReconciler) findConflictingOwner(
	ctx context.Context,
	repo *hav1alpha1.HomeAssistantCommunityRepository,
	haName string,
	resolved communityrepo.Resolved,
) (*types.NamespacedName, error) {
	list := &hav1alpha1.HomeAssistantCommunityRepositoryList{}
	if err := r.List(ctx, list, client.InNamespace(repo.Namespace)); err != nil {
		return nil, err
	}

	myKey := communityrepo.NewConflictKey(haName, communityrepo.Category(repo.Spec.Category), resolved)
	for _, item := range list.Items {
		if item.Name == repo.Name {
			continue
		}
		// A sibling already being deleted is on its way out — its finalizer will
		// release the target shortly (best-effort). Treating it as still occupying
		// the key would needlessly reject a new CR that's about to become valid.
		if !item.DeletionTimestamp.IsZero() {
			continue
		}
		if item.Status.Phase != hav1alpha1.PhaseInstalled && item.Status.Phase != hav1alpha1.PhaseInstalling {
			continue
		}
		itemKey := communityrepo.ConflictKey{
			HomeAssistantName: item.Spec.HomeAssistantRef.Name,
			Category:          communityrepo.Category(item.Spec.Category),
			ResolvedTarget:    item.Status.ResolvedTarget,
		}
		if itemKey == myKey {
			owner := types.NamespacedName{Name: item.Name, Namespace: item.Namespace}
			return &owner, nil
		}
	}
	return nil, nil
}

// handleDeletion removes this repository's entry from the aggregate ConfigMap.
// Integration deletion keeps the finalizer until the restart carrying the empty or
// reduced integration set has completed, so the init container remains injected
// long enough to remove its owned destination from the PVC.
func (r *HomeAssistantCommunityRepositoryReconciler) handleDeletion(
	ctx context.Context,
	repo *hav1alpha1.HomeAssistantCommunityRepository,
) (bool, error) {
	log := logf.FromContext(ctx)
	if repo.Status.ResolvedTarget == "" {
		// Never got far enough to be materialized into the ConfigMap.
		return true, nil
	}

	haRef := types.NamespacedName{Name: repo.Spec.HomeAssistantRef.Name, Namespace: repo.Namespace}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		log.Info("HomeAssistant not found during deletion, skipping cleanup (best-effort)")
		return true, nil
	}

	content, err := r.removeConfigMapEntry(ctx, ha, repo.Namespace, string(repo.Spec.Category), repo.Status.ResolvedTarget)
	if err != nil {
		return false, err
	}
	if repo.Spec.Category == hav1alpha1.CategoryIntegration {
		statefulSetExists, err := r.bumpStatefulSetHash(ctx, ha, content)
		if err != nil {
			return false, err
		}
		if !statefulSetExists {
			if !ha.DeletionTimestamp.IsZero() {
				log.Info("Home Assistant is being deleted, skipping PVC cleanup (best-effort)")
				return true, nil
			}
			return false, nil
		}
		confirmed, reason, _, err := r.integrationMaterializationConfirmed(
			ctx, ha, calculateIntegrationRepositoryHash(content))
		if err != nil {
			return false, err
		}
		// A successful init-container termination confirms PVC cleanup. Deletion
		// must not remain blocked by an unrelated Home Assistant startup failure.
		if !confirmed && reason != reasonRepoHomeAssistantNotReady {
			return false, nil
		}
	}

	r.emitEvent(repo, corev1.EventTypeNormal, eventRepositoryRemoved,
		fmt.Sprintf("Removed %s %q from Home Assistant", repo.Spec.Category, repo.Status.ResolvedTarget))
	return true, nil
}

// --- Aggregate ConfigMap helpers ---

func communityRepositoriesConfigMapName(haName string) string {
	return haName + communityRepositoriesConfigMapSuffix
}

// loadConfigMap fetches the aggregate ConfigMap for ha (creating an in-memory,
// not-yet-persisted one owned by ha if absent) along with its decoded entries.
func (r *HomeAssistantCommunityRepositoryReconciler) loadConfigMap(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	namespace string,
) (*corev1.ConfigMap, map[string]communityRepositoryConfigMapEntry, error) {
	cm := &corev1.ConfigMap{}
	cmKey := types.NamespacedName{Name: communityRepositoriesConfigMapName(ha.Name), Namespace: namespace}
	err := r.Get(ctx, cmKey, cm)
	switch {
	case err == nil:
		return cm, decodeConfigMapEntries(ctx, cm), nil
	case k8serrors.IsNotFound(err):
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmKey.Name, Namespace: namespace}}
		if err := controllerutil.SetControllerReference(ha, cm, r.Scheme); err != nil {
			return nil, nil, err
		}
		return cm, map[string]communityRepositoryConfigMapEntry{}, nil
	default:
		return nil, nil, err
	}
}

// upsertConfigMapEntry adds or updates this repository's entry, preserving all
// sibling entries already present, and returns the resulting repositories.json content.
// previousResolvedTarget is this same CR's own resolvedTarget as of its last
// successful validation (empty if this is its first). It is normally identical to
// resolved.ResolvedTarget, but a ref bump can change what a repository resolves to
// (e.g. a renamed integration domain) — when it does, the stale entry under the old
// key must be removed here, or it would otherwise leak in the ConfigMap forever
// (nothing else ever revisits an old key once resolvedTarget moves on).
func (r *HomeAssistantCommunityRepositoryReconciler) upsertConfigMapEntry(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	repo *hav1alpha1.HomeAssistantCommunityRepository,
	resolved communityrepo.Resolved,
	previousResolvedTarget string,
) (string, error) {
	cm, entries, err := r.loadConfigMap(ctx, ha, repo.Namespace)
	if err != nil {
		return "", err
	}
	if previousResolvedTarget != "" && previousResolvedTarget != resolved.ResolvedTarget {
		delete(entries, entryKey(string(repo.Spec.Category), previousResolvedTarget))
	}
	entries[entryKey(string(repo.Spec.Category), resolved.ResolvedTarget)] = communityRepositoryConfigMapEntry{
		Category:       string(repo.Spec.Category),
		Repository:     repo.Spec.Repository,
		Ref:            repo.Spec.Ref,
		ResolvedTarget: resolved.ResolvedTarget,
		SourcePath:     resolved.SourcePath,
	}
	return r.writeConfigMap(ctx, cm, entries)
}

// removeConfigMapEntry removes this repository's entry (identified by its own
// last-known category/resolvedTarget) and returns the resulting content.
func (r *HomeAssistantCommunityRepositoryReconciler) removeConfigMapEntry(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	namespace, category, resolvedTarget string,
) (string, error) {
	cm, entries, err := r.loadConfigMap(ctx, ha, namespace)
	if err != nil {
		return "", err
	}
	delete(entries, entryKey(category, resolvedTarget))
	return r.writeConfigMap(ctx, cm, entries)
}

// currentConfigMapContent returns the aggregate ConfigMap's current content
// (empty string if it does not exist yet).
func (r *HomeAssistantCommunityRepositoryReconciler) currentConfigMapContent(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	namespace string,
) (string, error) {
	cm := &corev1.ConfigMap{}
	cmKey := types.NamespacedName{Name: communityRepositoriesConfigMapName(ha.Name), Namespace: namespace}
	if err := r.Get(ctx, cmKey, cm); err != nil {
		if k8serrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return cm.Data[communityRepositoriesConfigMapKey], nil
}

// decodeConfigMapEntries parses the ConfigMap's repositories.json into a map keyed
// by entryKey. A missing key or corrupt JSON is treated as "no entries yet" rather
// than an error, so a damaged ConfigMap self-heals on the next successful write
// instead of blocking reconciliation — but corruption is still logged, since a
// silent self-heal would otherwise erase all evidence that something overwrote or
// corrupted the ConfigMap outside the operator.
func decodeConfigMapEntries(ctx context.Context, cm *corev1.ConfigMap) map[string]communityRepositoryConfigMapEntry {
	entries := map[string]communityRepositoryConfigMapEntry{}
	raw, ok := cm.Data[communityRepositoriesConfigMapKey]
	if !ok {
		return entries
	}
	var payload communityRepositoriesConfigMapPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to decode community repositories ConfigMap, treating as empty",
			"configMap", cm.Name, "namespace", cm.Namespace)
		return entries
	}
	for _, e := range payload.Repositories {
		entries[entryKey(e.Category, e.ResolvedTarget)] = e
	}
	return entries
}

// entryKey identifies a ConfigMap entry by (category, resolvedTarget) — the same
// tuple (together with the HomeAssistant name implied by the ConfigMap itself)
// used as the conflict-detection key, so it is always unique among entries that
// passed conflict checking.
func entryKey(category, resolvedTarget string) string {
	return category + "|" + resolvedTarget
}

// writeConfigMap marshals entries into a stable (sorted) order and creates or
// updates cm if the content actually changed. Returns the final content string.
func (r *HomeAssistantCommunityRepositoryReconciler) writeConfigMap(
	ctx context.Context,
	cm *corev1.ConfigMap,
	entries map[string]communityRepositoryConfigMapEntry,
) (string, error) {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sortStrings(keys)

	payload := communityRepositoriesConfigMapPayload{
		Repositories: make([]communityRepositoryConfigMapEntry, 0, len(keys)),
	}
	for _, k := range keys {
		payload.Repositories = append(payload.Repositories, entries[k])
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	content := string(data)

	if cm.Data != nil && cm.Data[communityRepositoriesConfigMapKey] == content {
		return content, nil
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[communityRepositoriesConfigMapKey] = content

	if cm.ResourceVersion == "" {
		if err := r.Create(ctx, cm); err != nil {
			return "", err
		}
		return content, nil
	}
	if err := r.Update(ctx, cm); err != nil {
		return "", err
	}
	return content, nil
}

// bumpStatefulSetHash triggers a rolling restart of the HomeAssistant pod by
// patching a content-derived hash annotation on the StatefulSet's pod template,
// mirroring the existing config-hash/secrets-hash restart mechanisms (there is no
// shared helper for this in the codebase — each controller patches its own
// annotation key directly). A no-op if the StatefulSet does not exist yet (the
// HomeAssistant reconciler will pick up the ConfigMap entry when it creates it) or
// if the hash is unchanged.
func (r *HomeAssistantCommunityRepositoryReconciler) bumpStatefulSetHash(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	configMapContent string,
) (bool, error) {
	sts := &appsv1.StatefulSet{}
	stsKey := types.NamespacedName{Name: ha.Name, Namespace: ha.Namespace}
	if err := r.Get(ctx, stsKey, sts); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	hash := calculateIntegrationRepositoryHash(configMapContent)
	if sts.Spec.Template.Annotations[communityRepositoryHashAnnotationKey] == hash {
		return true, nil
	}
	if sts.Spec.Template.Annotations == nil {
		sts.Spec.Template.Annotations = map[string]string{}
	}
	sts.Spec.Template.Annotations[communityRepositoryHashAnnotationKey] = hash
	return true, r.Update(ctx, sts)
}

// setPhase updates status.phase, the Ready condition, and observedGeneration, then
// persists the status subresource.
func (r *HomeAssistantCommunityRepositoryReconciler) setPhase(
	ctx context.Context,
	repo *hav1alpha1.HomeAssistantCommunityRepository,
	phase hav1alpha1.CommunityRepositoryPhase,
	reason, message string,
	requeueAfter time.Duration,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	previousStatus := repo.DeepCopy().Status

	repo.Status.Phase = phase
	repo.Status.ObservedGeneration = repo.Generation

	if phase == hav1alpha1.PhaseInstalling {
		if repo.Status.InstallingSince == nil {
			now := metav1.Now()
			repo.Status.InstallingSince = &now
		}
	} else {
		repo.Status.InstallingSince = nil
	}

	if phase == hav1alpha1.PhaseFailed {
		repo.Status.LastError = message
		r.emitEvent(repo, corev1.EventTypeWarning, eventRepositoryInstallFailed, message)
	} else {
		repo.Status.LastError = ""
	}

	condStatus := metav1.ConditionFalse
	if phase == hav1alpha1.PhaseInstalled {
		condStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: repo.Generation,
	})

	if apiequality.Semantic.DeepEqual(previousStatus, repo.Status) {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	if err := r.Status().Update(ctx, repo); err != nil {
		log.Error(err, "Failed to update community repository status")
		return ctrl.Result{}, err
	}
	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// emitEvent emits a Kubernetes event for the repository.
func (r *HomeAssistantCommunityRepositoryReconciler) emitEvent(
	repo *hav1alpha1.HomeAssistantCommunityRepository,
	eventType, reason, message string,
) {
	if r.Recorder != nil {
		// message is passed as an argument (not the format string) so a literal
		// "%" in a repository name or wrapped error message is emitted unchanged
		// instead of being interpreted as a Sprintf verb.
		r.Recorder.Eventf(repo, nil, eventType, reason, reason, "%s", message)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *HomeAssistantCommunityRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1alpha1.HomeAssistantCommunityRepository{}).
		Watches(
			&hav1.HomeAssistant{},
			handler.EnqueueRequestsFromMapFunc(r.findRepositoriesForHomeAssistant),
		).
		Watches(
			&hav1alpha1.HomeAssistantCommunityRepository{},
			handler.EnqueueRequestsFromMapFunc(r.findSiblingRepositories),
			builder.WithPredicates(siblingRelevantChangePredicate()),
		).
		Named("homeassistantcommunityrepository").
		Complete(r)
}

// findRepositoriesForHomeAssistant returns reconcile requests for all repositories
// referencing the changed HomeAssistant (e.g. it just became available).
func (r *HomeAssistantCommunityRepositoryReconciler) findRepositoriesForHomeAssistant(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	ha, ok := obj.(*hav1.HomeAssistant)
	if !ok {
		return nil
	}
	list := &hav1alpha1.HomeAssistantCommunityRepositoryList{}
	if err := r.List(ctx, list, client.InNamespace(ha.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, item := range list.Items {
		if item.Spec.HomeAssistantRef.Name == ha.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: item.Name, Namespace: item.Namespace},
			})
		}
	}
	return requests
}

// findSiblingRepositories returns reconcile requests for other repositories
// targeting the same HomeAssistant as the changed one, so a Failed/TargetConflict
// repository gets a chance to retry once the conflicting sibling changes or is
// removed — without this, a resolved conflict would otherwise require a manual
// spec edit to be picked up.
func (r *HomeAssistantCommunityRepositoryReconciler) findSiblingRepositories(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	changed, ok := obj.(*hav1alpha1.HomeAssistantCommunityRepository)
	if !ok {
		return nil
	}
	list := &hav1alpha1.HomeAssistantCommunityRepositoryList{}
	if err := r.List(ctx, list, client.InNamespace(changed.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, item := range list.Items {
		if item.Name == changed.Name {
			continue
		}
		if item.Spec.HomeAssistantRef.Name != changed.Spec.HomeAssistantRef.Name {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: item.Name, Namespace: item.Namespace},
		})
	}
	return requests
}

// siblingRelevantChangePredicate limits findSiblingRepositories fan-out to changes
// that can actually affect conflict resolution (phase, resolvedTarget, or
// deletion). Without it, any write to any repository — including unrelated status
// fields — would re-enqueue every sibling targeting the same HomeAssistant.
func siblingRelevantChangePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRepo, ok := e.ObjectOld.(*hav1alpha1.HomeAssistantCommunityRepository)
			if !ok {
				return true
			}
			newRepo, ok := e.ObjectNew.(*hav1alpha1.HomeAssistantCommunityRepository)
			if !ok {
				return true
			}
			return oldRepo.Status.Phase != newRepo.Status.Phase ||
				oldRepo.Status.ResolvedTarget != newRepo.Status.ResolvedTarget ||
				!oldRepo.DeletionTimestamp.Equal(newRepo.DeletionTimestamp)
		},
	}
}
