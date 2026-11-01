package utils

import "fmt"

// BackupRestoreQualificationTarget is passed only by the isolated E2E harness.
// Production manager configuration intentionally has no equivalent setting.
type BackupRestoreQualificationTarget struct{ ImageDigest, Namespace, TargetUID string }

func (t BackupRestoreQualificationTarget) Validate(actualImageDigest, namespace, targetUID string) error {
	if t.ImageDigest == "" || t.Namespace == "" || t.TargetUID == "" {
		return fmt.Errorf("qualification target is incomplete")
	}
	if t.ImageDigest != actualImageDigest || t.Namespace != namespace || t.TargetUID != targetUID {
		return fmt.Errorf("qualification target mismatch")
	}
	return nil
}
