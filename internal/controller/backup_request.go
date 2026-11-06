package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const (
	backupRequestAnnotation = "ha.homeassistant.io/backup-request"
	backupReceiptAnnotation = "ha.homeassistant.io/backup-receipt"
)

var backupRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// backupReceipt is deliberately small. It records the safety boundary before a
// non-idempotent HA command, not a backup history.
type backupReceipt struct {
	Version          int       `json:"version"`
	InstanceUID      string    `json:"instanceUID"`
	RequestID        string    `json:"requestID"`
	Phase            string    `json:"phase"`
	ClaimedAt        time.Time `json:"claimedAt"`
	ExpectedAgentIDs []string  `json:"expectedAgentIDs"`
	BackupID         string    `json:"backupID,omitempty"`
	Reason           string    `json:"reason,omitempty"`
}

func receiptFromHA(ha *hav1.HomeAssistant) (*backupReceipt, error) {
	if ha.Annotations == nil || ha.Annotations[backupReceiptAnnotation] == "" {
		return nil, nil
	}
	var receipt backupReceipt
	if err := json.Unmarshal([]byte(ha.Annotations[backupReceiptAnnotation]), &receipt); err != nil {
		return nil, err
	}
	if receipt.Version != 1 || receipt.InstanceUID != string(ha.UID) {
		return nil, fmt.Errorf("invalid backup receipt")
	}
	if !backupRequestIDPattern.MatchString(receipt.RequestID) || receipt.ClaimedAt.IsZero() ||
		!validBackupReceiptPhase(receipt.Phase) || !uniqueNonEmptyStrings(receipt.ExpectedAgentIDs) {
		return nil, fmt.Errorf("invalid backup receipt")
	}
	return &receipt, nil
}

func validBackupReceiptPhase(phase string) bool {
	switch phase {
	case "Dispatching", "Accepted", "Succeeded", "Failed", "Unknown":
		return true
	default:
		return false
	}
}

