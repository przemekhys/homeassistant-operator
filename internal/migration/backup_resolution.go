package migration

import (
	"context"
	"fmt"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
)

type backupResolutionReader interface {
	backupPreflightReader
	GetBackupDetails(context.Context, string, string) (*haclient.BackupDetails, error)
}

// BackupResolutionObservation is a point-in-time source observation, not a
// receipt, a dispatch permit, a verified archive, or proof of ownership.
type BackupResolutionObservation struct {
	BackupID, AgentID string
	Size              int64
}

// ReadBackupResolution reads only the explicitly indicated backup and current
// protected scope. The caller must separately verify the trust of the source
// transport and the durable execution/evidence bindings. No list/latest or HA
// mutation operation is exposed by this interface.
func ReadBackupResolution(
	ctx context.Context, source backupResolutionReader, token string, key []byte,
	r ha.MigrationExecutionRequest, e migrationprotocol.Evidence,
) (BackupResolutionObservation, error) {
	var result BackupResolutionObservation
	jobID := e.JobID
	if jobID == "" {
		jobID = e.BackupID
	}
	if ctx == nil || source == nil || token == "" || len(key) == 0 || r.Stage != "backup" ||
		e.Version != "v1" || e.Stage != r.Stage || e.OperationUID != r.OperationUID || e.RequestID != r.RequestID ||
		e.SourceID != r.SourceID || e.TargetUID != r.Target.UID || !nativeBackupIDPattern.MatchString(jobID) ||
		(e.BackupID != "" && e.BackupID != jobID) {
		return result, fmt.Errorf("external backup evidence invalid")
	}
	if VerifyBackupScopeAndKey(ctx, source, token, r.ScopeDigest, key) != nil {
		return result, fmt.Errorf("backup scope unavailable")
	}
	preflight := observeBackupPreflight(ctx, source, token)
	details, err := source.GetBackupDetails(ctx, token, jobID)
	if err != nil || details == nil || details.Backup.ID != jobID || details.Backup.HomeAssistantVersion != "2026.11.0" ||
		details.Backup.Date == "" || !details.Backup.Automatic || details.AgentErrors == nil || len(details.AgentErrors) != 0 ||
		!backupResultScopeMatches(details.Backup, preflight) {
		return result, fmt.Errorf("external backup result unavailable")
	}
	for _, id := range preflight.AgentIDs {
		copy, ok := details.Backup.Agents[id]
		if !ok || !copy.Protected || copy.Size <= 0 {
			return result, fmt.Errorf("external backup result unavailable")
		}
	}
	agent := preflight.AgentIDs[0]
	return BackupResolutionObservation{BackupID: details.Backup.ID, AgentID: agent, Size: details.Backup.Agents[agent].Size}, nil
}
