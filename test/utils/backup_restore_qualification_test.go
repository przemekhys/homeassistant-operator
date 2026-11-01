package utils

import "testing"

func TestBackupRestoreQualificationTarget(t *testing.T) {
	target := BackupRestoreQualificationTarget{ImageDigest: "sha256:one", Namespace: "test", TargetUID: "uid"}
	if err := target.Validate("sha256:one", "test", "uid"); err != nil {
		t.Fatal(err)
	}
	if err := target.Validate("sha256:two", "test", "uid"); err == nil {
		t.Fatal("mismatched image accepted")
	}
}
