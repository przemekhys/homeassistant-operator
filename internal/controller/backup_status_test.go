package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestBackupStatusProjectsScheduledArchiveWithoutRequest(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{Backups: []haclient.BackupArchive{{
		ID: "scheduled", Date: "2026-11-03T12:00:00Z", Automatic: true,
		Agents: map[string]haclient.BackupAgentCopy{"backup.local": {}},
	}}}, backupConfigForAgents("backup.local"))
	if ha.Status.Backup.Request != nil {
		t.Fatal("scheduled backup created a one-shot request status")
	}
	attempt := ha.Status.Backup.LastAttempt
	if attempt == nil || attempt.Source != "HomeAssistant" || attempt.Outcome != "Succeeded" || attempt.BackupID != "scheduled" {
		t.Fatalf("scheduled archive status = %#v", attempt)
	}
}

func TestEarliestRequeuePreservesBackupRequestDeadline(t *testing.T) {
	result := earliestRequeue(
		ctrl.Result{RequeueAfter: 120 * time.Second},
		ctrl.Result{RequeueAfter: 10 * time.Second},
		ctrl.Result{RequeueAfter: 30 * time.Second},
	)
	if result.RequeueAfter != 10*time.Second {
		t.Fatalf("requeue after = %s, want 10s", result.RequeueAfter)
	}
}

func TestHomeAssistantPrimaryPredicateKeepsBackupRequestAnnotation(t *testing.T) {
	oldHA := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"other": "value"}}}
	newHA := oldHA.DeepCopy()
	newHA.Annotations[backupRequestAnnotation] = "request-1"
	if !homeAssistantPrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldHA, ObjectNew: newHA}) {
		t.Fatal("backup request annotation update was filtered")
	}
	statusOnly := oldHA.DeepCopy()
	statusOnly.Status.Backup = &hav1.BackupStatus{}
	if homeAssistantPrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldHA, ObjectNew: statusOnly}) {
		t.Fatal("status-only update was not filtered")
	}
}

func TestBackupStatusFailureDoesNotInventArchiveEvidence(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{AgentErrors: map[string]any{
		"backup.local": "native create failed",
	}}, backupConfigForAgents("backup.local"))
	attempt := ha.Status.Backup.LastAttempt
	if attempt == nil || attempt.Outcome != "Failed" || attempt.BackupID != "" || attempt.CompletedAt != nil {
		t.Fatalf("failure status invented archive data: %#v", attempt)
	}
	if ha.Status.Backup.LastSuccessfulBackupTime != nil {
		t.Fatal("failure without archive invented a successful backup time")
	}
}

func TestBackupStatusKeepsNewerConfirmedSuccessWhenHistoryIsOld(t *testing.T) {
	prior := metav1.NewTime(mustTime(t, "2026-11-03T12:00:00Z"))
	ha := &hav1.HomeAssistant{Status: hav1.HomeAssistantStatus{Backup: &hav1.BackupStatus{LastSuccessfulBackupTime: &prior}}}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{Backups: []haclient.BackupArchive{{
		ID: "old", Date: "2026-11-03T11:00:00Z", Agents: map[string]haclient.BackupAgentCopy{"backup.local": {}},
	}}}, backupConfigForAgents("backup.local"))
	if got := ha.Status.Backup.LastSuccessfulBackupTime.Time; !got.Equal(prior.Time) {
		t.Fatalf("old backup history regressed success time to %s", got)
	}
}

func TestBackupStatusUnknownReceiptCanLaterResolveWithoutRedispatch(t *testing.T) {
	claimedAt := time.Now().Add(-16 * time.Minute)
	ha := &hav1.HomeAssistant{}
	setBackupRequestStatus(ha, &backupReceipt{
		RequestID: "request-1", BackupID: "archive-1", Phase: "Unknown", ClaimedAt: claimedAt,
		ExpectedAgentIDs: []string{"backup.local"},
	})
	if ha.Status.Backup.LastAttempt.Outcome != "Unknown" {
		t.Fatalf("expired receipt outcome = %q, want Unknown", ha.Status.Backup.LastAttempt.Outcome)
	}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{Backups: []haclient.BackupArchive{{
		ID: "archive-1", Date: "2026-11-03T12:00:00Z", Agents: map[string]haclient.BackupAgentCopy{"backup.local": {}},
	}}}, backupConfigForAgents("backup.local"))
	if ha.Status.Backup.LastAttempt.Outcome != "Succeeded" {
		t.Fatalf("later archive evidence did not update observation: %#v", ha.Status.Backup.LastAttempt)
	}
}
