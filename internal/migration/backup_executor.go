package migration

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var nativeBackupIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// BackupDispatchReceipt is durable evidence of a single submission attempt.
// It deliberately excludes credentials, response bodies, and source URLs.
type BackupDispatchReceipt struct {
	RequestID     string `json:"requestID,omitempty"`
	OperationUID  string `json:"operationUID"`
	TargetUID     string `json:"targetUID"`
	ReportID      string `json:"reportID"`
	ScopeDigest   string `json:"scopeDigest"`
	Phase         string `json:"phase"`
	JobID         string `json:"jobID,omitempty"`
	ArchiveDigest string `json:"archiveDigest,omitempty"`
	ArchiveSize   int64  `json:"archiveSize,omitempty"`
	ArchivePVCUID string `json:"archivePVCUID,omitempty"`
	BlockedReason string `json:"blockedReason,omitempty"`
}

// BackupDispatchStore must claim atomically and must not replace an existing
// receipt. SaveAccepted must use a compare-and-swap on the claimed version.
type BackupDispatchStore interface {
	Claim(context.Context, BackupDispatchReceipt) (bool, error)
	SaveAccepted(context.Context, BackupDispatchReceipt) error
}

// BackupDispatcher is the non-idempotent native backup endpoint.
type BackupDispatcher interface {
	backupPreflightReader
	GenerateBackupWithAutomaticSettings(context.Context, string) (string, error)
}

// DispatchBackupOnce rechecks scope and recovery key before submitting at most
// once. The caller must verify the durable approval before calling. Once a claim exists, neither failure nor a restart
// authorises a second dispatch. A returned ID is acceptance, not backup completion.
func DispatchBackupOnce(ctx context.Context, store BackupDispatchStore, source BackupDispatcher,
	token string, recoveryKey []byte, receipt BackupDispatchReceipt,
) (BackupDispatchReceipt, error) {
	if ctx == nil || store == nil || source == nil || strings.TrimSpace(token) == "" ||
		strings.TrimSpace(receipt.OperationUID) == "" || strings.TrimSpace(receipt.TargetUID) == "" ||
		strings.TrimSpace(receipt.ReportID) == "" || strings.TrimSpace(receipt.ScopeDigest) == "" ||
		receipt.Phase != "" || receipt.JobID != "" || receipt.ArchiveDigest != "" || receipt.ArchiveSize != 0 || receipt.ArchivePVCUID != "" || receipt.BlockedReason != "" {
		return BackupDispatchReceipt{}, fmt.Errorf("backup dispatch requires complete verified intent")
	}
	if err := VerifyBackupScopeAndKey(ctx, source, token, receipt.ScopeDigest, recoveryKey); err != nil {
		return BackupDispatchReceipt{}, fmt.Errorf("backup scope or recovery key verification failed")
	}
	receipt.Phase = "Dispatching"
	claimed, err := store.Claim(ctx, receipt)
	if err != nil {
		return BackupDispatchReceipt{}, fmt.Errorf("cannot persist backup dispatch claim")
	}
	if !claimed {
		return BackupDispatchReceipt{}, fmt.Errorf("backup dispatch already claimed; inspect its outcome")
	}
	jobID, err := source.GenerateBackupWithAutomaticSettings(ctx, token)
	if err != nil || !nativeBackupIDPattern.MatchString(jobID) || strings.Contains(jobID, token) ||
		(len(recoveryKey) != 0 && strings.Contains(jobID, string(recoveryKey))) {
		return receipt, fmt.Errorf("backup submission outcome unknown")
	}
	receipt.Phase, receipt.JobID = "Accepted", jobID
	if err := store.SaveAccepted(ctx, receipt); err != nil {
		return BackupDispatchReceipt{}, fmt.Errorf("backup submission outcome could not be persisted")
	}
	return receipt, nil
}
