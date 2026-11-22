package migrationprotocol

import (
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
