package utils

// BackupRestoreCandidate identifies a Home Assistant image which is permitted
// only in the isolated qualification harness. It is deliberately not a runtime
// support declaration.
type BackupRestoreCandidate struct {
	Version         string
	SourceCommit    string
	PlatformDigests map[string]string
}

var (
	// HomeAssistantBackupRestoreCandidate is the first source-reviewed release
	// exercised by the backup/restore qualification tests. Platform manifest
	// pins are candidate inputs, not successful production qualification records.
	HomeAssistantBackupRestoreCandidate = BackupRestoreCandidate{
		Version:      "2026.10.0",
		SourceCommit: "6a811d3359c7b2076dc9e1cf900843a129c044af",
		PlatformDigests: map[string]string{
			"amd64": "sha256:956a5d9effc091888cd34cb0b6fb4f66c448d257d20519e72302557682734fd0",
			"arm64": "sha256:0c73235a9140a9b02e4bf618d06d8c496b12ae70656ecddb0e02ad31ad122be4",
		},
	}

	// HomeAssistantBackupRestoreHistoricalCandidate captures the older source
	// shape used by compatibility tests. It is not a supported runtime version.
	HomeAssistantBackupRestoreHistoricalCandidate = BackupRestoreCandidate{
		Version:      "2026.3.0",
		SourceCommit: "2f9faa53a110f6ac3b0e2216573bdf2a957388",
	}
)
