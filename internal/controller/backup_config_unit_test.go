package controller

import (
	"testing"

	"k8s.io/utils/ptr"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

func TestBuildBackupConfigRequestPreservesPublicDefaults(t *testing.T) {
	r := &HomeAssistantReconciler{}
	req := r.buildBackupConfigRequest(&hav1.BackupSpec{})
	if req.Schedule.Recurrence != "daily" || req.Schedule.Time != nil {
		t.Fatalf("unexpected schedule defaults: %#v", req.Schedule)
	}
	if req.Retention.Copies != nil || req.Retention.Days != nil {
		t.Fatalf("unexpected retention defaults: %#v", req.Retention)
	}
	if req.CreateBackup.IncludeDatabase == nil || !*req.CreateBackup.IncludeDatabase {
		t.Fatalf("includeDatabase default was not preserved: %#v", req.CreateBackup)
	}
	if !stringSliceSetEqual(req.CreateBackup.AgentIDs, []string{"backup.local"}) {
		t.Fatalf("unexpected default agents: %v", req.CreateBackup.AgentIDs)
	}
}

func TestBuildBackupConfigRequestMapsWeekdayAndNever(t *testing.T) {
	r := &HomeAssistantReconciler{}
	for _, day := range []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"} {
		weekday := r.buildBackupConfigRequest(&hav1.BackupSpec{Enabled: true, Recurrence: day})
		if weekday.Schedule.Recurrence != "custom_days" || !stringSliceSetEqual(weekday.Schedule.Days, []string{day}) {
			t.Fatalf("weekday %q was not mapped: %#v", day, weekday.Schedule)
		}
	}
	never := r.buildBackupConfigRequest(&hav1.BackupSpec{
		Enabled: true, Recurrence: "never", IncludeDatabase: ptr.To(false),
	})
	if never.Schedule.Recurrence != "never" || never.CreateBackup.IncludeDatabase == nil ||
		*never.CreateBackup.IncludeDatabase {
		t.Fatalf("never schedule not retained: %#v", never)
	}
}

func TestNeedsBackupUpdateIgnoresAgentOrder(t *testing.T) {
	r := &HomeAssistantReconciler{}
	desired := r.buildBackupConfigRequest(&hav1.BackupSpec{Enabled: true, AgentIDs: []string{"agent-b", "agent-a"}})
	current := &haclient.BackupConfig{
		Schedule:     haclient.BackupSchedule{Recurrence: "daily"},
		Retention:    haclient.BackupRetention{},
		CreateBackup: haclient.BackupCreateConfig{IncludeDatabase: ptr.To(true), AgentIDs: []string{"agent-a", "agent-b"}},
	}
	if r.needsBackupUpdate(current, desired) {
		t.Fatal("agent ordering alone triggered a config update")
	}
}

func TestMissingBackupAgentDoesNotSelectFallback(t *testing.T) {
	agents := []haclient.BackupAgent{{ID: "backup.local"}, {ID: "custom.agent"}}
	if missingBackupAgent([]string{"custom.agent"}, agents) {
		t.Fatal("available custom agent was rejected")
	}
	if !missingBackupAgent([]string{"missing.agent"}, agents) {
		t.Fatal("missing agent was silently accepted")
	}
}

func TestBackupConfigReadFailureUsesSafeCapabilityReasons(t *testing.T) {
	for name, err := range map[string]error{
		"unsupported": &haclient.Error{Code: "unknown_command"},
		"permission":  &haclient.Error{Code: "forbidden"},
		"other":       &haclient.Error{Message: "sensitive server response"},
	} {
		t.Run(name, func(t *testing.T) {
			reason, message := backupConfigReadFailure(err)
			if message == "" || message == "sensitive server response" {
				t.Fatalf("unsafe or empty message: %q", message)
			}
			if name == "unsupported" && reason != "Unsupported" {
				t.Fatalf("reason = %q, want Unsupported", reason)
			}
			if name == "permission" && reason != "PermissionDenied" {
				t.Fatalf("reason = %q, want PermissionDenied", reason)
			}
		})
	}
}
