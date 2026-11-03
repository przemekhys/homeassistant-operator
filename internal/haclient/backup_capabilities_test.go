package haclient

import (
	"strings"
	"testing"
)

var qualifiedDigest = "sha256:" + strings.Repeat("a", 64)

func TestCapabilityCatalogFailsClosedAndPromotesIndependently(t *testing.T) {
	catalog, err := NewCapabilityCatalog([]QualificationRecord{{
		ImageDigest: qualifiedDigest, Capabilities: map[BackupCapability]bool{CapabilityBackup: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.Supports(qualifiedDigest, CapabilityBackup) {
		t.Fatal("qualified backup rejected")
	}
	if catalog.Supports(qualifiedDigest, CapabilityRestoreExisting) {
		t.Fatal("backup qualification promoted restore")
	}
	if catalog.Supports("sha256:unknown", CapabilityBackup) {
		t.Fatal("unknown image accepted")
	}
	if catalog.BackupJobIsArchiveID(qualifiedDigest) {
		t.Fatal("backup support implicitly granted an unreviewed archive/job correlation rule")
	}
}

func TestCapabilityCatalogOwnsItsQualificationRecords(t *testing.T) {
	capabilities := map[BackupCapability]bool{CapabilityBackup: true}
	catalog, err := NewCapabilityCatalog(
		[]QualificationRecord{{ImageDigest: qualifiedDigest, Capabilities: capabilities}},
	)
	if err != nil {
		t.Fatal(err)
	}
	capabilities[CapabilityRestoreExisting] = true
	capabilities[CapabilityBackup] = false
	backup := catalog.Supports(qualifiedDigest, CapabilityBackup)
	if !backup || catalog.Supports(qualifiedDigest, CapabilityRestoreExisting) {
		t.Fatal("caller changed execution authority after catalog creation")
	}
}

func TestBackupArchiveCorrelationRequiresItsOwnReviewedFlagAndBackupCapability(t *testing.T) {
	other := "sha256:" + strings.Repeat("b", 64)
	catalog, err := NewCapabilityCatalog([]QualificationRecord{
		{ImageDigest: qualifiedDigest, JobIDIsArchiveID: true,
			Capabilities: map[BackupCapability]bool{CapabilityBackup: true}},
		{ImageDigest: other, JobIDIsArchiveID: true,
			Capabilities: map[BackupCapability]bool{CapabilityRestoreExisting: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.BackupJobIsArchiveID(qualifiedDigest) || catalog.BackupJobIsArchiveID(other) {
		t.Fatal("archive correlation did not require both reviewed backup contracts")
	}
	var unavailable *CapabilityCatalog
	if unavailable.BackupJobIsArchiveID(qualifiedDigest) {
		t.Fatal("absent catalog granted archive correlation")
	}
}

func TestLoadCapabilityCatalogRejectsUnreviewedSchemas(t *testing.T) {
	for _, document := range []string{
		`{}`, `{"formatVersion":2,"records":[]}`, `{"formatVersion":1,"records":null}`,
		`{"formatVersion":1,"records":[],"candidate":true}`,
		`{"formatVersion":1,"records":[]} {}`,
		`{"formatVersion":1,"records":[{"imageDigest":"latest","capabilities":{"backup":true}}]}`,
		`{"formatVersion":1,"records":[{"imageDigest":"` + qualifiedDigest + `","capabilities":{"all":true}}]}`,
		strings.Repeat(" ", 256*1024+1),
	} {
		if _, err := LoadCapabilityCatalog(strings.NewReader(document)); err == nil {
			t.Fatal("invalid catalog accepted")
		}
	}
	duplicates := []QualificationRecord{{ImageDigest: qualifiedDigest}, {ImageDigest: qualifiedDigest}}
	if _, err := NewCapabilityCatalog(duplicates); err == nil {
		t.Fatal("duplicate image accepted")
	}
}

func TestProductionCatalogHasNoCandidateEnvironmentBypass(t *testing.T) {
	t.Setenv("QUALIFICATION_IMAGE_DIGEST", qualifiedDigest)
	t.Setenv("QUALIFICATION_NAMESPACE", "isolated")
	t.Setenv("QUALIFICATION_TARGET_UID", "uid")
	catalog, err := NewProductionCapabilityCatalog()
	if err != nil {
		t.Fatal(err)
	}
	capabilities := []BackupCapability{CapabilityBackup, CapabilityRestoreExisting, CapabilityRestoreOnboarding}
	for _, capability := range capabilities {
		if catalog.Supports(qualifiedDigest, capability) {
			t.Fatal("candidate environment granted production support")
		}
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
