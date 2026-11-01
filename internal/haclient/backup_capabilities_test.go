package haclient

import "testing"

func TestCapabilityCatalogFailsClosedAndPromotesIndependently(t *testing.T) {
	catalog, err := NewCapabilityCatalog([]QualificationRecord{{ImageDigest: "sha256:qualified", Capabilities: map[BackupCapability]bool{CapabilityBackup: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.Supports("sha256:qualified", CapabilityBackup) {
		t.Fatal("qualified backup rejected")
	}
	if catalog.Supports("sha256:qualified", CapabilityRestoreExisting) {
		t.Fatal("backup qualification promoted restore")
	}
	if catalog.Supports("sha256:unknown", CapabilityBackup) {
		t.Fatal("unknown image accepted")
	}
}

func TestQualificationScopeRejectsDifferentTarget(t *testing.T) {
	scope := QualificationScope{ImageDigest: "sha256:one", Namespace: "isolated", TargetUID: "uid"}
	if err := scope.Validate("sha256:one", "isolated", "uid"); err != nil {
		t.Fatal(err)
	}
	if err := scope.Validate("sha256:one", "other", "uid"); err == nil {
		t.Fatal("scope accepted different namespace")
	}
}
