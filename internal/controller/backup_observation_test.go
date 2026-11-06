package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

func TestProjectBackupInfoUsesOnlySuccessfulArchiveDate(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	r := &HomeAssistantReconciler{}
	r.projectBackupInfo(ha, &haclient.BackupInfo{Backups: []haclient.BackupArchive{
		{ID: "failed", Date: "2026-11-03T12:00:00Z", FailedAgentIDs: []string{"backup.remote"}},
		{ID: "complete", Date: "2026-11-03T11:00:00Z", Agents: map[string]haclient.BackupAgentCopy{"backup.local": {}}},
	}}, backupConfigForAgents("backup.local"))
	if ha.Status.Backup == nil || ha.Status.Backup.LastSuccessfulBackupTime == nil {
		t.Fatal("successful archive time was not projected")
	}
	if got := ha.Status.Backup.LastSuccessfulBackupTime.Time; !got.Equal(mustTime(t, "2026-11-03T11:00:00Z")) {
		t.Fatalf("last successful backup time = %s", got)
	}
}

func TestProjectBackupInfoDoesNotInventSuccess(t *testing.T) {
	prior := metav1.NewTime(mustTime(t, "2026-11-03T10:00:00Z"))
	ha := &hav1.HomeAssistant{Status: hav1.HomeAssistantStatus{
		Backup: &hav1.BackupStatus{LastSuccessfulBackupTime: &prior},
	}}
	r := &HomeAssistantReconciler{}
	r.projectBackupInfo(ha, &haclient.BackupInfo{Backups: []haclient.BackupArchive{
		{ID: "failed", Date: "2026-11-03T12:00:00Z", FailedAgentIDs: []string{"backup.local"}},
	}}, backupConfigForAgents("backup.local"))
	if got := ha.Status.Backup.LastSuccessfulBackupTime.Time; !got.Equal(prior.Time) {
		t.Fatalf("failed archive replaced prior successful time: %s", got)
	}
}

func TestProjectBackupInfoRequiresDestinationEvidence(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	r := &HomeAssistantReconciler{}
	r.projectBackupInfo(ha, &haclient.BackupInfo{Backups: []haclient.BackupArchive{{
		ID: "uploading", Date: "2026-11-03T12:00:00Z",
	}}}, backupConfigForAgents("backup.local"))
	if ha.Status.Backup.LastSuccessfulBackupTime != nil {
		t.Fatal("archive without destination metadata was reported as a success")
	}
}

func TestProjectBackupInfoProjectsActiveWorkAsRunning(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	(&HomeAssistantReconciler{}).projectBackupInfo(
		ha, &haclient.BackupInfo{State: "creating"}, backupConfigForAgents("backup.local"),
	)
	if ha.Status.Backup.LastAttempt == nil || ha.Status.Backup.LastAttempt.Outcome != "Running" {
		t.Fatalf("active backup was not projected as running: %#v", ha.Status.Backup.LastAttempt)
	}
	if ha.Status.Backup.LastSuccessfulBackupTime != nil {
		t.Fatal("active backup was projected as a success")
	}
}

func TestProjectBackupInfoDoesNotUseAutomaticCompletionWithoutDestinations(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{
		LastCompletedAutomaticBackup: stringPointer("2026-11-03T12:00:00Z"),
	}, backupConfigForAgents("backup.local"))
	if ha.Status.Backup.LastSuccessfulBackupTime != nil || ha.Status.Backup.LastAttempt != nil {
		t.Fatal("automatic completion metadata was treated as archive success evidence")
	}
}

func TestProjectBackupInfoRequiresConfiguredDestinationHistory(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{Backups: []haclient.BackupArchive{{
		ID: "archive", Date: "2026-11-03T12:00:00Z", Agents: map[string]haclient.BackupAgentCopy{"backup.local": {}},
	}}}, nil)
	if ha.Status.Backup.LastAttempt == nil || ha.Status.Backup.LastAttempt.Outcome != "Unknown" {
		t.Fatalf("archive without configured destination history = %#v, want Unknown", ha.Status.Backup.LastAttempt)
	}
	if ha.Status.Backup.LastSuccessfulBackupTime != nil {
		t.Fatal("archive without configured destination history was reported as success")
	}
}

func TestProjectBackupInfoProjectsAgentErrorsWithoutPayload(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{AgentErrors: map[string]any{
		"backup.remote": map[string]string{"unsafe": "do not expose"},
	}}, backupConfigForAgents("backup.local", "backup.remote"))
	attempt := ha.Status.Backup.LastAttempt
	if attempt == nil || attempt.Outcome != "Failed" || len(attempt.Locations) != 1 ||
		attempt.Locations[0].ID != "backup.remote" {
		t.Fatalf("agent errors were not safely projected: %#v", attempt)
	}
}

func TestSetBackupRequestStatusDoesNotTreatAcceptanceAsSuccess(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	setBackupRequestStatus(ha, &backupReceipt{RequestID: "request-1", Phase: "Accepted", ClaimedAt: time.Now()})
	if ha.Status.Backup.LastAttempt == nil || ha.Status.Backup.LastAttempt.Outcome != "Running" {
		t.Fatalf("accepted receipt status = %#v, want Running", ha.Status.Backup.LastAttempt)
	}
	if ha.Status.Backup.LastSuccessfulBackupTime != nil {
		t.Fatal("accepted receipt was reported as a success")
	}
}

func backupConfigForAgents(agentIDs ...string) *haclient.BackupConfig {
	return &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{AgentIDs: agentIDs}}
}

func stringPointer(value string) *string { return &value }

func TestBackupArchiveCoversEveryExpectedAgent(t *testing.T) {
	archive := haclient.BackupArchive{Agents: map[string]haclient.BackupAgentCopy{
		"backup.local": {}, "custom.agent": {},
	}}
	if !backupArchiveCoversAgents(archive, []string{"backup.local", "custom.agent"}) {
		t.Fatal("complete destination evidence was rejected")
	}
	if backupArchiveCoversAgents(archive, []string{"backup.local", "missing.agent"}) {
		t.Fatal("missing destination was accepted as a full success")
	}
}

func mustTime(t *testing.T, raw string) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
