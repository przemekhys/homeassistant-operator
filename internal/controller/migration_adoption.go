package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type migrationAdoptionState string

const (
	migrationAdoptionPaused   migrationAdoptionState = "Paused"
	migrationAdoptionChecking migrationAdoptionState = "Checking"
	migrationAdoptionConflict migrationAdoptionState = "Conflict"
	migrationAdoptionBlocked  migrationAdoptionState = "Blocked"
	migrationAdoptionAdopted  migrationAdoptionState = "Adopted"
)

// migrationAdoptionResult is a redacted, reproducible comparison result. The
// digest identifies the compared canonical values without exposing them.
type migrationAdoptionResult struct {
	State            migrationAdoptionState
	ComparisonDigest string
	Reason           string
}

func evaluateMigrationAdoption(
	imported, paused bool,
	sourceID, targetID string,
	desired, actual json.RawMessage,
) migrationAdoptionResult {
	if !imported {
		return migrationAdoptionResult{State: migrationAdoptionAdopted, Reason: "NotImported"}
	}
	if paused {
		return migrationAdoptionResult{
			State:  migrationAdoptionPaused,
			Reason: "MigrationPaused",
		}
	}
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(targetID) == "" ||
		len(desired) == 0 ||
		len(actual) == 0 {
		return migrationAdoptionResult{
			State:  migrationAdoptionBlocked,
			Reason: "IncompleteIdentityOrData",
		}
	}
	if sourceID != targetID {
		return migrationAdoptionResult{
			State:  migrationAdoptionBlocked,
			Reason: "IdentityMismatch",
		}
	}
	desiredCanonical, err := canonicalMigrationJSON(desired)
	if err != nil {
		return migrationAdoptionResult{
			State:  migrationAdoptionBlocked,
			Reason: "InvalidDesiredData",
		}
	}
	actualCanonical, err := canonicalMigrationJSON(actual)
	if err != nil {
		return migrationAdoptionResult{
			State:  migrationAdoptionBlocked,
			Reason: "InvalidObservedData",
		}
	}
	digest := comparisonDigest(sourceID, targetID, desiredCanonical, actualCanonical)
	if string(desiredCanonical) != string(actualCanonical) {
		return migrationAdoptionResult{
			State:            migrationAdoptionConflict,
			ComparisonDigest: digest,
			Reason:           "CanonicalFieldsDiffer",
		}
	}
	return migrationAdoptionResult{
		State:            migrationAdoptionAdopted,
		ComparisonDigest: digest,
		Reason:           "CanonicalFieldsMatch",
	}
}

func canonicalMigrationJSON(raw json.RawMessage) ([]byte, error) {
	if err := validateMigrationJSON(raw); err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// validateMigrationJSON rejects duplicate keys at every level: encoding/json's
// default last-key-wins behaviour can otherwise hide conflicting HA fields.
func validateMigrationJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateMigrationJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON data")
	}
	return nil
}

func validateMigrationJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("invalid JSON object key")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate JSON object key")
			}
			seen[name] = struct{}{}
			if err := validateMigrationJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := validateMigrationJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid JSON array")
		}
	default:
		return fmt.Errorf("invalid JSON value")
	}
	return nil
}

func comparisonDigest(sourceID, targetID string, desired, actual []byte) string {
	sum := sha256.Sum256(
		append(
			append(append([]byte(sourceID+"\x00"+targetID+"\x00"), desired...), '\x00'),
			actual...),
	)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func redactedAdoptionDiff(result migrationAdoptionResult) string {
	return fmt.Sprintf(
		"state=%s reason=%s comparison=%s",
		result.State,
		result.Reason,
		result.ComparisonDigest,
	)
}
