package controller

import (
	"context"
	"errors"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/coordination"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

var errSynchronizationWriteBlocked = errors.New("ordinary synchronization write is blocked")

func coordinationEnabled(ha *hav1.HomeAssistant) bool {
	return ha.Spec.Alpha != nil && ha.Spec.Alpha.CoordinatedSynchronization != nil &&
		ha.Spec.Alpha.CoordinatedSynchronization.Enabled
}

// childSynchronizationGate runs before deletion and other side effects. This
// observation is not execution authority: each HA mutation also acquires a CAS
// permit immediately before dispatch.
func childSynchronizationGate(
	ctx context.Context, deps CoordinationDependencies, cached client.Client, child client.Object, targetName string,
) (ctrl.Result, error) {
	ha, coordinated, err := liveSynchronizationTarget(ctx, deps, cached, child.GetNamespace(), targetName)
	if err != nil {
		if apierrors.IsNotFound(err) && ha == nil {
			// Preserve legacy missing-target handling until a target exists.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: synchronizationRequeue}, err
	}
	if ha.Spec.Synchronization != nil && ha.Spec.Synchronization.Paused {
		return ctrl.Result{RequeueAfter: synchronizationRequeue}, nil
	}
	if !coordinated {
		return ctrl.Result{}, nil
	}
	result, err := ordinaryWriteGate(ctx, deps.Store, ha.Namespace, ha.UID)
	return result, err
}

func removeFinalizerAfterSynchronizationGate(
	ctx context.Context, dependencies CoordinationDependencies, cached client.Client,
	child client.Object, targetName, finalizer string,
) (ctrl.Result, error) {
	if result, err := childSynchronizationGate(ctx, dependencies, cached, child, targetName); err != nil || !result.IsZero() {
		return result, err
	}
	controllerutil.RemoveFinalizer(child, finalizer)
	if err := cached.Update(ctx, child); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// An existing root continues to govern writes even if alpha is subsequently
// disabled. Production reconcilers inject APIReader and use direct reads rather
// than substituting their informer cache.
func liveSynchronizationTarget(
	ctx context.Context, deps CoordinationDependencies, cached client.Client, namespace, name string,
) (*hav1.HomeAssistant, bool, error) {
	reader := deps.APIReader
	if reader == nil {
		reader = cached
	}
	ha := &hav1.HomeAssistant{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, ha); err != nil {
		return nil, false, err
	}
	if deps.APIReader == nil || deps.Store.Reader == nil || deps.Store.Client == nil {
		if coordinationEnabled(ha) {
			return ha, false, errSynchronizationWriteBlocked
		}
		return ha, false, nil
	}
	_, err := deps.Store.GetRoot(ctx, namespace, ha.UID)
	if apierrors.IsNotFound(err) && !coordinationEnabled(ha) {
		return ha, false, nil
	}
	if err != nil {
		return ha, false, err
	}
	return ha, true, nil
}

func coordinatedHAClient(
	ctx context.Context, ha *hav1.HomeAssistant, deps CoordinationDependencies,
	cached client.Client, factory func(string) *haclient.Client,
) *haclient.Client {
	base := newHAClientForHA(ha, factory)
	// Reconcilers that have not opted into coordination retain their existing
	// behavior. Every coordinated production reconciler receives dependencies at
	// construction and is fail-closed in liveSynchronizationTarget instead.
	if deps.APIReader == nil || deps.Store.Reader == nil || deps.Store.Client == nil {
		return base
	}
	_, coordinated, err := liveSynchronizationTarget(ctx, deps, cached, ha.Namespace, ha.Name)
	if err == nil && !coordinated && (ha.Spec.Synchronization == nil || !ha.Spec.Synchronization.Paused) {
		return base
	}
	return base.WithWriteGuard(func(ctx context.Context, execute func(context.Context) error) error {
		return executeOrdinaryHAWrite(ctx, deps, cached, ha, execute)
	})
}

func executeOrdinaryHAWrite(
	ctx context.Context, deps CoordinationDependencies, cached client.Client, expected *hav1.HomeAssistant,
	execute func(context.Context) error,
) error {
	writeCtx, cancel := boundedRequestContext(ctx)
	defer cancel()
	ha, coordinated, err := liveSynchronizationTarget(writeCtx, deps, cached, expected.Namespace, expected.Name)
	if err != nil {
		return errSynchronizationWriteBlocked
	}
	if ha.UID != expected.UID || (ha.Spec.Synchronization != nil && ha.Spec.Synchronization.Paused) {
		return errSynchronizationWriteBlocked
	}
	if !coordinated {
		return execute(writeCtx)
	}
	owner := uuid.NewString()
	permit, err := deps.Store.AcquirePermit(writeCtx, ha.Namespace, ha.UID, owner, "Ordinary", "")
	if err != nil {
		return errSynchronizationWriteBlocked
	}
	// A pause or UID replacement between observation and permit acquisition
	// cancels only this Prepared ticket, before the dispatch boundary.
	live, _, readErr := liveSynchronizationTarget(writeCtx, deps, cached, ha.Namespace, ha.Name)
	if readErr != nil || live.UID != ha.UID || (live.Spec.Synchronization != nil && live.Spec.Synchronization.Paused) {
		_ = deps.Store.CancelPrepared(writeCtx, ha.Namespace, ha.UID, owner, permit.ID)
		return errSynchronizationWriteBlocked
	}
	if err := deps.Store.TransitionPermit(writeCtx, ha.Namespace, ha.UID, owner, permit.ID,
		coordination.PermitInFlight); err != nil {
		return errSynchronizationWriteBlocked
	}
	if err := execute(writeCtx); err != nil {
		_ = deps.Store.TransitionPermit(writeCtx, ha.Namespace, ha.UID, owner, permit.ID, coordination.PermitUnknown)
		// Keep raw native/transport errors out of controller status, Events and logs.
		return errors.New("ordinary Home Assistant mutation outcome is unknown")
	}
	if err := deps.Store.TransitionPermit(writeCtx, ha.Namespace, ha.UID, owner, permit.ID,
		coordination.PermitConfirmed); err != nil {
		return errSynchronizationWriteBlocked
	}
	if err := deps.Store.ReleasePermit(writeCtx, ha.Namespace, ha.UID, owner, permit.ID); err != nil {
		return errSynchronizationWriteBlocked
	}
	return nil
}
