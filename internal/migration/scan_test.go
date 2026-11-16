package migration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

type scanReader struct {
	entries []haclient.ConfigEntry
	calls   int
}

type registryScanReader struct {
	scanReader
	raw json.RawMessage
}

func (r *registryScanReader) SendWebSocketCommand(
	_ context.Context,
	_ string,
	command string,
	_ map[string]interface{},
) (json.RawMessage, error) {
	if command != "config/floor_registry/list" {
		panic("unexpected HA operation: " + command)
	}
	return r.raw, nil
}

func TestScanProposesOnlyCompleteUnambiguousFloors(t *testing.T) {
	reader := &registryScanReader{
		raw: json.RawMessage(
			`[{"floor_id":"floor-a","name":"Ground","icon":null,"level":0},{"floor_id":"floor-b","name":"Missing"},{"floor_id":"duplicate","name":"One","icon":null,"level":null},{"floor_id":"duplicate","name":"Two","icon":null,"level":null}]`,
		),
	}
	report, err := Scan(
		context.Background(),
		reader,
		ScanRequest{
			SourceID: "source",
			Token:    "token",
			Target:   Target{Namespace: "default", Name: "target", UID: "target-uid"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var importable, incomplete, conflicts int
	for _, object := range report.Objects {
		switch object.Classification {
		case ClassificationImportable:
			importable++
		case ClassificationIncomplete:
			incomplete++
		case ClassificationConflict:
			conflicts++
		}
	}
	if importable != 1 || incomplete != 1 || conflicts != 2 {
		t.Fatalf("objects=%#v", report.Objects)
	}
}

func (r *scanReader) ListConfigEntries(
	_ context.Context,
	_ string,
) ([]haclient.ConfigEntry, error) {
	r.calls++
	return r.entries, nil
}

func TestScanProducesRedactedDeterministicUnsupportedReportWithoutWrites(t *testing.T) {
	reader := &scanReader{
		entries: []haclient.ConfigEntry{
			{EntryID: "entry-b", Domain: "mqtt"},
			{EntryID: "entry-a", Domain: "todo"},
		},
	}
	request := ScanRequest{
		SourceID: "source",
		Token:    "source-token",
		Target:   Target{Namespace: "default", Name: "target", UID: "target-uid"},
	}
	first, err := Scan(context.Background(), reader, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Scan(context.Background(), reader, request)
	if err != nil || first.ID != second.ID || reader.calls != 2 {
		t.Fatalf("scan report identity or read-only calls invalid: %#v, %v", second, err)
	}
	if len(first.Objects) != 2 || first.Objects[0].SourceID != "entry-a" ||
		first.Objects[0].Classification != ClassificationUnsupported {
		t.Fatalf("unexpected report objects: %#v", first.Objects)
	}
}

func TestScanReportsIncompleteIdentityWithoutSecrets(t *testing.T) {
	reader := &scanReader{entries: []haclient.ConfigEntry{{Domain: "unsafe"}}}
	report, err := Scan(
		context.Background(),
		reader,
		ScanRequest{
			SourceID: "source",
			Token:    "never-in-report",
			Target:   Target{Namespace: "default", Name: "target", UID: "uid"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Objects) != 1 ||
		report.Objects[0].Classification != ClassificationIncomplete {
		t.Fatalf("incomplete entry = %#v", report.Objects)
	}
	if containsSensitiveKey(report.Objects[0].Proposal) {
		t.Fatal("report contains sensitive proposal")
	}
}

func TestReportRejectsSourceURLContainingCredentials(t *testing.T) {
	for _, source := range []string{"https://user:password@example.test", "https://example.test/?token=secret", "https://example.test/#secret"} {
		report := &Report{SourceID: source, Target: Target{Namespace: "default", Name: "target", UID: "uid"}}
		if err := report.Finalize(); err == nil {
			t.Fatal("accepted source URL containing credentials")
		}
	}
}
