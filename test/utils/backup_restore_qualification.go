package utils

import (
	"fmt"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const BackupQualificationNamespaceLabel = "ha.homeassistant.io/backup-restore-qualification"

// BackupRestoreQualificationTarget is passed only by the isolated E2E harness.
// Production manager configuration intentionally has no equivalent setting.
type BackupRestoreQualificationTarget struct{ ImageDigest, Namespace, TargetUID string }

func (t BackupRestoreQualificationTarget) Validate(actualImageDigest, namespace, targetUID string) error {
	if !haclient.ValidBackupImageDigest(t.ImageDigest) || t.Namespace == "" || t.TargetUID == "" {
		return fmt.Errorf("qualification target is incomplete")
	}
	if t.ImageDigest != actualImageDigest || t.Namespace != namespace || t.TargetUID != targetUID {
		return fmt.Errorf("qualification target mismatch")
	}
	return nil
}
