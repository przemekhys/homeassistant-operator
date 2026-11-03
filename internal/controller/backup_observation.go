package controller

import (
	"context"
	"encoding/json"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const (
	conditionTypeBackupObservation  = "BackupObservationReady"
	conditionTypeBackupKeyProtected = "BackupKeyProtected"
)

func (r *HomeAssistantReconciler) reconcileBackupObservation(
	ctx context.Context, ha *hav1.HomeAssistant, c *haclient.Client, token string, config *haclient.BackupConfig,
) (ctrl.Result, error) {
	if ha.Status.Backup == nil {
		ha.Status.Backup = &hav1.BackupStatus{}
	}
	now := metav1.Now()
	agents, err := c.ListBackupAgents(ctx, token)
	if err != nil {
		setBackupCondition(ha, conditionTypeBackupObservation, metav1.ConditionFalse,
			"ObservationUnavailable", "cannot read backup information")
		_ = r.Status().Update(ctx, ha)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	ha.Status.Backup.Agents = make([]hav1.BackupAgentStatus, 0, len(agents))
	for _, agent := range agents {
		ha.Status.Backup.Agents = append(ha.Status.Backup.Agents, hav1.BackupAgentStatus{ID: agent.ID, Name: agent.Name})
	}
	info, err := c.GetBackupInfo(ctx, token)
	if err == nil {
		ha.Status.Backup.ObservedAt = &now
		setBackupCondition(ha, conditionTypeBackupObservation, metav1.ConditionTrue,
			"Observed", "backup information observed")
		r.projectBackupInfo(ha, info)
		if err := r.finalizeAcceptedBackupReceipt(ctx, ha, c, token, info); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		setBackupCondition(ha, conditionTypeBackupObservation, metav1.ConditionFalse,
			"ObservationUnavailable", "cannot read backup information")
	}
	ref, keyErr := r.protectBackupKey(ctx, ha, config.CreateBackup.Password)
	if keyErr != nil {
		setBackupCondition(ha, conditionTypeBackupKeyProtected, metav1.ConditionFalse,
			"SecretWriteFailed", "cannot protect backup encryption key")
	} else if ref == nil {
		ha.Status.Backup.KeySecretRef = nil
		setBackupCondition(ha, conditionTypeBackupKeyProtected, metav1.ConditionUnknown,
			"EncryptionNotEnabled", "no backup encryption key observed")
	} else {
		ha.Status.Backup.KeySecretRef = ref
		setBackupCondition(ha, conditionTypeBackupKeyProtected, metav1.ConditionTrue,
			"Protected", "current backup encryption key is protected")
	}
	if err := r.Status().Update(ctx, ha); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *HomeAssistantReconciler) finalizeAcceptedBackupReceipt(
	ctx context.Context, ha *hav1.HomeAssistant, c *haclient.Client, token string, info *haclient.BackupInfo,
) error {
	receipt, err := receiptFromHA(ha)
	if err != nil || receipt == nil || receipt.Phase != "Accepted" || receipt.BackupID == "" {
		return err
	}
	var found bool
	for _, archive := range info.Backups {
		if archive.ID == receipt.BackupID {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	details, err := c.GetBackupDetails(ctx, token, receipt.BackupID)
	if err != nil {
		return nil
	}
	archive := details.Backup
	if len(archive.FailedAgentIDs) > 0 {
		receipt.Phase, receipt.Reason = "Failed", "DestinationFailed"
	} else if backupArchiveCoversAgents(archive, receipt.ExpectedAgentIDs) {
		receipt.Phase, receipt.Reason = "Succeeded", ""
	} else {
		return nil
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if ha.Annotations == nil {
		ha.Annotations = map[string]string{}
	}
	ha.Annotations[backupReceiptAnnotation] = string(data)
	if err := r.Update(ctx, ha); err != nil {
		return err
	}
	setBackupRequestStatus(ha, receipt)
	return nil
}

func backupArchiveCoversAgents(archive haclient.BackupArchive, expected []string) bool {
	if len(expected) == 0 || len(archive.Agents) == 0 {
		return false
	}
	for _, id := range expected {
		if _, found := archive.Agents[id]; !found {
			return false
		}
	}
	return true
}

func setBackupCondition(
	ha *hav1.HomeAssistant, conditionType string, status metav1.ConditionStatus, reason, message string,
) {
	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type: conditionType, Status: status, Reason: reason, Message: message, ObservedGeneration: ha.Generation,
	})
}

func (r *HomeAssistantReconciler) projectBackupInfo(ha *hav1.HomeAssistant, info *haclient.BackupInfo) {
	if ha.Status.Backup == nil {
		ha.Status.Backup = &hav1.BackupStatus{}
	}
	if info.State != "" && info.State != "idle" {
		ha.Status.Backup.LastAttempt = &hav1.BackupAttemptStatus{
			Source: "HomeAssistant", Outcome: "Running",
		}
	}
	if len(info.Backups) == 0 {
		return
	}
	var best *haclient.BackupArchive
	for i := range info.Backups {
		archive := &info.Backups[i]
		// An archive without final destination metadata is not enough evidence
		// for a full success. It may still be uploading or lack the history
		// required to prove where the data reached.
		if len(archive.FailedAgentIDs) != 0 || len(archive.Agents) == 0 {
			continue
		}
		if best == nil || archive.Date > best.Date {
			best = archive
		}
	}
	if best == nil {
		return
	}
	if timestamp, err := time.Parse(time.RFC3339, best.Date); err == nil {
		when := metav1.NewTime(timestamp)
		ha.Status.Backup.LastSuccessfulBackupTime = &when
	}
}
