// Package migration implements the read-only scan and create-only import
// boundary for Home Assistant migrations.
package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const ReportVersion = "v1alpha1"

type Target struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type ObjectClassification string

const (
	ClassificationImportable  ObjectClassification = "Importable"
	ClassificationIncomplete  ObjectClassification = "Incomplete"
	ClassificationUnsupported ObjectClassification = "Unsupported"
	ClassificationConflict    ObjectClassification = "Conflict"
)

// ReportObject is a redacted proposal. Proposal must never include values from
// a source secret or config flow.
type ReportObject struct {
	Kind           string               `json:"kind"`
	SourceID       string               `json:"sourceID"`
	Name           string               `json:"name"`
	Classification ObjectClassification `json:"classification"`
	Reason         string               `json:"reason,omitempty"`
	Proposal       json.RawMessage      `json:"proposal,omitempty"`
}

type Report struct {
	Version  string           `json:"version"`
	SourceID string           `json:"sourceID"`
	Target   Target           `json:"target"`
	Objects  []ReportObject   `json:"objects"`
	Backup   *BackupPreflight `json:"backup,omitempty"`
	ID       string           `json:"id"`
}

// BackupPreflight contains only the observable scope and key availability,
// never the recovery key or other credentials.
type BackupPreflight struct {
	Status           string          `json:"status"`
	Reason           string          `json:"reason,omitempty"`
	IncludeDatabase  *bool           `json:"includeDatabase,omitempty"`
	AgentIDs         []string        `json:"agentIDs,omitempty"`
	Protected        bool            `json:"protected"`
	Limitations      []string        `json:"limitations"`
	IncludeAddons    json.RawMessage `json:"includeAddons,omitempty"`
	IncludeAllAddons *bool           `json:"includeAllAddons,omitempty"`
	IncludeFolders   json.RawMessage `json:"includeFolders,omitempty"`
}

// Finalize validates the public report shape, applies deterministic ordering,
// and computes an identifier from all content except the identifier itself.
func (r *Report) Finalize() error {
	if r.Version == "" {
		r.Version = ReportVersion
	}
	if r.Version != ReportVersion || r.SourceID == "" || r.Target.Namespace == "" ||
		r.Target.Name == "" ||
		r.Target.UID == "" {
		return fmt.Errorf("migration report has incomplete source or target identity")
	}
	if strings.Contains(r.SourceID, "://") {
		u, err := url.Parse(r.SourceID)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("migration report has an invalid source identity")
		}
	}
	sort.Slice(r.Objects, func(i, j int) bool {
		if r.Objects[i].Kind == r.Objects[j].Kind {
			return r.Objects[i].SourceID < r.Objects[j].SourceID
		}
		return r.Objects[i].Kind < r.Objects[j].Kind
	})
	for _, object := range r.Objects {
		if object.Kind == "" || object.SourceID == "" || object.Name == "" ||
			object.Classification == "" {
			return fmt.Errorf("migration report contains incomplete object")
		}
		if containsSensitiveKey(object.Proposal) {
			return fmt.Errorf("migration report proposal contains sensitive data")
		}
	}
	r.ID = ""
	encoded, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode migration report: %w", err)
	}
	digest := sha256.Sum256(encoded)
	r.ID = "sha256:" + hex.EncodeToString(digest[:])
	return nil
}

func containsSensitiveKey(value json.RawMessage) bool {
	if len(value) == 0 {
		return false
	}
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return true
	}
	return containsSensitiveValue(decoded)
}

func containsSensitiveValue(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, nested := range value {
			switch strings.ToLower(key) {
			case "password", "token", "secret", "api_key":
				return true
			}
			if containsSensitiveValue(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range value {
			if containsSensitiveValue(nested) {
				return true
			}
		}
	}
	return false
}
