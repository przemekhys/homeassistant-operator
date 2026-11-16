package migration

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

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
	// ScanRequest.Token is deliberately not a Report field. This direct report
	// check makes that contract visible even if request handling changes.
	report := &Report{
		SourceID: "source",
		Target:   Target{Namespace: "ns", Name: "target", UID: "uid"},
	}
	if err := report.Finalize(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "scan-token") ||
		strings.Contains(string(encoded), "password") {
		t.Fatalf("report exposed sensitive request data: %s", encoded)
	}
}
