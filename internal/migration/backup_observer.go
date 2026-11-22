package migration

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type backupObservationReader interface {
	backupPreflightReader
	backupProfileReader
	GetBackupDetails(context.Context, string, string) (*haclient.BackupDetails, error)
	CanDecryptBackup(context.Context, string, string, string, string) error
	DownloadBackup(context.Context, string, string, string) (*http.Response, error)
}

// ArchiveDispatchedBackup observes the accepted job ID and streams its result.
// It never submits or retries a backup and never publishes Ready without a way
// to authenticate the actual bytes written to the independent volume.
func ArchiveDispatchedBackup(
	ctx context.Context, kube client.Client, source backupObservationReader,
	namespace, name, requestID, dir string, poll time.Duration,
) error {
	err := observeDispatchedBackup(ctx, kube, source, namespace, name, requestID, dir, poll)
	if err != nil {
		recordBackupObservationError(ctx, kube, namespace, name, requestID, err)
	}
	return err
}

// ObserveAndStoreBackup verifies one exact externally identified result. It
// does not validate selected HA data or publish a ready descriptor.
//
//nolint:gocyclo // Validation and lifecycle branches are intentionally fail-closed.
func ObserveAndStoreBackup(ctx context.Context, source backupObservationReader, token string, recoveryKey []byte, jobID, agentID, scopeDigest, dir, filename string) (ArchiveReceipt, error) {
	if ctx == nil || source == nil || token == "" || len(recoveryKey) == 0 || jobID == "" || agentID == "" {
		return ArchiveReceipt{}, fmt.Errorf("backup observation inputs unavailable")
	}
	if err := VerifyBackupScopeAndKey(ctx, source, token, scopeDigest, recoveryKey); err != nil {
		return ArchiveReceipt{}, fmt.Errorf("backup scope or key is not verified")
	}
	preflight := observeBackupPreflight(ctx, source, token)
	currentDigest, err := BackupScopeDigest(preflight)
	if err != nil || currentDigest != scopeDigest {
		return ArchiveReceipt{}, fmt.Errorf("backup settings differ from approved scope")
	}
	details, err := source.GetBackupDetails(ctx, token, jobID)
	if err != nil || details == nil || details.Backup.ID != jobID || !details.Backup.Automatic ||
		details.AgentErrors == nil || len(details.AgentErrors) != 0 {
		return ArchiveReceipt{}, fmt.Errorf("matching backup is unavailable or incomplete")
	}
	if !backupResultScopeMatches(details.Backup, preflight) {
		return ArchiveReceipt{}, fmt.Errorf("backup result scope is incomplete or differs")
	}
	for _, configured := range preflight.AgentIDs {
		agent, ok := details.Backup.Agents[configured]
		if !ok || !agent.Protected || agent.Size <= 0 {
			return ArchiveReceipt{}, fmt.Errorf("backup destination is incomplete or unprotected")
		}
	}
	for _, failed := range details.Backup.FailedAgentIDs {
		if failed == agentID {
			return ArchiveReceipt{}, fmt.Errorf("backup agent failed")
		}
	}
	copy, exists := details.Backup.Agents[agentID]
	if !exists || !copy.Protected || copy.Size <= 0 {
		return ArchiveReceipt{}, fmt.Errorf("protected backup copy unavailable")
	}
	if err := source.CanDecryptBackup(ctx, token, jobID, agentID, string(recoveryKey)); err != nil {
		return ArchiveReceipt{}, fmt.Errorf("backup recovery key could not be verified")
	}
	response, err := source.DownloadBackup(ctx, token, jobID, agentID)
	if err != nil || response == nil || response.Body == nil {
		return ArchiveReceipt{}, fmt.Errorf("backup archive unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 ||
		(response.ContentLength >= 0 && response.ContentLength != copy.Size) {
		return ArchiveReceipt{}, fmt.Errorf("backup archive size or response invalid")
	}
	stored, err := StoreProtectedArchive(ctx, dir, filename, response.Body, copy.Size, true)
	if err != nil {
		return ArchiveReceipt{}, fmt.Errorf("backup archive transfer incomplete")
	}
	if err := VerifyBackupScopeAndKey(ctx, source, token, scopeDigest, recoveryKey); err != nil {
		return ArchiveReceipt{}, fmt.Errorf("backup settings changed during transfer")
	}
	return stored, nil
}
