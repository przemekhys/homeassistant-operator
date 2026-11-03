package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

func TestBackupErrorsDoNotRenderSensitiveWrappedData(t *testing.T) {
	const secret = "test-recovery-key-do-not-log"
	err := &haclient.Error{
		Type: haclient.ErrorTypeHTTP, Message: "backup command transport failed", Err: errors.New(secret),
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("backup error leaked sensitive wrapped data: %q", err)
	}
	_, message := backupConfigReadFailure(err)
	if strings.Contains(message, secret) {
		t.Fatalf("backup status message leaked sensitive wrapped data: %q", message)
	}
}

func TestBackupStatusMessagesAreClosedAndDoNotContainCredentials(t *testing.T) {
	for _, err := range []error{
		&haclient.Error{Type: haclient.ErrorTypeHTTP, Code: "forbidden", Err: errors.New("token-value")},
		&haclient.Error{Type: haclient.ErrorTypeHTTP, Err: errors.New("recovery-key")},
	} {
		_, message := backupConfigReadFailure(err)
		if strings.Contains(message, "token-value") || strings.Contains(message, "recovery-key") {
			t.Fatalf("unsafe backup condition message: %q", message)
		}
	}
}
