package controller

import (
	"context"
	"strings"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// migrationLocalWorkTerminationTimeout bounds how long a deleting or
	// cancelled migration waits for its own worker Pods to terminate. After
	// this deadline the operation stays Blocked and retains its finalizer and
	// storage rather than releasing them while a Pod may still be writing.
	migrationLocalWorkTerminationTimeout = 2 * time.Minute
	migrationLocalWorkRequeueAfter       = 10 * time.Second
)

// migrationLocalWorkState describes whether the migration's own Jobs and Pods
// have stopped. It is derived only from Kubernetes state: cancellation never
// contacts Home Assistant and never claims that an already accepted HA task was
// rolled back.
type migrationLocalWorkState struct {
	Done    bool
	Blocked bool
	Reason  string
	Message string
}

// stopMigrationLocalWork deletes the migration's worker Jobs and Pods and waits
// for them to disappear. It never deletes receipts, the archive PVC or the
// finalizer; those remain until the finalizer is deliberately released so the
// durable evidence survives the whole local cleanup.
func stopMigrationLocalWork(
	ctx context.Context, kube client.Client, reader client.Reader, op *ha.HomeAssistantMigration,
) (migrationLocalWorkState, error) {
	if op == nil {
		return migrationLocalWorkState{Done: true}, nil
	}
	if reader == nil {
		reader = kube
	}
	if kube == nil || reader == nil {
		return migrationLocalWorkState{
			Blocked: true,
			Reason:  "LocalWorkTerminationUnavailable",
			Message: "local worker cleanup needs a Kubernetes reader and writer",
		}, nil
	}
	jobs, err := migrationOwnedJobs(ctx, reader, op)
	if err != nil {
		return migrationLocalWorkState{}, err
	}
	pods, err := migrationWorkerPods(ctx, reader, op)
	if err != nil {
		return migrationLocalWorkState{}, err
	}

	now := time.Now()
	for i := range jobs {
		if jobs[i].DeletionTimestamp != nil &&
			now.Sub(jobs[i].DeletionTimestamp.Time) > migrationLocalWorkTerminationTimeout {
			return migrationLocalWorkState{
				Blocked: true,
				Reason:  "LocalWorkTerminationBlocked",
				Message: "worker Jobs are stuck terminating; finalizer and archive storage are retained",
			}, nil
		}
	}
	for i := range pods {
		if pods[i].DeletionTimestamp != nil &&
			now.Sub(pods[i].DeletionTimestamp.Time) > migrationLocalWorkTerminationTimeout {
			return migrationLocalWorkState{
				Blocked: true,
				Reason:  "LocalWorkTerminationBlocked",
				Message: "worker Pods are stuck terminating; finalizer and archive storage are retained",
			}, nil
		}
	}

	pending := false
	for i := range jobs {
		job := &jobs[i]
		// A deleting Job cannot create a replacement Pod. Its remaining Pods are
		// checked below, so a stuck Job object alone must not retain storage.
		if job.DeletionTimestamp != nil {
			continue
		}
		err := kube.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
		if err != nil && !apierrors.IsNotFound(err) {
			return migrationLocalWorkState{}, err
		}
		pending = true
	}
	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		if pod.DeletionTimestamp != nil {
			pending = true
			continue
		}
		if err := kube.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil && !apierrors.IsNotFound(err) {
			return migrationLocalWorkState{}, err
		}
		pending = true
	}
	if pending {
		return migrationLocalWorkState{
			Done:    false,
			Reason:  "LocalWorkTerminationInProgress",
			Message: "local worker Jobs or Pods are still stopping; finalizer and archive storage are retained",
		}, nil
	}
	return migrationLocalWorkState{Done: true}, nil
}

// migrationOwnedJobs returns only Jobs whose controller owner is this exact
// migration. A same-named Job in another namespace or owned by another object is
// never touched.
func migrationOwnedJobs(ctx context.Context, reader client.Reader, op *ha.HomeAssistantMigration) ([]batchv1.Job, error) {
	var list batchv1.JobList
	if err := reader.List(ctx, &list, client.InNamespace(op.Namespace)); err != nil {
		return nil, err
	}
	owned := make([]batchv1.Job, 0, len(list.Items))
	for i := range list.Items {
		owner := metav1.GetControllerOf(&list.Items[i])
		if owner == nil || owner.UID != op.UID || owner.Kind != "HomeAssistantMigration" {
			continue
		}
		owned = append(owned, list.Items[i])
	}
	return owned, nil
}