func uniqueNonEmptyStrings(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func setBackupRequestStatus(ha *hav1.HomeAssistant, receipt *backupReceipt) {
	if ha.Status.Backup == nil {
		ha.Status.Backup = &hav1.BackupStatus{}
	}
	if receipt == nil {
		ha.Status.Backup.Request = nil
		return
	}
	previous := ha.Status.Backup.Request
	changed := previous == nil || previous.ID != receipt.RequestID || previous.Phase != receipt.Phase
	claimed := metav1.NewTime(receipt.ClaimedAt)
	ha.Status.Backup.Request = &hav1.BackupRequestStatus{
		ID: receipt.RequestID, Phase: receipt.Phase, BackupID: receipt.BackupID, ClaimedAt: &claimed,
	}
	if changed && validBackupReceiptPhase(receipt.Phase) {
		outcome := receipt.Phase
		if outcome == "Dispatching" || outcome == "Accepted" {
			outcome = "Running"
		}
		ha.Status.Backup.LastAttempt = &hav1.BackupAttemptStatus{
			Source: "Operator", RequestID: receipt.RequestID, BackupID: receipt.BackupID, Outcome: outcome,
		}
	}
}

func (r *HomeAssistantReconciler) reconcileBackupRequest(
	ctx context.Context, ha *hav1.HomeAssistant, c *haclient.Client, token string, config *haclient.BackupConfig,
) (ctrl.Result, error) {
	requestID := ""
	if ha.Annotations != nil {
		requestID = ha.Annotations[backupRequestAnnotation]
	}
	receipt, receiptErr := receiptFromHA(ha)
	if receiptErr != nil {
		return r.backupRequestStatus(ctx, ha, nil, "InvalidReceipt", "backup receipt is invalid")
	}
	if receipt != nil {
		setBackupRequestStatus(ha, receipt)
		if receipt.Phase == "Dispatching" {
			receipt.Phase, receipt.Reason = "Unknown", "SubmissionUncertain"
			return r.persistBackupReceipt(ctx, ha, receipt, 30*time.Second)
		}
		if receipt.Phase == "Accepted" && time.Since(receipt.ClaimedAt) > 15*time.Minute {
			receipt.Phase, receipt.Reason = "Unknown", "OutcomeUnavailable"
			return r.persistBackupReceipt(ctx, ha, receipt, 30*time.Second)
		}
		if receipt.RequestID == requestID || requestID == "" {
			return r.backupRequestStatus(ctx, ha, receipt, "Observed", "backup request already claimed")
		}
		// A different ID is only allowed after an explicitly resolved request.
		if receipt.Phase == "Dispatching" || receipt.Phase == "Accepted" {
			return r.backupRequestStatus(ctx, ha, receipt, "RequestInProgress", "wait for the current backup request")
		}
	}
	if requestID == "" {
		return ctrl.Result{}, nil
	}
	if !backupRequestIDPattern.MatchString(requestID) {
		return r.backupRequestStatus(ctx, ha, receipt, "InvalidRequest", "backup request ID is invalid")
	}
	agents, err := c.ListBackupAgents(ctx, token)
	if err != nil {
		return r.backupRequestStatus(ctx, ha, receipt, "ObservationUnavailable", "cannot verify backup destinations")
	}
	expected := config.CreateBackup.AgentIDs
	if len(expected) == 0 {
		expected = []string{"backup.local"}
	}
	available := map[string]bool{}
	for _, agent := range agents {
		available[agent.ID] = true
	}
	for _, id := range expected {
		if !available[id] {
			return r.backupRequestStatus(ctx, ha, receipt, "AgentUnavailable", "a requested backup destination is unavailable")
		}
	}
	info, err := c.GetBackupInfo(ctx, token)
	if err != nil {
		return r.backupRequestStatus(ctx, ha, receipt, "ObservationUnavailable", "cannot verify backup activity")
	}
	if info.State != "idle" {
		return r.backupRequestStatus(ctx, ha, receipt, "RequestInProgress", "Home Assistant is already processing a backup")
	}

	receipt = &backupReceipt{
		Version: 1, InstanceUID: string(ha.UID), RequestID: requestID, Phase: "Dispatching",
		ClaimedAt: time.Now().UTC(), ExpectedAgentIDs: expected,
	}
	if result, err := r.persistBackupReceipt(ctx, ha, receipt, 0); err != nil || result.RequeueAfter > 0 {
		return result, err
	}
	jobID, err := c.GenerateBackupWithAutomaticSettings(ctx, token)
	if err != nil {
		var haErr *haclient.Error
		if errors.As(err, &haErr) && haErr.Code != "" {
			receipt.Phase, receipt.Reason = "Failed", "NativeRejected"
			return r.persistBackupReceipt(ctx, ha, receipt, 30*time.Second)
		}
		receipt.Phase, receipt.Reason = "Unknown", "SubmissionUncertain"
		return r.persistBackupReceipt(ctx, ha, receipt, 30*time.Second)
	}
	receipt.Phase, receipt.BackupID, receipt.Reason = "Accepted", jobID, ""
	return r.persistBackupReceipt(ctx, ha, receipt, 10*time.Second)
}

func (r *HomeAssistantReconciler) persistBackupReceipt(
	ctx context.Context, ha *hav1.HomeAssistant, receipt *backupReceipt, requeue time.Duration,
) (ctrl.Result, error) {
	data, err := json.Marshal(receipt)
	if err != nil {
		return ctrl.Result{}, err
	}
	if ha.Annotations == nil {
		ha.Annotations = map[string]string{}
	}
	ha.Annotations[backupReceiptAnnotation] = string(data)
	if err := r.Update(ctx, ha); err != nil {
		return ctrl.Result{}, err
	}
	setBackupRequestStatus(ha, receipt)
	setBackupCondition(ha, "BackupRequestReady", metav1.ConditionTrue, "Observed", "backup request recorded")
	if err := r.Status().Update(ctx, ha); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

func (r *HomeAssistantReconciler) backupRequestStatus(
	ctx context.Context, ha *hav1.HomeAssistant, receipt *backupReceipt, reason, message string,
) (ctrl.Result, error) {
	setBackupRequestStatus(ha, receipt)
	status := metav1.ConditionFalse
	if reason == "Observed" {
		status = metav1.ConditionTrue
	}
	setBackupCondition(ha, "BackupRequestReady", status, reason, message)
	if err := r.Status().Update(ctx, ha); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
