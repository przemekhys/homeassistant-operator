package utils

import "strings"

const (
	BackupRestoreTestKey     = "backup-restore-test-key"
	BackupRestoreTestToken   = "backup-restore-test-token"
	BackupRestoreArchiveName = "backup-restore-test.tar"
)

// ContainsSensitiveFixture reports whether text contains one of this feature's
// synthetic canaries. It is suitable for assertions over errors and rendered
// diagnostic output without ever using real credentials.
func ContainsSensitiveFixture(text string) bool {
	return strings.Contains(text, BackupRestoreTestKey) || strings.Contains(text, BackupRestoreTestToken)
}
