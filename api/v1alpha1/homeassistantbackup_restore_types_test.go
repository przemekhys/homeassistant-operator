package v1alpha1

import (
	"encoding/json"
	"testing"
)

func TestBackupOperationPreservesOmittedAndEmptyAgents(t *testing.T) {
	omitted, err := json.Marshal(HomeAssistantBackupSpec{HomeAssistantRef: HomeAssistantTargetReference{Name: "ha", UID: "uid"}})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := json.Marshal(HomeAssistantBackupSpec{HomeAssistantRef: HomeAssistantTargetReference{Name: "ha", UID: "uid"}, AgentIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	var omittedFields, emptyFields map[string]json.RawMessage
	_ = json.Unmarshal(omitted, &omittedFields)
	_ = json.Unmarshal(empty, &emptyFields)
	if string(omittedFields["agentIDs"]) != "null" || string(emptyFields["agentIDs"]) != "[]" {
		t.Fatalf("agent intent was collapsed: omitted=%s empty=%s", omitted, empty)
	}
}

func TestRestoreSourceAndScopeRemainExplicit(t *testing.T) {
	restore := HomeAssistantRestoreSpec{HomeAssistantRef: HomeAssistantTargetReference{Name: "ha", UID: "uid"}, BackupID: "backup", Source: RestoreSource{AgentID: "core"}, EncryptionKeySecretRef: SecretKeyReference{Name: "key", Key: "value"}, PostRestoreAccessSecretRef: SecretKeyReference{Name: "access", Key: "token"}, Scope: RestoreScope{HomeAssistant: true}, SafetyBackupPolicy: "Skip", ResumePolicy: "KeepPaused"}
	raw, err := json.Marshal(restore)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || !restore.Scope.HomeAssistant || restore.Source.AgentID == "" {
		t.Fatal("restore scope/source was not retained")
	}
}

func TestRestoreSourceXORShapes(t *testing.T) {
	cases := []struct {
		name   string
		source RestoreSource
		valid  bool
	}{
		{name: "agent", source: RestoreSource{AgentID: "core"}, valid: true},
		{name: "uploaded", source: RestoreSource{Uploaded: &UploadedRestoreSource{}}, valid: true},
		{name: "both", source: RestoreSource{AgentID: "core", Uploaded: &UploadedRestoreSource{}}, valid: false},
		{name: "neither", source: RestoreSource{}, valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			valid := (tc.source.AgentID != "") != (tc.source.Uploaded != nil)
			if valid != tc.valid {
				t.Fatalf("source validity = %v, want %v", valid, tc.valid)
			}
		})
	}
}
