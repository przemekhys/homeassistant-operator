package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/przemekhys/homeassistant-operator/internal/coordination"
)

func synchronizationTestStore(t *testing.T) coordination.Store {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	return coordination.Store{Reader: c, Client: c}
}

func TestOrdinaryWriteGateUsesDurableRoot(t *testing.T) {
	ctx, store, uid := context.Background(), synchronizationTestStore(t), types.UID("instance")
	if _, err := store.CreateRoot(ctx, "test", uid); err != nil {
		t.Fatal(err)
	}
	if result, err := ordinaryWriteGate(ctx, store, "test", uid); err != nil || !result.IsZero() {
		t.Fatalf("open gate = %#v, %v", result, err)
	}
	if err := store.SetHold(ctx, "test", uid, "restore", "restore pending"); err != nil {
		t.Fatal(err)
	}
	if result, err := ordinaryWriteGate(ctx, store, "test", uid); err != nil ||
		result.RequeueAfter != synchronizationRequeue {
		t.Fatalf("held gate = %#v, %v", result, err)
	}
}

func TestOrdinaryWriteGateDeniesWritesWhilePermitIsActive(t *testing.T) {
	ctx, store, uid := context.Background(), synchronizationTestStore(t), types.UID("instance")
	if _, err := store.CreateRoot(ctx, "test", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquirePermit(ctx, "test", uid, "writer", "Configuration", "operation"); err != nil {
		t.Fatal(err)
	}
	if result, err := ordinaryWriteGate(ctx, store, "test", uid); err != nil ||
		result.RequeueAfter != synchronizationRequeue {
		t.Fatalf("permit gate = %#v, %v", result, err)
	}
}

func TestFinalizerWriteGateDoesNotBypassDurableHold(t *testing.T) {
	ctx, store, uid := context.Background(), synchronizationTestStore(t), types.UID("instance")
	if _, err := store.CreateRoot(ctx, "test", uid); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHold(ctx, "test", uid, "restore", "restore pending"); err != nil {
		t.Fatal(err)
	}
	if result, err := finalizerWriteGate(ctx, store, "test", uid); err != nil ||
		result.RequeueAfter != synchronizationRequeue {
		t.Fatalf("finalizer gate = %#v, %v", result, err)
	}
}

func TestUpdateStatusIfChangedSuppressesUnchangedObject(t *testing.T) {
	before := &corev1.ConfigMap{}
	after := before.DeepCopy()
	if err := updateStatusIfChanged(context.Background(), nil, before, after); err != nil {
		t.Fatalf("unchanged status update = %v", err)
	}
}

func TestOrdinaryWriteGateFailsClosedWhenRootIsUnavailable(t *testing.T) {
	result, err := ordinaryWriteGate(context.Background(), synchronizationTestStore(t), "test", "missing")
	if err == nil || result.RequeueAfter != synchronizationRequeue {
		t.Fatalf("missing root = %#v, %v", result, err)
	}
}

func TestBoundedContextsApplyTheirDeclaredDeadlines(t *testing.T) {
	request, cancelRequest := boundedRequestContext(context.Background())
	defer cancelRequest()
	step, cancelStep := boundedStepContext(context.Background())
	defer cancelStep()
	requestDeadline, requestOK := request.Deadline()
	stepDeadline, stepOK := step.Deadline()
	if !requestOK || !stepOK {
		t.Fatal("bounded context has no deadline")
	}
	if remaining := time.Until(requestDeadline); remaining > ordinaryRequestTimeout || remaining <= 0 {
		t.Fatalf("request deadline remaining = %s", remaining)
	}
	if remaining := time.Until(stepDeadline); remaining > reconcileStepTimeout || remaining <= 0 {
		t.Fatalf("step deadline remaining = %s", remaining)
	}
}

func TestBoundedContextsPropagateParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	request, cancelRequest := boundedRequestContext(parent)
	defer cancelRequest()
	step, cancelStep := boundedStepContext(parent)
	defer cancelStep()
	cancelParent()
	for name, ctx := range map[string]context.Context{"request": request, "step": step} {
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatalf("%s context did not propagate cancellation", name)
		}
	}
}
