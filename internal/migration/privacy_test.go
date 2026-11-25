package migration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
)

func TestImportResultJSONNeverSerializesSecretValues(t *testing.T) {
	value := "private-credential"
	result := ImportResult{
		Secret: &corev1.Secret{Data: map[string][]byte{"key": []byte(value)}},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), value) ||
		strings.Contains(string(encoded), base64.StdEncoding.EncodeToString([]byte(value))) {
		t.Fatal("serialized import result contains secret data")
	}
}

func TestReportRejectsSensitiveProposalKeysWithoutSerializingValues(t *testing.T) {
	for _, key := range []string{"token", "Password", "SECRET", "api_key"} {
		t.Run(key, func(t *testing.T) {
			const sensitive = "value-that-must-not-escape"
			proposal, err := json.Marshal(map[string]string{key: sensitive})
			if err != nil {
				t.Fatal(err)
			}
			report := &Report{
				SourceID: "source",
				Target:   Target{Namespace: "ns", Name: "target", UID: "uid"},
				Objects: []ReportObject{
					{
						Kind:           "Integration",
						SourceID:       "entry",
						Name:           "redacted",
						Classification: ClassificationUnsupported,
						Proposal:       proposal,
					},
				},
			}
			err = report.Finalize()
			if err == nil || strings.Contains(err.Error(), sensitive) {
				t.Fatalf("unsafe report validation error: %v", err)
			}
		})
	}
}

func TestReportJSONNeverContainsScanToken(t *testing.T) {
	const token = "unique-scan-token-must-not-escape"
	reader := &scanReader{entries: []haclient.ConfigEntry{{EntryID: "entry", Domain: "mqtt"}}}
	report, err := Scan(context.Background(), reader, ScanRequest{
		SourceID: "source", Token: token, Target: Target{Namespace: "ns", Name: "target", UID: "uid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || len(report.Objects) == 0 {
		t.Fatal("scan did not read the integration entry")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), base64.StdEncoding.EncodeToString([]byte(token))) {
		t.Fatal("report exposed the scan token")
	}
}
