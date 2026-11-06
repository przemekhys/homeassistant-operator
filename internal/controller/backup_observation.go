package controller

import (
	"context"
	"encoding/json"
	"sort"
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
	var observedKey *string
	if config != nil {
		observedKey = config.CreateBackup.Password
	}
	ref, keyErr := r.protectBackupKey(ctx, ha, observedKey)
	applyKeyStatus := func() {
		if ha.Status.Backup == nil {
			ha.Status.Backup = &hav1.BackupStatus{}
		}
		if keyErr != nil {
			ha.Status.Backup.KeySecretRef = nil
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
	}
	applyKeyStatus()
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
	var finalizeErr error
	if err == nil {
		ha.Status.Backup.ObservedAt = &now
		setBackupCondition(ha, conditionTypeBackupObservation, metav1.ConditionTrue,
			"Observed", "backup information observed")
		r.projectBackupInfo(ha, info, config)
		finalizeErr = r.finalizeAcceptedBackupReceipt(ctx, ha, c, token, info)
	} else {
		setBackupCondition(ha, conditionTypeBackupObservation, metav1.ConditionFalse,
			"ObservationUnavailable", "cannot read backup information")
	}
	// A receipt metadata update may refresh the object and replace its status.
	applyKeyStatus()
	if err := r.Status().Update(ctx, ha); err != nil {
		return ctrl.Result{}, err
	}
	if finalizeErr != nil {
		return ctrl.Result{}, finalizeErr
	}
	return ctrl.Result{RequeueAfter: 120 * time.Second}, nil
}

func (r *HomeAssistantReconciler) finalizeAcceptedBackupReceipt(
	ctx context.Context, ha *hav1.HomeAssistant, c *haclient.Client, token string, info *haclient.BackupInfo,
) error {
	receipt, err := receiptFromHA(ha)
	if err != nil || receipt == nil || (receipt.Phase != "Accepted" && receipt.Phase != "Unknown") ||
		receipt.BackupID == "" {
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

func (r *HomeAssistantReconciler) projectBackupInfo(
	ha *hav1.HomeAssistant, info *haclient.BackupInfo, config *haclient.BackupConfig,
) {
	if ha.Status.Backup == nil {
		ha.Status.Backup = &hav1.BackupStatus{}
	}
	if info.State != "" && info.State != "idle" {
		ha.Status.Backup.LastAttempt = &hav1.BackupAttemptStatus{
			Source: "HomeAssistant", Outcome: "Running",
		}
	}
	var latest *haclient.BackupArchive
	var latestTime time.Time
	for i := range info.Backups {
		archive := &info.Backups[i]
		timestamp, err := time.Parse(time.RFC3339, archive.Date)
		if err != nil || (!latestTime.IsZero() && !timestamp.After(latestTime)) {
			continue
		}
		latest, latestTime = archive, timestamp
	}
	if latest != nil && (info.State == "" || info.State == "idle") {
		r.projectBackupArchive(ha, *latest, latestTime, requiredBackupAgents(config))
	}
	if len(info.AgentErrors) > 0 && (latest == nil || info.State == "" || info.State == "idle") {
		ha.Status.Backup.LastAttempt = &hav1.BackupAttemptStatus{
			Source: "HomeAssistant", Outcome: "Failed", Locations: backupAgentErrorLocations(info.AgentErrors),
		}
	}
	// History is bounded and may contain a newer failure. Preserve the newest
	// independently proven success instead of letting that failure erase it.
	for i := range info.Backups {
		archive := info.Backups[i]
		timestamp, err := time.Parse(time.RFC3339, archive.Date)
		if err != nil || len(archive.FailedAgentIDs) != 0 ||
			!backupArchiveCoversAgents(archive, requiredBackupAgents(config)) {
			continue
		}
		if ha.Status.Backup.LastSuccessfulBackupTime == nil ||
			timestamp.After(ha.Status.Backup.LastSuccessfulBackupTime.Time) {
			when := metav1.NewTime(timestamp)
			ha.Status.Backup.LastSuccessfulBackupTime = &when
		}
	}
}

func requiredBackupAgents(config *haclient.BackupConfig) []string {
	if config == nil || len(config.CreateBackup.AgentIDs) == 0 {
		return nil
	}
	return config.CreateBackup.AgentIDs
}

func (r *HomeAssistantReconciler) projectBackupArchive(
	ha *hav1.HomeAssistant, archive haclient.BackupArchive, timestamp time.Time, expected []string,
) {
	when := metav1.NewTime(timestamp)
	attempt := &hav1.BackupAttemptStatus{
		Source: "HomeAssistant", BackupID: archive.ID, CompletedAt: &when,
		Locations: backupArchiveLocations(archive),
	}
	switch {
	case len(archive.FailedAgentIDs) > 0:
		attempt.Outcome = "Failed"
	case backupArchiveCoversAgents(archive, expected):
		attempt.Outcome = "Succeeded"
		if ha.Status.Backup.LastSuccessfulBackupTime == nil ||
			timestamp.After(ha.Status.Backup.LastSuccessfulBackupTime.Time) {
			ha.Status.Backup.LastSuccessfulBackupTime = &when
		}
	default:
		attempt.Outcome = "Unknown"
	}
	ha.Status.Backup.LastAttempt = attempt
}

func backupArchiveLocations(archive haclient.BackupArchive) []hav1.BackupLocationStatus {
	locations := make([]hav1.BackupLocationStatus, 0, len(archive.Agents)+len(archive.FailedAgentIDs))
	failed := make(map[string]struct{}, len(archive.FailedAgentIDs))
	for _, id := range archive.FailedAgentIDs {
		failed[id] = struct{}{}
	}
	for id := range archive.Agents {
		outcome := "Succeeded"
		if _, isFailed := failed[id]; isFailed {
			outcome = "Failed"
			delete(failed, id)
		}
		locations = append(locations, hav1.BackupLocationStatus{ID: id, Outcome: outcome})
	}
	for id := range failed {
		locations = append(locations, hav1.BackupLocationStatus{ID: id, Outcome: "Failed"})
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i].ID < locations[j].ID })
	return locations
}

func backupAgentErrorLocations(errors map[string]any) []hav1.BackupLocationStatus {
	locations := make([]hav1.BackupLocationStatus, 0, len(errors))
	for id := range errors {
		locations = append(locations, hav1.BackupLocationStatus{ID: id, Outcome: "Failed"})
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i].ID < locations[j].ID })
	return locations
}
