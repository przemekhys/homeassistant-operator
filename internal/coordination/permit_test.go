package coordination

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestPermitDoesNotUnlockUnknownWork(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	uid := types.UID("instance")
	if _, err := store.CreateRoot(ctx, "test", uid); err != nil {
		t.Fatal(err)
	}
	p, err := store.AcquirePermit(ctx, "test", uid, "writer-a", "Normal", "operation")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionPermit(ctx, "test", uid, "writer-a", p.ID, PermitInFlight); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionPermit(ctx, "test", uid, "writer-a", p.ID, PermitUnknown); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleasePermit(ctx, "test", uid, "writer-a", p.ID); err == nil {
		t.Fatal("unknown permit was released")
	}
	if _, err := store.AcquirePermit(ctx, "test", uid, "writer-b", "Normal", "other"); !errors.Is(err, ErrPermitHeld) {
		t.Fatalf("got %v, want permit held", err)
	}
}

func TestOnlyPreparedPermitCanBeTakenOver(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	uid := types.UID("instance")
	if _, err := store.CreateRoot(ctx, "test", uid); err != nil {
		t.Fatal(err)
	}
	p, err := store.AcquirePermit(ctx, "test", uid, "old", "Normal", "operation")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TakeOverPrepared(ctx, "test", uid, "old", "new", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionPermit(ctx, "test", uid, "new", p.ID, PermitInFlight); err != nil {
		t.Fatal(err)
	}
	if err := store.TakeOverPrepared(ctx, "test", uid, "new", "other", p.ID); err == nil {
		t.Fatal("in-flight permit was taken over")
	}
}

func TestConflictDoesNotGrantOrReleasePermit(t *testing.T) {
	ctx := context.Background()
	base := newTestStore()
	uid := types.UID("instance")
	if _, err := base.CreateRoot(ctx, "test", uid); err != nil {
		t.Fatal(err)
	}
	conflicting := &conflictOnceClient{Client: base.Client, conflict: true}
	store := Store{Reader: base.Reader, Client: conflicting}
	if _, err := store.AcquirePermit(ctx, "test", uid, "writer", "Normal", "operation"); !errors.Is(
		err,
		errInjectedConflict,
	) {
		t.Fatalf("got %v, want injected conflict", err)
	}
	root, err := base.GetRoot(ctx, "test", uid)
	if err != nil {
		t.Fatal(err)
	}
	if root.ActivePermit != nil {
		t.Fatal("conflicted write changed durable root")
	}
	if _, err := store.AcquirePermit(ctx, "test", uid, "writer", "Normal", "operation"); err != nil {
		t.Fatal(err)
	}
}

func TestLeaderHandoverCannotTakeOverDispatchedPermit(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	uid := types.UID("instance")
	if _, err := store.CreateRoot(ctx, "test", uid); err != nil {
		t.Fatal(err)
	}
	permit, err := store.AcquirePermit(ctx, "test", uid, "leader-a", "Restore", "operation")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionPermit(ctx, "test", uid, "leader-a", permit.ID, PermitInFlight); err != nil {
		t.Fatal(err)
	}
	if err := store.TakeOverPrepared(ctx, "test", uid, "leader-a", "leader-b", permit.ID); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("handover error = %v, want invalid transition", err)
	}
}

func TestHoldDeniesOrdinaryPermitButAllowsPreparation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	uid := types.UID("instance")
	if _, err := store.CreateRoot(ctx, "test", uid); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHold(ctx, "test", uid, "restore", "restore pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquirePermit(ctx, "test", uid, "ordinary", "Normal", "operation"); !errors.Is(
		err,
		ErrHoldActive,
	) {
		t.Fatalf("ordinary permit error = %v", err)
	}
	if _, err := store.AcquirePermit(ctx, "test", uid, "prepare", "RestorePreparation", "restore"); err != nil {
		t.Fatalf("preparation denied: %v", err)
	}
}

func TestDispatchCrashWindowsNeverPermitUnsafeUnlock(t *testing.T) {
	for _, tc := range []struct {
		name              string
		states            []PermitState
		takeover, release bool
	}{
		{name: "before dispatch", takeover: true},
		{name: "dispatch sent", states: []PermitState{PermitInFlight}, takeover: false, release: false},
		{name: "response lost", states: []PermitState{PermitInFlight, PermitUnknown}, takeover: false, release: false},
		{name: "response recorded", states: []PermitState{PermitInFlight, PermitConfirmed}, takeover: false, release: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, uid := context.Background(), newTestStore(), types.UID("instance")
			if _, err := store.CreateRoot(ctx, "test", uid); err != nil {
				t.Fatal(err)
			}
			permit, err := store.AcquirePermit(ctx, "test", uid, "writer-a", "Restore", "operation")
			if err != nil {
				t.Fatal(err)
			}
			for _, state := range tc.states {
				if err := store.TransitionPermit(ctx, "test", uid, "writer-a", permit.ID, state); err != nil {
					t.Fatal(err)
				}
			}
			takeoverErr := store.TakeOverPrepared(ctx, "test", uid, "writer-a", "writer-b", permit.ID)
			if tc.takeover != (takeoverErr == nil) {
				t.Fatalf("takeover=%v, want %v", takeoverErr, tc.takeover)
			}
			releaseErr := store.ReleasePermit(ctx, "test", uid, "writer-a", permit.ID)
			if tc.release != (releaseErr == nil) {
				t.Fatalf("release=%v, want %v", releaseErr, tc.release)
			}
		})
	}
}
