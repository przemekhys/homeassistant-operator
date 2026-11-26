package controller

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMigrationReceiptAndConditionMessagesDoNotContainSecrets(t *testing.T) {
	const recoveryKey = "recovery-key-must-not-appear"
	receipt, err := json.Marshal(
		migrationReceipt{Generation: 1, TargetUID: "target-uid", Stage: "approved"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(receipt), recoveryKey) ||
		strings.Contains(string(receipt), "token") {
		t.Fatalf("receipt exposed sensitive value: %s", receipt)
	}
	for _, message := range []string{
		"migration is waiting for a durable approval or implementation evidence",
		"migration upload outcome unknown",
		"migration restore rejected: HTTP 400",
	} {
		if strings.Contains(message, recoveryKey) {
			t.Fatalf("unsafe status or error message: %s", message)
		}
	}
}
