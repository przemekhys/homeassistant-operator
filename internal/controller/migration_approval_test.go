package controller

import (
	"strings"
	"testing"
)

func TestMigrationOverwriteApprovalIsBoundAndNeverEnablesUnsafeRace(t *testing.T) {
	approval := &migrationOverwriteApproval{
		ObjectUID:        "object-uid",
		Generation:       7,
		ComparisonDigest: "sha256:comparison",
		Approved:         true,
	}
	if !migrationOverwriteApproved(approval, "object-uid", 7, "sha256:comparison") {
		t.Fatal("matching approval rejected")
	}
	for _, tc := range []struct {
		uid        string
		generation int64
		digest     string
	}{{"other", 7, "sha256:comparison"}, {"object-uid", 8, "sha256:comparison"}, {"object-uid", 7, "sha256:changed"}} {
		if migrationOverwriteApproved(approval, tc.uid, tc.generation, tc.digest) {
			t.Fatal("stale approval accepted")
		}
	}
	if migrationOverwriteSafe(approval, "object-uid", 7, "sha256:comparison") {
		t.Fatal("approval enabled unsafe overwrite")
	}
}

func TestParseMigrationOverwriteApproval(t *testing.T) {
	valid := `{"version":"v1","approved":true,"objectUID":"object","generation":3,"targetUID":"target","sourceID":"source","comparisonDigest":"sha256:abc"}`
	identity := migrationIdentity{SourceID: "source", TargetUID: "target"}
	for _, tc := range []struct {
		name, raw, uid, digest string
		generation             int64
		identity               migrationIdentity
		want                   bool
	}{
		{"valid", valid, "object", "sha256:abc", 3, identity, true},
		{"absent", "", "object", "sha256:abc", 3, identity, false},
		{"revoked", strings.Replace(valid, `"approved":true`, `"approved":false`, 1), "object", "sha256:abc", 3, identity, false},
		{"duplicate", strings.Replace(valid, `"version":"v1"`, `"version":"v1","version":"v1"`, 1), "object", "sha256:abc", 3, identity, false},
		{"unknown", strings.Replace(valid, `"version":"v1"`, `"version":"v1","extra":true`, 1), "object", "sha256:abc", 3, identity, false},
		{"wrong field case", strings.Replace(valid, `"version"`, `"Version"`, 1), "object", "sha256:abc", 3, identity, false},
		{"version", strings.Replace(valid, `"v1"`, `"v2"`, 1), "object", "sha256:abc", 3, identity, false},
		{"object", valid, "other", "sha256:abc", 3, identity, false},
		{"generation", valid, "object", "sha256:abc", 4, identity, false},
		{"target", valid, "object", "sha256:abc", 3, migrationIdentity{SourceID: "source", TargetUID: "other"}, false},
		{"source", valid, "object", "sha256:abc", 3, migrationIdentity{SourceID: "other", TargetUID: "target"}, false},
		{"digest", valid, "object", "sha256:other", 3, identity, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validMigrationOverwriteAnnotation(tc.raw, tc.uid, tc.generation, tc.identity, tc.digest); got != tc.want {
				t.Fatalf("valid = %t, want %t", got, tc.want)
			}
		})
	}
}
