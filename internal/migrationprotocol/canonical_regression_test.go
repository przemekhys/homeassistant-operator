package migrationprotocol

import (
	"strings"
	"testing"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

func TestCanonicalJSONRejectsUnpairedSurrogates(t *testing.T) {
	for _, raw := range []string{`{"x":"\ud800"}`, `{"x":"\udc00"}`, `{"x":"\ud800\u0041"}`} {
		if _, err := CanonicalJSON([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"x":"\ud83d\ude00"}`, `{"x":"\\ud800"}`} {
		if _, err := CanonicalJSON([]byte(raw)); err != nil {
			t.Fatalf("rejected %s: %v", raw, err)
		}
	}
}

func TestEvidenceRejectsCaseAmbiguity(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	raw := `{"version":"v1","operationUID":"op","requestID":"` + id + `","stage":"backup","sourceID":"source","targetUID":"target","jobID":"job"}`
	if _, err := ParseEvidence([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEvidence([]byte(strings.Replace(raw, `"jobID":"job"`, `"jobID":"job","JOBID":"other"`, 1))); err == nil {
		t.Fatal("accepted case-ambiguous fields")
	}
}

func TestVerificationIDUsesCodePoints(t *testing.T) {
	item := ha.MigrationVerificationItem{Kind: "TodoItems", ID: strings.Repeat("ż", 256), ExpectedDigest: "sha256:" + strings.Repeat("a", 64)}
	if _, err := VerificationDigest([]ha.MigrationVerificationItem{item}); err != nil {
		t.Fatal(err)
	}
	item.ID += "ż"
	if _, err := VerificationDigest([]ha.MigrationVerificationItem{item}); err == nil {
		t.Fatal("accepted too many code points")
	}
}
