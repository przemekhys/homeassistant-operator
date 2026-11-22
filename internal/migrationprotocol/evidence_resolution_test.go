package migrationprotocol

import (
	"strings"
	"testing"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

func TestObservationResolutionRejectsAmbiguousSet(t *testing.T) {
	op := &ha.HomeAssistantMigration{Spec: ha.HomeAssistantMigrationSpec{Resolutions: []ha.MigrationResolution{
		{Stage: "backup", RequestID: "request", Decision: "ObserveExternalResult"},
		{Stage: "backup", RequestID: "request", Decision: "ObserveExternalResult"},
	}}}
	if _, err := ObservationResolution(op, "backup", "request"); err == nil {
		t.Fatal("ambiguous observations accepted")
	}
	op.Spec.Resolutions = op.Spec.Resolutions[:1]
	if r, err := ObservationResolution(op, "backup", "request"); err != nil || r == nil {
		t.Fatal("single observation rejected", err)
	}
}

func TestAcceptedResolutionsRejectDuplicateObservations(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	r := ha.MigrationResolution{Version: "v1", Stage: "backup", RequestID: id, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: "evidence", UID: "uid", Key: "result"}}
	op := &ha.HomeAssistantMigration{Spec: ha.HomeAssistantMigrationSpec{Resolutions: []ha.MigrationResolution{r}}}
	if err := ValidateAccepted(op); err != nil {
		t.Fatal(err)
	}
	other := r.DeepCopy()
	other.EvidenceRef.Name = "other"
	op.Spec.Resolutions = append(op.Spec.Resolutions, *other)
	if err := ValidateAccepted(op); err == nil || !strings.Contains(err.Error(), "duplicate observation") {
		t.Fatalf("expected duplicate observation error, got %v", err)
	}
	op.Spec.Resolutions[1].Stage = "upload"
	if err := ValidateAccepted(op); err != nil {
		t.Fatalf("distinct stage rejected: %v", err)
	}
}
