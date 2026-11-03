package controller

import (
	"context"
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

func setBackupCondition(
	ha *hav1.HomeAssistant, conditionType string, status metav1.ConditionStatus, reason, message string,
) {
	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type: conditionType, Status: status, Reason: reason, Message: message, ObservedGeneration: ha.Generation,
	})
}

func (r *HomeAssistantReconciler) projectBackupInfo(ha *hav1.HomeAssistant, info *haclient.BackupInfo) {
	if len(info.Backups) == 0 {
		return
	}
	var best *haclient.BackupArchive
	for i := range info.Backups {
		archive := &info.Backups[i]
		if len(archive.FailedAgentIDs) != 0 {
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
