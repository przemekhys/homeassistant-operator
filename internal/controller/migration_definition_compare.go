package controller

import (
	"encoding/json"
	"fmt"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

// compareMigrationDefinition compares the complete API representation, not a
// subset of fields. The result is informational: no existing write/finalizer
// path may run until a kind-specific adoption protocol is qualified.
func compareMigrationDefinition(
	object any,
	sourceID string,
	observed json.RawMessage,
) migrationAdoptionResult {
	var desired map[string]interface{}
	var id string
	var err error
	switch v := object.(type) {
	case *hav1.HomeAssistantAutomation:
		id = v.Spec.ID
		desired, err = (&HomeAssistantAutomationReconciler{}).automationToYaml(v)
		delete(desired, "enabled") // HA persists enabled separately from config.
		if err == nil {
			observed, err = normalizeMigrationAutomationFields(observed)
			if err != nil {
				return migrationAdoptionResult{State: migrationAdoptionBlocked, Reason: "AmbiguousAutomationDefinition"}
			}
		}
	case *hav1.HomeAssistantScene:
		id = v.Spec.ID
		desired, err = (&HomeAssistantSceneReconciler{}).sceneToYaml(v)
	case *hav1.HomeAssistantScript:
		id = v.Spec.ID
		desired, err = (&HomeAssistantScriptReconciler{}).scriptToYaml(v)
	default:
		return migrationAdoptionResult{State: migrationAdoptionBlocked, Reason: "UnsupportedKind"}
	}
	if err != nil || id == "" || id != sourceID {
		return migrationAdoptionResult{
			State:  migrationAdoptionBlocked,
			Reason: "IncompleteDefinitionOrIdentity",
		}
	}
	encoded, err := json.Marshal(desired)
	if err != nil {
		return migrationAdoptionResult{
			State:  migrationAdoptionBlocked,
			Reason: "InvalidDesiredData",
		}
	}
	return evaluateMigrationAdoption(true, false, sourceID, id, encoded, observed)
}

// normalizeMigrationAutomationFields maps HA's returned plural keys onto the
// keys used by the operator's write payload. Never guess when both spellings
// are present. Values and unrelated keys are preserved verbatim for comparison.
func normalizeMigrationAutomationFields(raw json.RawMessage) (json.RawMessage, error) {
	if err := validateMigrationJSON(raw); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("invalid automation definition")
	}
	for _, pair := range [][2]string{{"trigger", "triggers"}, {"action", "actions"}} {
		plural, exists := fields[pair[1]]
		if !exists {
			continue
		}
		if _, duplicate := fields[pair[0]]; duplicate {
			return nil, fmt.Errorf("ambiguous automation field")
		}
		fields[pair[0]] = plural
		delete(fields, pair[1])
	}
	return json.Marshal(fields)
}

// migrationDefinitionDiff never includes the original fields or values.
func migrationDefinitionDiff(result migrationAdoptionResult) string {
	return fmt.Sprintf("%s: %s", result.State, result.Reason)
}