// migrationWorkerPods lists Pods labelled for this migration's accepted
// requests. A Pod is only considered local work when its controller owner is a
// Job with the migration worker name prefix, so an unrelated labelled Pod is
// never deleted.
func migrationWorkerPods(ctx context.Context, reader client.Reader, op *ha.HomeAssistantMigration) ([]corev1.Pod, error) {
	seen := map[string]bool{}
	var pods []corev1.Pod
	for _, request := range op.Spec.ExecutionRequests {
		value := migrationWorkerLabelValue(request.RequestID)
		if value == "" {
			continue
		}
		for _, selector := range []map[string]string{
			{migrationRequestLabel: value},
			{migrationOnboardingRequestLabel: value},
		} {
			var list corev1.PodList
			if err := reader.List(ctx, &list, client.InNamespace(op.Namespace), client.MatchingLabels(selector)); err != nil {
				return nil, err
			}
			for i := range list.Items {
				pod := &list.Items[i]
				key := pod.Namespace + "/" + pod.Name
				if seen[key] {
					continue
				}
				seen[key] = true
				if !migrationWorkerPod(pod) {
					continue
				}
				pods = append(pods, *pod)
			}
		}
	}
	return pods, nil
}

// migrationWorkerPod recognises only Pods controlled by a migration worker Job.
func migrationWorkerPod(pod *corev1.Pod) bool {
	owner := metav1.GetControllerOf(pod)
	return owner != nil && owner.Kind == "Job" && strings.HasPrefix(owner.Name, "migration-")
}

// migrationWorkerLabelValue mirrors the label value used by worker Jobs so the
// cleanup path finds exactly the same Pods.
func migrationWorkerLabelValue(requestID string) string {
	trimmed := strings.TrimPrefix(requestID, "sha256:")
	if len(trimmed) < 40 {
		return ""
	}
	return trimmed[:40]
}

// handleMigrationDeletion stops local worker Jobs and Pods before releasing the
// finalizer. Releasing the finalizer lets Kubernetes garbage-collect the archive
// PVC and receipts, so it must not happen while a Pod may still be writing. The
// status is kept only as an observation; the finalizer is the durable guard.
func (r *HomeAssistantMigrationReconciler) handleMigrationDeletion(
	ctx context.Context, migration *ha.HomeAssistantMigration,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(migration, migrationOperationFinalizer) {
		return ctrl.Result{}, nil
	}
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	state, err := stopMigrationLocalWork(ctx, r.Client, reader, migration)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !state.Done {
		reason := state.Reason
		if reason == "" {
			reason = "LocalWorkTerminationInProgress"
		}
		message := state.Message
		if message == "" {
			message = "local worker Jobs or Pods are still stopping; finalizer and archive storage are retained"
		}
		if meta.SetStatusCondition(&migration.Status.Conditions, metav1.Condition{
			Type: "Progressing", Status: metav1.ConditionFalse, ObservedGeneration: migration.Generation,
			Reason: reason, Message: message,
		}) {
			if err := r.Status().Update(ctx, migration); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: migrationLocalWorkRequeueAfter}, nil
	}
	controllerutil.RemoveFinalizer(migration, migrationOperationFinalizer)
	return ctrl.Result{}, r.Update(ctx, migration)
}

// handleMigrationCancellation blocks new stages and stops local work. It never
// waits for Home Assistant and never declares an accepted HA request rolled
// back. The finalizer and archive storage stay in place so the evidence and any
// partially written archive remain available until the administrator deletes
// the operation.
func (r *HomeAssistantMigrationReconciler) handleMigrationCancellation(
	ctx context.Context, migration *ha.HomeAssistantMigration,
) (ctrl.Result, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	state, err := stopMigrationLocalWork(ctx, r.Client, reader, migration)
	if err != nil {
		return ctrl.Result{}, err
	}
	phase := "Blocked"
	reason := "CancellationRequested"
	message := "new migration work is blocked; local workers stopped; " +
		"an already accepted Home Assistant task is not rolled back"
	if migrationAbortRequested(migration) {
		reason = "AbortRequested"
		message = "new migration work is blocked; local workers stopped; " +
			"an already accepted Home Assistant task is not rolled back"
	}
	if !state.Done {
		reason = "CancellationInProgress"
		message = "new migration work is blocked; local worker Jobs or Pods are still stopping; " +
			"an already accepted Home Assistant task is not rolled back"
		if state.Blocked {
			reason = "LocalWorkTerminationBlocked"
			message = "new migration work is blocked; local worker Pods could not be confirmed stopped, " +
				"so finalizer and archive storage are retained; " +
				"an already accepted Home Assistant task is not rolled back"
		}
	}
	changed := migration.Status.Phase != phase
	migration.Status.Phase = phase
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
	return ctrl.Result{RequeueAfter: migrationLocalWorkRequeueAfter}, nil
}
