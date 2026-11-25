package controller

import (
	"encoding/json"
	"strings"
	"testing"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestSceneComparisonPreservesLargeNumbers(t *testing.T) {
	scene := &hav1.HomeAssistantScene{Spec: hav1.HomeAssistantSceneSpec{ID: "scene", Entities: []hav1.SceneEntity{{EntityID: "light.test", State: "on", Attributes: runtime.RawExtension{Raw: []byte(`{"value":9007199254740993}`)}}}}}
	for _, tc := range []struct {
		raw  string
		want migrationAdoptionState
	}{
		{`{"id":"scene","entities":{"light.test":{"state":"on","value":9007199254740993}}}`, migrationAdoptionAdopted},
		{`{"id":"scene","entities":{"light.test":{"state":"on","value":9007199254740992}}}`, migrationAdoptionConflict},
	} {
		if got := compareMigrationDefinition(scene, "scene", json.RawMessage(tc.raw)); got.State != tc.want {
			t.Fatalf("state=%s, want %s", got.State, tc.want)
		}
	}
}

func TestScriptComparisonPreservesLargeNumbers(t *testing.T) {
	script := &hav1.HomeAssistantScript{Spec: hav1.HomeAssistantScriptSpec{
		ID: "script", Alias: "Example", Sequence: []hav1.ScriptAction{{RawExtension: runtime.RawExtension{Raw: []byte(`{"service":"test.run","data":{"value":9007199254740993}}`)}}},
		Fields: map[string]hav1.ScriptField{"value": {RawExtension: runtime.RawExtension{Raw: []byte(`{"default":9007199254740993}`)}}},
	}}
	for _, tc := range []struct {
		raw  string
		want migrationAdoptionState
	}{
		{`{"alias":"Example","sequence":[{"service":"test.run","data":{"value":9007199254740993}}],"fields":{"value":{"default":9007199254740993}}}`, migrationAdoptionAdopted},
		{`{"alias":"Example","sequence":[{"service":"test.run","data":{"value":9007199254740992}}],"fields":{"value":{"default":9007199254740993}}}`, migrationAdoptionConflict},
	} {
		if got := compareMigrationDefinition(script, "script", json.RawMessage(tc.raw)); got.State != tc.want {
			t.Fatalf("state=%s, want %s", got.State, tc.want)
		}
	}
}

func TestMigrationSceneDefinitionRequiresExactIdentityAndCompleteMatch(t *testing.T) {
	scene := &hav1.HomeAssistantScene{
		Spec: hav1.HomeAssistantSceneSpec{
			ID:       "evening",
			Name:     "Evening",
			Entities: []hav1.SceneEntity{{EntityID: "light.room", State: "on"}},
		},
	}
	for _, tc := range []struct {
		name, id, observed string
		want               migrationAdoptionState
	}{
		{"matching", "evening", `{"id":"evening","name":"Evening","entities":{"light.room":{"state":"on"}}}`, migrationAdoptionAdopted},
		{"changed", "evening", `{"id":"evening","name":"Evening","entities":{"light.room":{"state":"off"}}}`, migrationAdoptionConflict},
		{"unknown field", "evening", `{"id":"evening","name":"Evening","entities":{"light.room":{"state":"on"}},"unexpected":"secret"}`, migrationAdoptionConflict},
		{"wrong identity", "other", `{"id":"evening","name":"Evening","entities":{"light.room":{"state":"on"}}}`, migrationAdoptionBlocked},
		{"missing data", "evening", ``, migrationAdoptionBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := compareMigrationDefinition(scene, tc.id, json.RawMessage(tc.observed))
			if got.State != tc.want {
				t.Fatalf("state = %s, want %s", got.State, tc.want)
			}
			if strings.Contains(migrationDefinitionDiff(got), "secret") {
				t.Fatal("diff leaked field value")
			}
		})
	}
}

func TestNormalizeMigrationAutomationFields(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		invalid           bool
	}{
		{"plural HA response", `{"triggers":[1],"actions":[2],"conditions":[3],"other":"keep"}`, `{"action":[2],"condition":[3],"other":"keep","trigger":[1]}`, false},
		{"singular unchanged", `{"trigger":[1],"action":[2]}`, `{"action":[2],"trigger":[1]}`, false},
		{"ambiguous trigger", `{"trigger":[],"triggers":[]}`, "", true},
		{"ambiguous action", `{"action":[],"actions":[]}`, "", true},
		{"ambiguous condition", `{"condition":[],"conditions":[]}`, "", true},
		{"duplicate key", `{"triggers":[],"triggers":[1]}`, "", true},
		{"invalid response", `null`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeMigrationAutomationFields(json.RawMessage(tc.input))
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid input accepted")
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Fatalf("normalized = %s, %v", got, err)
			}
		})
	}
}

func TestNormalizeMigrationScriptMode(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		invalid           bool
	}{
		{"implicit default", `{"alias":"example"}`, `{"alias":"example","mode":"single"}`, false},
		{"explicit default", `{"mode":"single"}`, `{"mode":"single"}`, false},
		{"explicit different mode", `{"mode":"queued"}`, `{"mode":"queued"}`, false},
		{"duplicate mode", `{"mode":"single","mode":"queued"}`, "", true},
		{"invalid", `null`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeMigrationScriptMode(json.RawMessage(tc.input))
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid input accepted")
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Fatalf("normalized = %s, %v", got, err)
			}
		})
	}
}

func TestCompareMigrationScriptDefaultMode(t *testing.T) {
	script := &hav1.HomeAssistantScript{Spec: hav1.HomeAssistantScriptSpec{ID: "script-id", Alias: "Example", Mode: hav1.ScriptModeSingle}}
	for _, tc := range []struct {
		name, observed string
		want           migrationAdoptionState
	}{
		{"implicit default", `{"alias":"Example","sequence":null}`, migrationAdoptionAdopted},
		{"different mode", `{"alias":"Example","sequence":null,"mode":"queued"}`, migrationAdoptionConflict},
		{"invalid definition", `null`, migrationAdoptionBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareMigrationDefinition(script, "script-id", json.RawMessage(tc.observed)); got.State != tc.want {
				t.Fatalf("state = %s, want %s", got.State, tc.want)
			}
		})
	}
}
