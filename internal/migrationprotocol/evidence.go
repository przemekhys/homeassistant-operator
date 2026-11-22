package migrationprotocol

import (
	"encoding/json"
	"fmt"
	"strings"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Evidence is a hint to read an external result, never permission to dispatch.
type Evidence struct {
	Version              string                       `json:"version"`
	OperationUID         string                       `json:"operationUID"`
	RequestID            string                       `json:"requestID"`
	Stage                string                       `json:"stage"`
	SourceID             string                       `json:"sourceID"`
	TargetUID            string                       `json:"targetUID"`
	JobID                string                       `json:"jobID,omitempty"`
	BackupID             string                       `json:"backupID,omitempty"`
	ArchiveDescriptorRef *ha.MigrationObjectReference `json:"archiveDescriptorRef,omitempty"`
	ArchiveDigest        string                       `json:"archiveDigest,omitempty"`
}

func ValidStage(s string) bool { return s == "backup" || s == "upload" || s == "restore" }

// ObservationResolution rejects ambiguous append-only hints rather than relying
// on the ordering of a Kubernetes set list.
func ObservationResolution(op *ha.HomeAssistantMigration, stage, requestID string) (*ha.MigrationResolution, error) {
	if op == nil {
		return nil, fmt.Errorf("migration unavailable")
	}
	var found *ha.MigrationResolution
	for i := range op.Spec.Resolutions {
		r := &op.Spec.Resolutions[i]
		if r.Stage != stage || r.RequestID != requestID || r.Decision != "ObserveExternalResult" {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("ambiguous observation resolutions")
		}
		found = r
	}
	return found, nil
}

func ValidateEvidenceRef(ref *ha.MigrationEvidenceReference) error {
	if ref == nil || ref.APIVersion != "v1" || ref.Kind != "ConfigMap" || len(validation.IsDNS1123Subdomain(ref.Name)) != 0 || strings.TrimSpace(ref.UID) == "" || len(validation.IsConfigMapKey(ref.Key)) != 0 {
		return fmt.Errorf("invalid migration evidence reference")
	}
	return nil
}

func ParseEvidence(raw []byte) (*Evidence, error) {
	var e Evidence
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || ambiguousEvidenceKeys(fields) || Decode(raw, &e) != nil || e.Version != "v1" || !ValidStage(e.Stage) || !ValidDigest(e.RequestID) || e.OperationUID == "" || e.SourceID == "" || e.TargetUID == "" || (e.JobID == "" && e.BackupID == "" && e.ArchiveDescriptorRef == nil) || (e.ArchiveDigest != "" && !ValidDigest(e.ArchiveDigest)) {
		return nil, fmt.Errorf("invalid migration evidence")
	}
	// HA identifiers are opaque API identifiers, not Kubernetes DNS names.
	for _, id := range []string{e.JobID, e.BackupID} {
		if len(id) > 256 || strings.ContainsAny(id, "\r\n\x00") || strings.TrimSpace(id) != id {
			return nil, fmt.Errorf("invalid external result identity")
		}
	}
	if ref := e.ArchiveDescriptorRef; ref != nil && (len(validation.IsDNS1123Subdomain(ref.Name)) != 0 || ref.UID == "") {
		return nil, fmt.Errorf("invalid archive reference")
	}
	return &e, nil
}

func ambiguousEvidenceKeys(fields map[string]json.RawMessage) bool {
	keys := make([]string, 0, len(fields))
	for key, value := range fields {
		for _, previous := range keys {
			if strings.EqualFold(key, previous) {
				return true
			}
		}
		keys = append(keys, key)
		var nested map[string]json.RawMessage
		if json.Unmarshal(value, &nested) == nil && nested != nil && ambiguousEvidenceKeys(nested) {
			return true
		}
	}
	return false
}
