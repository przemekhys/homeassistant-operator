package haclient

import "fmt"

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
	ImageDigest  string
	Capabilities map[BackupCapability]bool
}

type CapabilityCatalog struct {
	records map[string]QualificationRecord
}

func NewCapabilityCatalog(records []QualificationRecord) (*CapabilityCatalog, error) {
	catalog := &CapabilityCatalog{records: make(map[string]QualificationRecord, len(records))}
	for _, record := range records {
		if record.ImageDigest == "" {
			return nil, fmt.Errorf("qualification record requires image digest")
		}
		if _, exists := catalog.records[record.ImageDigest]; exists {
			return nil, fmt.Errorf("duplicate qualification record for image digest")
		}
		catalog.records[record.ImageDigest] = record
	}
	return catalog, nil
}

// Supports is deliberately fail-closed for unknown images and capabilities.
func (c *CapabilityCatalog) Supports(imageDigest string, capability BackupCapability) bool {
	if c == nil {
		return false
	}
	record, ok := c.records[imageDigest]
	return ok && record.Capabilities[capability]
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
