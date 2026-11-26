package controller

import (
	"encoding/json"
	"strings"
)

const migrationOverwriteApprovalAnnotation = "ha.homeassistant.io/migration-overwrite-approval"

type migrationOverwriteAnnotation struct {
	Version          string `json:"version"`
	Approved         bool   `json:"approved"`
	ObjectUID        string `json:"objectUID"`
	Generation       int64  `json:"generation"`
	TargetUID        string `json:"targetUID"`
	SourceID         string `json:"sourceID"`
	ComparisonDigest string `json:"comparisonDigest"`
}

// validMigrationOverwriteAnnotation only validates intent; it never authorizes
// a write without a separately verified atomic mutation mechanism.
func validMigrationOverwriteAnnotation(
	raw, uid string,
	generation int64,
	identity migrationIdentity,
	digest string,
) bool {
	if validateMigrationJSON([]byte(raw)) != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || len(fields) != 7 {
		return false
	}
	for _, key := range []string{"version", "approved", "objectUID", "generation", "targetUID", "sourceID", "comparisonDigest"} {
		if _, exists := fields[key]; !exists {
			return false
		}
	}
	var approval migrationOverwriteAnnotation
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&approval) != nil {
		return false
	}
	return approval.Version == "v1" && approval.Approved && uid != "" && generation > 0 &&
		identity.TargetUID != "" && identity.SourceID != "" && digest != "" &&
		approval.ObjectUID == uid && approval.Generation == generation &&
		approval.TargetUID == identity.TargetUID && approval.SourceID == identity.SourceID && approval.ComparisonDigest == digest
}

// migrationOverwriteApproval is durable user intent for one exact comparison.
// It is invalidated by a UID, generation, or digest change before any write.
type migrationOverwriteApproval struct {
	ObjectUID        string
	Generation       int64
	ComparisonDigest string
	Approved         bool
}

func migrationOverwriteApproved(
	approval *migrationOverwriteApproval,
	objectUID string,
	generation int64,
	comparisonDigest string,
) bool {
	return approval != nil && approval.Approved && strings.TrimSpace(objectUID) != "" &&
		approval.ObjectUID == objectUID &&
		approval.Generation == generation &&
		approval.ComparisonDigest == comparisonDigest &&
		strings.TrimSpace(comparisonDigest) != ""
}

// migrationOverwriteSafe is deliberately false: HA APIs used by the current
// adapters do not offer an atomic compare-and-swap. A matching approval alone
// is never permission to race an overwrite.
func migrationOverwriteSafe(_ *migrationOverwriteApproval, _ string, _ int64, _ string) bool {
	return false
}
