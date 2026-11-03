package coordination

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/types"
)

var (
	ErrPermitHeld        = errors.New("an active coordination permit is already held")
	ErrPermitNotOwner    = errors.New("permit is not owned by caller")
	ErrInvalidTransition = errors.New("invalid permit state transition")
	ErrHoldActive        = errors.New("coordination hold is active")
)

// AcquirePermit is a single CAS attempt. Callers retry only by rereading the
// root state; they must not put a side effect in a conflict-retry closure.
func (s Store) AcquirePermit(
	ctx context.Context,
	namespace string,
	instanceUID types.UID,
	owner, class string,
	operationUID types.UID,
) (*Permit, error) {
	var acquired *Permit
	_, err := s.UpdateRoot(ctx, namespace, instanceUID, func(root *Root) error {
		if root.RestoreHold != nil && class != "RestorePreparation" && class != "KeyProtection" {
			return ErrHoldActive
		}
		if root.Mode != "Open" && class != "RestorePreparation" && class != "KeyProtection" {
			return ErrHoldActive
		}
		if root.ActivePermit != nil {
			return ErrPermitHeld
		}
		permit := &Permit{
			ID:           uuid.NewString(),
			Owner:        owner,
			OperationUID: string(operationUID),
			Class:        class,
			State:        PermitPrepared,
			Epoch:        root.Epoch,
		}
		root.ActivePermit = permit
		acquired = permit
		return nil
	})
	if err != nil {
		return nil, err
	}
	return acquired, nil
}

// TransitionPermit records the dispatch boundary. Prepared can only become
// InFlight once; an InFlight permit can become Confirmed or Unknown. No helper
// ever converts Unknown back into a dispatchable state.
func (s Store) TransitionPermit(
	ctx context.Context,
	namespace string,
	instanceUID types.UID,
	owner, permitID string,
	next PermitState,
) error {
	_, err := s.UpdateRoot(ctx, namespace, instanceUID, func(root *Root) error {
		permit := root.ActivePermit
		if permit == nil || permit.ID != permitID || permit.Owner != owner {
			return ErrPermitNotOwner
		}
		valid := (permit.State == PermitPrepared && next == PermitInFlight) ||
			(permit.State == PermitInFlight && (next == PermitConfirmed || next == PermitUnknown))
		if !valid {
			return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, permit.State, next)
		}
		permit.State = next
		return nil
	})
	return err
}

// ReleasePermit is deliberately restricted to confirmed work. Unknown work is
// retained as a durable safety boundary until a separate resolution procedure.
func (s Store) ReleasePermit(
	ctx context.Context,
	namespace string,
	instanceUID types.UID,
	owner, permitID string,
) error {
	_, err := s.UpdateRoot(ctx, namespace, instanceUID, func(root *Root) error {
		permit := root.ActivePermit
		if permit == nil || permit.ID != permitID || permit.Owner != owner {
			return ErrPermitNotOwner
		}
		if permit.State != PermitConfirmed {
			return fmt.Errorf("%w: only confirmed permits may be released", ErrInvalidTransition)
		}
		root.ActivePermit = nil
		return nil
	})
	return err
}

// CancelPrepared removes only an owned permit that cannot yet have dispatched.
// A lost CAS response never grants authority to execute its callback.
func (s Store) CancelPrepared(
	ctx context.Context, namespace string, instanceUID types.UID, owner, permitID string,
) error {
	_, err := s.UpdateRoot(ctx, namespace, instanceUID, func(root *Root) error {
		permit := root.ActivePermit
		if permit == nil || permit.ID != permitID || permit.Owner != owner {
			return ErrPermitNotOwner
		}
		if permit.State != PermitPrepared {
			return ErrInvalidTransition
		}
		root.ActivePermit = nil
		return nil
	})
	return err
}

func (s Store) SetHold(
	ctx context.Context,
	namespace string,
	instanceUID, operationUID types.UID,
	reason string,
) error {
	_, err := s.UpdateRoot(ctx, namespace, instanceUID, func(root *Root) error {
		if root.RestoreHold != nil && root.RestoreHold.OperationUID != string(operationUID) {
			return ErrHoldActive
		}
		root.RestoreHold = &Hold{OperationUID: string(operationUID), Reason: reason}
		root.Mode = "RestoreExclusive"
		return nil
	})
	return err
}

// TakeOverPrepared permits recovery only before a dispatch boundary. It never
// takes over InFlight or Unknown work, because either may already have caused
// an external effect.
func (s Store) TakeOverPrepared(
	ctx context.Context,
	namespace string,
	instanceUID types.UID,
	oldOwner, newOwner, permitID string,
) error {
	_, err := s.UpdateRoot(ctx, namespace, instanceUID, func(root *Root) error {
		permit := root.ActivePermit
		if permit == nil || permit.ID != permitID || permit.Owner != oldOwner {
			return ErrPermitNotOwner
		}
		if permit.State != PermitPrepared {
			return fmt.Errorf("%w: only Prepared permits may be taken over", ErrInvalidTransition)
		}
		permit.Owner = newOwner
		return nil
	})
	return err
}

func (s Store) AcknowledgeDrain(
	ctx context.Context,
	namespace string,
	instanceUID types.UID,
	writer, podUID string,
	epoch int64,
) error {
	_, err := s.UpdateRoot(ctx, namespace, instanceUID, func(root *Root) error {
		if epoch != root.Epoch {
			return fmt.Errorf("drain acknowledgement has stale epoch %d", epoch)
		}
		if root.DrainAcks == nil {
			root.DrainAcks = make(map[string]DrainAcknowledgement)
		}
		root.DrainAcks[writer] = DrainAcknowledgement{Writer: writer, PodUID: podUID, Epoch: epoch}
		return nil
	})
	return err
}
