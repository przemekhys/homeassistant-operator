package migration

import (
	"encoding/json"
	"testing"
)

func TestProposalRejectsNestedDuplicateKeys(t *testing.T) {
	for _, raw := range []string{`{"config":{"token":"private"},"config":{}}`, `{"outer":{"x":1,"x":2}}`} {
		if !containsSensitiveKey(json.RawMessage(raw)) {
			t.Fatalf("accepted ambiguous proposal: %s", raw)
		}
	}
	if containsSensitiveKey(json.RawMessage(`{"config":{"brightness":1.5}}`)) {
		t.Fatal("valid fractional proposal rejected")
	}
}
