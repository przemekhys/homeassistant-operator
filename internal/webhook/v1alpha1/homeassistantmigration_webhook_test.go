package v1alpha1

import (
	"context"
	"strings"
	"testing"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func TestProxyCredentialBindingIsImmutableAndCancellationAware(t *testing.T) {
	validator := &HomeAssistantMigrationCustomValidator{}
	old := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{ProxyCredentialBinding: &hav1alpha1.MigrationProxyCredentialBinding{Version: "v1"}}}
	changed := old.DeepCopy()
	changed.Spec.ProxyCredentialBinding = nil
	if _, err := validator.ValidateUpdate(context.Background(), old, changed); err == nil {
		t.Fatal("removed proxy binding accepted")
	}
	old.Spec.ProxyCredentialBinding = nil
	changed.Spec.ProxyCredentialBinding = &hav1alpha1.MigrationProxyCredentialBinding{Version: "v1"}
	changed.Spec.CancelRequested = true
	if _, err := validator.ValidateUpdate(context.Background(), old, changed); err == nil {
		t.Fatal("proxy binding after cancellation accepted")
	}
}

func TestValidateHomeAssistantMigration(t *testing.T) {
	valid := &hav1alpha1.HomeAssistantMigration{
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			SourceID: "source",
			Target:   hav1alpha1.MigrationTarget{Name: "target", UID: "uid"},
			Archive: &hav1alpha1.MigrationArchive{
				BackupID: "backup",
				AgentID:  "backup.local",
				Digest:   "archive",
			},
			RestoreScopeDigest: "scope",
			Approvals: []hav1alpha1.MigrationApproval{
				{
					ID:            "restore",
					Digest:        "digest",
					TargetUID:     "uid",
					ArchiveDigest: "archive",
					ScopeDigest:   "scope",
					Approved:      true,
				},
			},
		},
	}
	if err := validateHomeAssistantMigration(valid); err != nil {
		t.Fatal(err)
	}
	valid.Spec.Approvals = append(valid.Spec.Approvals, valid.Spec.Approvals[0])
	if err := validateHomeAssistantMigration(valid); err == nil {
		t.Fatal("duplicate approval was accepted")
	}
}

func TestSourceTokenReferenceMustBeRequiredAndComplete(t *testing.T) {
	optional := true
	for _, ref := range []*corev1.SecretKeySelector{
		{LocalObjectReference: corev1.LocalObjectReference{Name: ""}, Key: "token"},
		{LocalObjectReference: corev1.LocalObjectReference{Name: "source"}, Key: ""},
		{LocalObjectReference: corev1.LocalObjectReference{Name: "source"}, Key: "token", Optional: &optional},
	} {
		migration := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"}, SourceTokenSecretRef: ref}}
		if err := validateHomeAssistantMigration(migration); err == nil {
			t.Fatalf("accepted invalid reference: %#v", ref)
		}
	}
}

func TestRecoveryKeyReferenceMustBeRequiredAndComplete(t *testing.T) {
	optional := true
	for _, ref := range []*corev1.SecretKeySelector{
		{LocalObjectReference: corev1.LocalObjectReference{Name: ""}, Key: "key"},
		{LocalObjectReference: corev1.LocalObjectReference{Name: "recovery"}, Key: ""},
		{LocalObjectReference: corev1.LocalObjectReference{Name: "recovery"}, Key: "key", Optional: &optional},
	} {
		migration := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{
			SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"}, RecoveryKeySecretRef: ref,
		}}
		if err := validateHomeAssistantMigration(migration); err == nil {
			t.Fatal("accepted invalid recovery reference")
		}
	}
}

func TestApprovedBackupRequiresMatchingReportTargetAndScope(t *testing.T) {
	migration := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{
		SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"},
		ReportID: "report", BackupScopeDigest: "scope",
		Approvals: []hav1alpha1.MigrationApproval{{ID: "backup", Approved: true,
			Digest: "report", TargetUID: "uid", ScopeDigest: "scope"}},
	}}
	if err := validateHomeAssistantMigration(migration); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*hav1alpha1.MigrationApproval){
		func(a *hav1alpha1.MigrationApproval) { a.Digest = "other" },
		func(a *hav1alpha1.MigrationApproval) { a.TargetUID = "other" },
		func(a *hav1alpha1.MigrationApproval) { a.ScopeDigest = "other" },
	} {
		candidate := migration.DeepCopy()
		mutate(&candidate.Spec.Approvals[0])
		if err := validateHomeAssistantMigration(candidate); err == nil {
			t.Fatal("accepted unbound backup approval")
		}
	}
}

func TestAdditionalApprovalsRequireBoundInputs(t *testing.T) {
	base := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{
		SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"}, ReportID: "report", BackupScopeDigest: "scope",
		Archive: &hav1alpha1.MigrationArchive{BackupID: "backup", AgentID: "agent", Digest: "archive"},
	}}
	for _, approval := range []hav1alpha1.MigrationApproval{
		{ID: "backup-settings-stable", Approved: true, Digest: "report", ScopeDigest: "scope", TargetUID: "uid"},
		{ID: "resume", Approved: true, Digest: "verification", ArchiveDigest: "archive", TargetUID: "uid"},
		{ID: "manual-cutover", Approved: true, Digest: "verification", ArchiveDigest: "archive", TargetUID: "uid"},
	} {
		m := base.DeepCopy()
		m.Spec.Approvals = []hav1alpha1.MigrationApproval{approval}
		if err := validateHomeAssistantMigration(m); err != nil {
			t.Fatalf("%s: %v", approval.ID, err)
		}
		m.Spec.Approvals[0].TargetUID = "other"
		if err := validateHomeAssistantMigration(m); err == nil {
			t.Fatalf("%s: accepted another target", approval.ID)
		}
	}
}

