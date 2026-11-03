package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestAcceptedBackupJobIdentityCannotBeReplaced(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	if _, err := store.CreateEvidence(ctx, "test", "operation", "target", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	accepted := Journal{Dispatch: DispatchAcknowledged, BackupJobID: "accepted-job"}
	if _, err := store.AdvanceJournal(ctx, "test", "operation", accepted); err != nil {
		t.Fatal(err)
	}
	for _, jobID := range []string{"", "different-job"} {
		changed := Journal{Dispatch: DispatchAcknowledged, BackupJobID: jobID}
		if _, err := store.AdvanceJournal(ctx, "test", "operation", changed); err == nil {
			t.Fatal("accepted backup identity could be replaced after dispatch")
		}
	}
}

func TestEvidenceRetainsIntentAndLimitsJournal(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	operation := types.UID("operation")
	intent := json.RawMessage(`{"backupID":"one"}`)
	if _, err := store.CreateEvidence(ctx, "test", operation, "target", intent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptApproval(ctx, "test", operation, json.RawMessage(`{"challenge":"one"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptApproval(ctx, "test", operation, json.RawMessage(`{"challenge":"two"}`)); err == nil {
		t.Fatal("approval mutation accepted")
	}
	_, err := store.UpdateJournal(ctx, "test", operation, func(json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"` + strings.Repeat("x", MaxEvidenceBytes) + `"`), nil
	})
	if !errors.Is(err, ErrEvidenceTooLarge) {
		t.Fatalf("got %v, want size error", err)
	}
}

func TestEvidenceJournalCannotRegressAfterDispatch(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	operation := types.UID("operation")
	if _, err := store.CreateEvidence(ctx, "test", operation, "target", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvanceJournal(ctx, "test", operation, Journal{Dispatch: DispatchMayHaveBeenSent}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvanceJournal(ctx, "test", operation, Journal{Dispatch: DispatchPrepared}); err == nil {
		t.Fatal("journal regressed to dispatchable state")
	}
}

func TestEvidenceCannotBeRecreatedWithDifferentAcceptedIntent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	operation := types.UID("operation")
	intent := json.RawMessage(`{"backupID":"one"}`)
	if _, err := store.CreateEvidence(ctx, "test", operation, "target", intent); err != nil {
		t.Fatal(err)
	}
	changed := json.RawMessage(`{"backupID":"two"}`)
	if _, err := store.CreateEvidence(ctx, "test", operation, "target", changed); err == nil {
		t.Fatal("recreated evidence accepted different intent")
	}
}

func TestEvidenceJournalResultCannotBeRewritten(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	operation := types.UID("operation")
	if _, err := store.CreateEvidence(ctx, "test", operation, "target", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	succeeded := Journal{Dispatch: DispatchAcknowledged, Result: "succeeded"}
	if _, err := store.AdvanceJournal(ctx, "test", operation, succeeded); err != nil {
		t.Fatal(err)
	}
	failed := Journal{Dispatch: DispatchAcknowledged, Result: "failed"}
	if _, err := store.AdvanceJournal(ctx, "test", operation, failed); err == nil {
		t.Fatal("journal result was rewritten")
	}
}

func TestMissingOrReplacedEvidenceFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	if _, err := store.GetEvidence(ctx, "test", "missing"); !errors.Is(err, ErrEvidenceMissing) {
		t.Fatalf("missing evidence error = %v", err)
	}
	operation := types.UID("operation")
	if _, err := store.CreateEvidence(ctx, "test", operation, "target", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateEvidence(ctx, "test", operation, "different-target", json.RawMessage(`{}`)); err == nil {
		t.Fatal("replaced target evidence accepted")
	}
}
