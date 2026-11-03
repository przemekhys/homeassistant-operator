package haclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var backupImageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ValidBackupImageDigest(digest string) bool {
	return backupImageDigestPattern.MatchString(digest)
}

// BackupCapability is promoted independently. Qualifying native backup never
// implicitly qualifies either restore path.
type BackupCapability string

const (
	CapabilityBackup            BackupCapability = "backup"
	CapabilityRestoreExisting   BackupCapability = "restore-existing"
	CapabilityRestoreOnboarding BackupCapability = "restore-onboarding"
)

// QualificationRecord is a reviewed production record for one immutable image.
// The zero-value catalog supports nothing.
type QualificationRecord struct {
	ImageDigest  string                    `json:"imageDigest"`
	Capabilities map[BackupCapability]bool `json:"capabilities"`
	// JobIDIsArchiveID is an independently reviewed Container wire contract.
	// It must not be inferred from a generic manager event without an ID.
	JobIDIsArchiveID bool `json:"jobIDIsArchiveID,omitempty"`
}

type CapabilityCatalog struct {
	records map[string]QualificationRecord
}

func NewCapabilityCatalog(records []QualificationRecord) (*CapabilityCatalog, error) {
	catalog := &CapabilityCatalog{records: make(map[string]QualificationRecord, len(records))}
	for _, record := range records {
		if !ValidBackupImageDigest(record.ImageDigest) {
			return nil, fmt.Errorf("qualification record requires a valid immutable image digest")
		}
		if _, exists := catalog.records[record.ImageDigest]; exists {
			return nil, fmt.Errorf("duplicate qualification record for image digest")
		}
		capabilities := make(map[BackupCapability]bool, len(record.Capabilities))
		for capability, supported := range record.Capabilities {
			switch capability {
			case CapabilityBackup, CapabilityRestoreExisting, CapabilityRestoreOnboarding:
				capabilities[capability] = supported
			default:
				return nil, fmt.Errorf("qualification record contains an unknown capability")
			}
		}
		record.Capabilities = capabilities
		catalog.records[record.ImageDigest] = record
	}
	return catalog, nil
}

// LoadCapabilityCatalog accepts only the reviewed schema, with a bounded input
// and no raw document data in errors. Future formats require explicit support.
func LoadCapabilityCatalog(reader io.Reader) (*CapabilityCatalog, error) {
	const maxBytes = 256 * 1024
	if reader == nil {
		return nil, fmt.Errorf("qualification catalog is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return nil, fmt.Errorf("qualification catalog is unreadable or oversized")
	}
	var document struct {
		FormatVersion int                   `json:"formatVersion"`
		Records       []QualificationRecord `json:"records"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("qualification catalog has an invalid schema")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("qualification catalog has trailing content")
	}
	if document.FormatVersion != 1 || document.Records == nil {
		return nil, fmt.Errorf("qualification catalog format is unsupported")
	}
	return NewCapabilityCatalog(document.Records)
}

// NewProductionCapabilityCatalog is compiled into the production binary. Only
// reviewed, successful qualification evidence may add records here. It cannot
// be overridden by an environment variable, flag, external file or CR field.
func NewProductionCapabilityCatalog() (*CapabilityCatalog, error) {
	return LoadCapabilityCatalog(strings.NewReader(`{"formatVersion":1,"records":[]}`))
}

// Supports is deliberately fail-closed for unknown images and capabilities.
func (c *CapabilityCatalog) Supports(imageDigest string, capability BackupCapability) bool {
	if c == nil {
		return false
	}
	record, ok := c.records[imageDigest]
	return ok && record.Capabilities[capability]
}

func (c *CapabilityCatalog) BackupJobIsArchiveID(imageDigest string) bool {
	return c.Supports(imageDigest, CapabilityBackup) && c.records[imageDigest].JobIDIsArchiveID
}

// QualificationScope belongs solely to test harness code. It is intentionally
// not expressible through a manager flag, environment variable, or CR field.
type QualificationScope struct{ ImageDigest, Namespace, TargetUID string }

func (s QualificationScope) Validate(actualImageDigest, namespace, targetUID string) error {
	if s.ImageDigest == "" || s.Namespace == "" || s.TargetUID == "" {
		return fmt.Errorf("qualification scope is incomplete")
	}
	if s.ImageDigest != actualImageDigest || s.Namespace != namespace || s.TargetUID != targetUID {
		return fmt.Errorf("candidate is outside its isolated qualification scope")
	}
	return nil
}
