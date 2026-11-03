package coordination

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
)

// DispatchState is deliberately monotonic. MayHaveBeenSent is written before
// the network call and is never changed back to a dispatchable state.
type DispatchState string

const (
	DispatchNotPrepared     DispatchState = "NotPrepared"
	DispatchPrepared        DispatchState = "Prepared"
	DispatchMayHaveBeenSent DispatchState = "MayHaveBeenSent"
	DispatchAcknowledged    DispatchState = "Acknowledged"
)

type Journal struct {
	Dispatch    DispatchState `json:"dispatch"`
	Result      string        `json:"result,omitempty"`
	BackupJobID string        `json:"backupJobID,omitempty"`
	BackupID    string        `json:"backupID,omitempty"`
}

func dispatchRank(state DispatchState) int {
	switch state {
	case DispatchNotPrepared:
		return 0
	case DispatchPrepared:
		return 1
	case DispatchMayHaveBeenSent:
		return 2
	case DispatchAcknowledged:
		return 3
	default:
		return -1
	}
}

// AdvanceJournal prevents a previously consumed operation from becoming
// dispatchable after a restart, conflict, or status loss.
func (s Store) AdvanceJournal(
	ctx context.Context,
	namespace string,
	operationUID types.UID,
	next Journal,
) (*Evidence, error) {
	return s.UpdateJournal(ctx, namespace, operationUID, func(raw json.RawMessage) (json.RawMessage, error) {
		current := Journal{Dispatch: DispatchNotPrepared}
		if len(raw) != 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &current); err != nil {
				return nil, fmt.Errorf("decode evidence journal: %w", err)
			}
		}
		if dispatchRank(next.Dispatch) < dispatchRank(current.Dispatch) {
			return nil, fmt.Errorf("evidence journal cannot regress from %s to %s", current.Dispatch, next.Dispatch)
		}
		if dispatchRank(next.Dispatch) < 0 {
			return nil, fmt.Errorf("unknown evidence dispatch state %q", next.Dispatch)
		}
		if current.Result != "" && next.Result != current.Result {
			return nil, fmt.Errorf("evidence result is immutable once recorded")
		}
		if current.BackupJobID != "" && next.BackupJobID != current.BackupJobID {
			return nil, fmt.Errorf("accepted native backup job identity is immutable")
		}
		if current.BackupID != "" && next.BackupID != current.BackupID {
			return nil, fmt.Errorf("confirmed native archive identity is immutable")
		}
		return json.Marshal(next)
	})
}
