package controller

import (
	"context"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/coordination"
)

const (
	ordinaryRequestTimeout = 10 * time.Second
	reconcileStepTimeout   = 20 * time.Second
	synchronizationRequeue = 10 * time.Second
)

// boundedRequestContext prevents ordinary HA/API requests from occupying a
// reconcile worker indefinitely.
func boundedRequestContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, ordinaryRequestTimeout)
}

func boundedStepContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, reconcileStepTimeout)
}

// ordinaryWriteGate reads the durable source of truth before any ordinary
// write. It never uses a cached paused condition as authorization.
func ordinaryWriteGate(
	ctx context.Context,
	store coordination.Store,
	namespace string,
	instanceUID types.UID,
) (ctrl.Result, error) {
	root, err := store.GetRoot(ctx, namespace, instanceUID)
	if err != nil {
		return ctrl.Result{RequeueAfter: synchronizationRequeue}, err
	}
	if root.Mode != "Open" || root.RestoreHold != nil || root.ActivePermit != nil {
		return ctrl.Result{RequeueAfter: synchronizationRequeue}, nil
	}
	return ctrl.Result{}, nil
}

// finalizerWriteGate applies the same durable authorization boundary to a
// cleanup finalizer. Deletion must not bypass an active synchronization or
// restore hold merely because the normal reconciliation path has stopped.
func finalizerWriteGate(
	ctx context.Context,
	store coordination.Store,
	namespace string,
	instanceUID types.UID,
) (ctrl.Result, error) {
	return ordinaryWriteGate(ctx, store, namespace, instanceUID)
}

// maintenanceHoldBlocksStatefulSetUpdate reads the durable coordinator rather
// than cached status before changing the workload. A missing coordinator for an
// opted-in instance is fail-closed: applying a new pod template can interrupt a
// restore whose evidence is temporarily unavailable.
func maintenanceHoldBlocksStatefulSetUpdate(
	ctx context.Context, deps CoordinationDependencies, cached client.Client, expected client.Object,
) (bool, error) {
	ha, coordinated, err := liveSynchronizationTarget(ctx, deps, cached, expected.GetNamespace(), expected.GetName())
	if err != nil {
		return coordinationEnabledObject(expected), err
	}
	if ha.UID != expected.GetUID() {
		return true, nil
	}
	if !coordinated {
		return false, nil
	}
	root, err := deps.Store.GetRoot(ctx, ha.Namespace, ha.UID)
	if err != nil {
		return true, err
	}
	return root.MaintenanceHold != nil, nil
}

func coordinationEnabledObject(object client.Object) bool {
	ha, ok := object.(*hav1.HomeAssistant)
	return ok && coordinationEnabled(ha)
}

// updateStatusIfChanged avoids status-only write loops. Callers must keep an
// unmodified DeepCopy as before and pass the object after projecting observed
// state into status.
func updateStatusIfChanged(ctx context.Context, writer client.StatusWriter, before, after client.Object) error {
	if reflect.DeepEqual(before, after) {
		return nil
	}
	return writer.Update(ctx, after)
}
