package migration

import (
	"context"
	"fmt"
	"strings"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// IntegrationReader is deliberately read-only. Scan never receives a
// Kubernetes client and therefore cannot mutate Kubernetes or Home Assistant.
type IntegrationReader interface {
	ListConfigEntries(context.Context, string) ([]haclient.ConfigEntry, error)
}

type ScanRequest struct {
	SourceID string
	Token    string
	Target   Target
}

func Scan(
	ctx context.Context,
	reader IntegrationReader,
	request ScanRequest,
) (*Report, error) {
	if reader == nil || strings.TrimSpace(request.SourceID) == "" ||
		strings.TrimSpace(request.Token) == "" {
		return nil, fmt.Errorf(
			"migration scan requires a source identity, token reference, and reader",
		)
	}
	entries, err := reader.ListConfigEntries(ctx, request.Token)
	if err != nil {
		return nil, fmt.Errorf("read migration source: %w", err)
	}
	report := &Report{
		Version:  ReportVersion,
		SourceID: request.SourceID,
		Target:   request.Target,
	}
	for _, entry := range entries {
		id := strings.TrimSpace(entry.EntryID)
		if id == "" {
			report.Objects = append(
				report.Objects,
				ReportObject{
					Kind:           "Integration",
					SourceID:       "missing",
					Name:           "redacted",
					Classification: ClassificationIncomplete,
					Reason:         "missing immutable config-entry ID",
				},
			)
			continue
		}
		// The capability matrix has no full, secret-safe config-flow export yet.
		report.Objects = append(
			report.Objects,
			ReportObject{
				Kind:           "Integration",
				SourceID:       id,
				Name:           entry.Domain,
				Classification: ClassificationUnsupported,
				Reason:         "full secret-safe integration export is not verified",
			},
		)
	}
	if registry, ok := reader.(floorRegistryReader); ok {
		floors, err := scanFloors(ctx, registry, request.Token)
		if err != nil {
			return nil, err
		}
		report.Objects = append(report.Objects, floors...)
	}
	if backup, ok := reader.(backupPreflightReader); ok {
		report.Backup = observeBackupPreflight(ctx, backup, request.Token)
	}
	if err := report.Finalize(); err != nil {
		return nil, err
	}
	return report, nil
}
