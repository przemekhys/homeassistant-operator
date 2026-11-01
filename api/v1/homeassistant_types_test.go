package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBackupSpecPreservesExplicitDisableAndAgentIntent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		spec       BackupSpec
		wantAgents string
	}{
		{name: "managed defaults", spec: BackupSpec{ManagementPolicy: "Managed"}, wantAgents: "null"},
		{name: "unmanaged explicit disable", spec: BackupSpec{ManagementPolicy: "Unmanaged", Enabled: false, AgentIDs: []string{}}, wantAgents: "[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if got := string(fields["enabled"]); got != "false" {
				t.Fatalf("enabled = %s, want explicit false", got)
			}
			if got := string(fields["agentIDs"]); got != tc.wantAgents {
				t.Fatalf("agentIDs = %s, want %s", got, tc.wantAgents)
			}
		})
	}
}

func TestRestoreOnlyIsAnExplicitInitializationPolicy(t *testing.T) {
	ha := HomeAssistant{Spec: HomeAssistantSpec{Alpha: &AlphaSpec{CoordinatedSynchronization: &CoordinatedSynchronizationSpec{Enabled: true, InitializationPolicy: "RestoreOnly"}}}}
	raw, err := json.Marshal(ha)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"initializationPolicy":"RestoreOnly"`) {
		t.Fatalf("RestoreOnly was omitted: %s", raw)
	}
}
