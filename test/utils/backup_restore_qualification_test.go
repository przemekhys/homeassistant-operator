package utils

import (
	"strings"
	"testing"
)

func TestBackupRestoreQualificationTarget(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	target := BackupRestoreQualificationTarget{ImageDigest: digest, Namespace: "test", TargetUID: "uid"}
	if err := target.Validate(digest, "test", "uid"); err != nil {
		t.Fatal(err)
	}
	if err := target.Validate("sha256:two", "test", "uid"); err == nil {
		t.Fatal("mismatched image accepted")
	}
}