func TestVerificationItemsMustBeCompleteAndUnique(t *testing.T) {
	m := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{
		SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"},
		Verification: []hav1alpha1.MigrationVerificationItem{{Kind: "EntityRegistryIdentity", ID: "sensor.example", ExpectedDigest: "sha256:" + strings.Repeat("a", 64)}},
	}}
	if err := validateHomeAssistantMigration(m); err != nil {
		t.Fatal(err)
	}
	m.Spec.Verification = append(m.Spec.Verification, m.Spec.Verification[0])
	if err := validateHomeAssistantMigration(m); err == nil {
		t.Fatal("accepted duplicate verification item")
	}
	m.Spec.Verification = m.Spec.Verification[:1]
	m.Spec.Verification[0].ExpectedDigest = ""
	if err := validateHomeAssistantMigration(m); err == nil {
		t.Fatal("accepted verification without expected digest")
	}
}

func TestMigrationReportAndBackupScopeAreImmutable(t *testing.T) {
	old := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{
		SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"},
		ReportID: "report", BackupScopeDigest: "scope",
	}}
	validator := &HomeAssistantMigrationCustomValidator{}
	for _, mutate := range []func(*hav1alpha1.HomeAssistantMigration){
		func(m *hav1alpha1.HomeAssistantMigration) { m.Spec.ReportID = "other" },
		func(m *hav1alpha1.HomeAssistantMigration) { m.Spec.BackupScopeDigest = "other" },
		func(m *hav1alpha1.HomeAssistantMigration) { m.Spec.BackupScopeDigest = "" },
	} {
		current := old.DeepCopy()
		mutate(current)
		if _, err := validator.ValidateUpdate(context.Background(), old, current); err == nil {
			t.Fatal("accepted changed report or backup scope")
		}
	}
}

func TestResolutionValidationAndAppendOnly(t *testing.T) {
	validator := &HomeAssistantMigrationCustomValidator{}
	old := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{
		SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"},
		Resolutions: []hav1alpha1.MigrationResolution{{Version: "v1", RequestID: "sha256:" + strings.Repeat("a", 64), Stage: "backup", Decision: "Abort"}},
	}}
	if _, err := validator.ValidateCreate(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	changed := old.DeepCopy()
	changed.Spec.Resolutions[0].Decision = "ObserveExternalResult"
	if _, err := validator.ValidateUpdate(context.Background(), old, changed); err == nil {
		t.Fatal("accepted changed resolution")
	}
	changed = old.DeepCopy()
	changed.Spec.Resolutions = nil
	if _, err := validator.ValidateUpdate(context.Background(), old, changed); err == nil {
		t.Fatal("accepted removed resolution")
	}
	changed = old.DeepCopy()
	changed.Spec.Resolutions = append(changed.Spec.Resolutions, hav1alpha1.MigrationResolution{Version: "v1", RequestID: "other", Stage: "restore", Decision: "Retry"})
	if _, err := validator.ValidateUpdate(context.Background(), old, changed); err == nil {
		t.Fatal("accepted retry")
	}
	changed = old.DeepCopy()
	changed.Spec.Resolutions = append(changed.Spec.Resolutions, hav1alpha1.MigrationResolution{Version: "v1", RequestID: "other", Stage: "restore", Decision: "ObserveExternalResult", EvidenceRef: &hav1alpha1.MigrationEvidenceReference{Name: "https://user:password@example.test"}})
	if _, err := validator.ValidateUpdate(context.Background(), old, changed); err == nil {
		t.Fatal("accepted credential-bearing evidence reference")
	}
}

func TestSourceEndpointRejectsCredentials(t *testing.T) {
	for _, endpoint := range []string{"https://user:password@example.test", "https://example.test/?token=secret", "https://example.test/#secret", "file:///tmp/source"} {
		m := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"}, SourceEndpoint: endpoint}}
		if err := validateHomeAssistantMigration(m); err == nil {
			t.Fatalf("accepted unsafe source endpoint %q", endpoint)
		}
	}
}

func TestSourceEndpointMustMatchApprovedSourceIdentity(t *testing.T) {
	m := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{
		SourceID: "https://source.example.test", SourceEndpoint: "https://other.example.test",
		Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"},
	}}
	if err := validateHomeAssistantMigration(m); err == nil {
		t.Fatal("accepted endpoint unrelated to source identity")
	}
	m.Spec.SourceEndpoint = m.Spec.SourceID
	if err := validateHomeAssistantMigration(m); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptedRequestAnnotationCannotBeChanged(t *testing.T) {
	old := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"}}}
	old.Annotations = map[string]string{"ha.homeassistant.io/migration-request-backup": "accepted"}
	current := old.DeepCopy()
	delete(current.Annotations, "ha.homeassistant.io/migration-request-backup")
	if _, err := (&HomeAssistantMigrationCustomValidator{}).ValidateUpdate(context.Background(), old, current); err == nil {
		t.Fatal("accepted deletion of request")
	}
}

func TestAcceptedBackupInputsCannotBeChanged(t *testing.T) {
	old := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{
		SourceID: "https://source.example", SourceEndpoint: "https://source.example", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"},
		ArchiveStorage: &hav1alpha1.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"},
	}}
	old.Annotations = map[string]string{"ha.homeassistant.io/migration-request-backup": "accepted"}
	current := old.DeepCopy()
	current.Spec.ArchiveStorage.PVCName = "different"
	if _, err := (&HomeAssistantMigrationCustomValidator{}).ValidateUpdate(context.Background(), old, current); err == nil {
		t.Fatal("accepted changed archive storage")
	}
}
