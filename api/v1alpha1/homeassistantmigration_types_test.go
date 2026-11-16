package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHomeAssistantMigrationDeepCopyRetainsImmutableIntent(t *testing.T) {
	migration := &HomeAssistantMigration{
		Spec: HomeAssistantMigrationSpec{
			SourceID:             "source-uid",
			Target:               MigrationTarget{Name: "target", UID: "target-uid"},
			Approvals:            []MigrationApproval{{ID: "restore", Digest: "sha256:approved", Approved: true}},
			SourceTokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "source-token"}, Key: "token"},
			RecoveryKeySecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "recovery"}, Key: "key"},
		},
		Status: HomeAssistantMigrationStatus{
			Phase:      "AwaitingSourceStop",
			Conditions: []metav1.Condition{{Type: "Waiting", Status: metav1.ConditionTrue, Reason: "AwaitingSourceStop"}},
		},
	}
	copy := migration.DeepCopy()
	copy.Spec.Approvals[0].Approved = false
	copy.Spec.SourceTokenSecretRef.Name = "other"
	copy.Spec.RecoveryKeySecretRef.Name = "other"
	copy.Status.Conditions[0].Reason = "Changed"
	if !migration.Spec.Approvals[0].Approved || migration.Status.Conditions[0].Reason != "AwaitingSourceStop" || migration.Spec.SourceTokenSecretRef.Name != "source-token" || migration.Spec.RecoveryKeySecretRef.Name != "recovery" {
		t.Fatal("deep copy modified durable intent or observed condition")
	}
}

func TestHomeAssistantMigrationStatusDoesNotSerializeCredentials(t *testing.T) {
	migration := HomeAssistantMigration{Status: HomeAssistantMigrationStatus{
		Phase:      "Unknown",
		Conditions: []metav1.Condition{{Type: "Outcome", Status: metav1.ConditionUnknown, Reason: "OutcomeUnknown", Message: "operator intervention required"}},
	}}
	encoded, err := json.Marshal(migration.Status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token", "password", "secret", "archive"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("status serialized sensitive field %q: %s", forbidden, encoded)
		}
	}
}

func TestMigrationExecutionInputsDeepCopy(t *testing.T) {
	m := &HomeAssistantMigration{Spec: HomeAssistantMigrationSpec{
		SourceEndpoint: "https://ha.example.test", ArchiveStorage: &MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"},
		Verification: []MigrationVerificationItem{{Kind: "entity", ID: "sensor.example", ExpectedDigest: "sha256:approved"}},
		Resolutions:  []MigrationResolution{{Version: "v1", RequestID: "request", Stage: "backup", Decision: "Abort"}},
	}}
	copy := m.DeepCopy()
	copy.Spec.ArchiveStorage.PVCName = "changed"
	copy.Spec.Verification[0].ID = "changed"
	copy.Spec.Resolutions[0].Decision = "ObserveExternalResult"
	if m.Spec.ArchiveStorage.PVCName != "archive" || m.Spec.Verification[0].ID != "sensor.example" || m.Spec.Resolutions[0].Decision != "Abort" {
		t.Fatal("deep copy shares execution inputs")
	}
}
