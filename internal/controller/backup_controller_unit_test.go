package controller

import (
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

func TestBuildBackupConfigRequestUsesNativeScheduleSemantics(t *testing.T) {
	r := &HomeAssistantReconciler{}

	defaults := r.buildBackupConfigRequest(&hav1.BackupSpec{Enabled: true})
	if defaults.Schedule.Recurrence != "daily" || len(defaults.Schedule.Days) != 0 {
		t.Fatalf("default schedule = %#v", defaults.Schedule)
	}
	if got := defaults.CreateBackup.AgentIDs; len(got) != 1 || got[0] != "backup.local" {
		t.Fatalf("default agents = %#v", got)
	}

	weekday := r.buildBackupConfigRequest(
		&hav1.BackupSpec{Enabled: true, Recurrence: "mon", IncludeDatabase: ptr.To(true)},
	)
	if weekday.Schedule.Recurrence != "custom_days" || len(weekday.Schedule.Days) != 1 ||
		weekday.Schedule.Days[0] != "mon" {
		t.Fatalf("weekday schedule = %#v", weekday.Schedule)
	}

	disabled := r.buildBackupConfigRequest(&hav1.BackupSpec{Enabled: false})
	if disabled.Schedule.Recurrence != "never" || disabled.Retention != nil || disabled.CreateBackup != nil {
		t.Fatalf("disabled request = %#v", disabled)
	}
}

func TestNeedsBackupUpdateTreatsAgentIDsAndNativeDaysAsSets(t *testing.T) {
	r := &HomeAssistantReconciler{}
	includeDatabase := true
	current := &haclient.BackupConfig{
		Schedule: haclient.BackupSchedule{Recurrence: "custom_days", Days: []string{"mon", "wed"}},
		CreateBackup: haclient.BackupCreateConfig{
			IncludeDatabase: &includeDatabase,
			AgentIDs:        []string{"backup.local", "remote"},
		},
	}
	desired := &haclient.BackupConfigRequest{
		Schedule: &haclient.BackupSchedule{Recurrence: "custom_days", Days: []string{"wed", "mon"}},
		CreateBackup: &haclient.BackupCreateConfig{
			IncludeDatabase: &includeDatabase,
			AgentIDs:        []string{"remote", "backup.local"},
		},
	}
	if r.needsBackupUpdate(current, desired) {
		t.Fatal("set-equivalent backup configuration was treated as drift")
	}
}

func TestValidateBackupAgentsRejectsUnavailableDestination(t *testing.T) {
	if err := validateBackupAgents([]string{"backup.local"}, []haclient.BackupAgent{{ID: "backup.local"}}); err != nil {
		t.Fatalf("available destination rejected: %v", err)
	}
	if err := validateBackupAgents([]string{"missing"}, []haclient.BackupAgent{{ID: "backup.local"}}); err == nil {
		t.Fatal("unavailable destination was accepted")
	}
}

func TestBackupConfigurationAndHealthConditionsRemainSeparate(t *testing.T) {
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Generation: 7}}
	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type: conditionTypeBackupConfigured, Status: metav1.ConditionTrue,
		ObservedGeneration: ha.Generation, Reason: reasonBackupConfigured,
	})

	setBackupHealthUnknown(ha)

	configured := meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupConfigured)
	health := meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupHealthy)
	if configured == nil || configured.Status != metav1.ConditionTrue {
		t.Fatalf("configured condition = %#v", configured)
	}
	if health == nil || health.Status != metav1.ConditionUnknown || health.Reason != "BackupOutcomeUnknown" {
		t.Fatalf("health condition = %#v", health)
	}
	if configured.LastTransitionTime.IsZero() || health.LastTransitionTime.IsZero() {
		t.Fatalf("condition timestamps must be set: configured=%#v health=%#v", configured, health)
	}
}

func TestRequiredDestinationOutcomeReportsPartialFailure(t *testing.T) {
	phase, reason, _, succeeded := requiredDestinationOutcome(
		[]string{"backup.local", "remote"},
		[]haclient.BackupDestinationResult{
			{AgentID: "backup.local", Protected: true},
			{AgentID: "remote", Error: haclient.BackupErrorSourceUnavailable},
		},
	)
	if phase != hav1alpha1.BackupPhasePartiallySucceeded || reason != "RequiredDestinationFailed" || succeeded {
		t.Fatalf("partial destination result = (%s, %q, %t)", phase, reason, succeeded)
	}
}

func TestBackupConfigErrorMessageDoesNotExposeSecret(t *testing.T) {
	message := backupConfigErrorMessage("get", errors.New("token=synthetic-secret"))
	if message != "Unable to get native backup configuration (Unknown)" {
		t.Fatalf("message = %q", message)
	}
	if strings.Contains(message, "synthetic-secret") {
		t.Fatalf("secret was exposed in message: %q", message)
	}

	message = backupConfigErrorMessage("configure", &haclient.BackupError{
		Kind: haclient.BackupErrorAuthenticationFailed,
		Err:  errors.New("password=synthetic-secret"),
	})
	if message != "Unable to configure native backup configuration (AuthenticationFailed)" {
		t.Fatalf("classified message = %q", message)
	}
	if strings.Contains(message, "synthetic-secret") {
		t.Fatalf("secret was exposed in classified message: %q", message)
	}
}
