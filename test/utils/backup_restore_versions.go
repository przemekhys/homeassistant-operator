package utils

// BackupRestoreCandidate identifies a Home Assistant image which is permitted
// only in the isolated qualification harness. It is deliberately not a runtime
// support declaration.
type BackupRestoreCandidate struct {
	Version      string
	SourceCommit string
}

var (
	// HomeAssistantBackupRestoreCandidate is the first source-reviewed release
	// exercised by the backup/restore qualification tests. Tests must replace
	// the tag with an immutable image digest before executing it.
	HomeAssistantBackupRestoreCandidate = BackupRestoreCandidate{
		Version:      "2026.10.0",
		SourceCommit: "6a811d3359c7b2076dc9e1cf900843a129c044af",
	}

	// HomeAssistantBackupRestoreHistoricalCandidate captures the older source
	// shape used by compatibility tests. It is not a supported runtime version.
	HomeAssistantBackupRestoreHistoricalCandidate = BackupRestoreCandidate{
		Version:      "2026.3.0",
		SourceCommit: "2f9faa53a110f6ac3b0e2216573bdf2a957388",
	}
)
