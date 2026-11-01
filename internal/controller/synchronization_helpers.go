package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

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
func ordinaryWriteGate(ctx context.Context, store coordination.Store, namespace string, instanceUID types.UID) (ctrl.Result, error) {
	root, err := store.GetRoot(ctx, namespace, instanceUID)
	if err != nil {
		return ctrl.Result{RequeueAfter: synchronizationRequeue}, err
	}
	if root.Mode != "Open" || root.RestoreHold != nil || root.ActivePermit != nil {
		return ctrl.Result{RequeueAfter: synchronizationRequeue}, nil
	}
	return ctrl.Result{}, nil
}
