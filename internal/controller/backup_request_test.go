package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

func TestReceiptFromHARejectsMalformedOrForeignReceipts(t *testing.T) {
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{UID: types.UID("current")}}
	for name, annotation := range map[string]string{
		"invalid JSON":    `{"version":`,
		"unknown version": `{"version":2,"instanceUID":"current"}`,
		"foreign UID":     `{"version":1,"instanceUID":"other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			ha.Annotations = map[string]string{backupReceiptAnnotation: annotation}
			if _, err := receiptFromHA(ha); err == nil {
				t.Fatal("receiptFromHA() accepted an unsafe receipt")
			}
		})
	}
}

func TestReceiptFromHAAcceptsCurrentReceipt(t *testing.T) {
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		UID: types.UID("current"),
		Annotations: map[string]string{backupReceiptAnnotation: `{
            "version":1,"instanceUID":"current","requestID":"request-1",
            "phase":"Accepted","claimedAt":"2026-11-03T10:00:00Z",
            "expectedAgentIDs":["backup.local"],"backupID":"backup-1"
        }`},
	}}
	receipt, err := receiptFromHA(ha)
	if err != nil {
		t.Fatalf("receiptFromHA() error = %v", err)
	}
	if receipt.RequestID != "request-1" || receipt.Phase != "Accepted" || receipt.BackupID != "backup-1" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
}

func TestSetBackupRequestStatusDoesNotExposeReceiptReason(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	receipt := &backupReceipt{
		RequestID: "request-1", Phase: "Unknown", BackupID: "backup-1",
		ClaimedAt: time.Now().UTC(), Reason: "SubmissionUncertain",
	}
	setBackupRequestStatus(ha, receipt)
	if ha.Status.Backup == nil || ha.Status.Backup.Request == nil {
		t.Fatal("request status was not projected")
	}
	if got := ha.Status.Backup.Request; got.ID != "request-1" || got.Phase != "Unknown" || got.BackupID != "backup-1" {
		t.Fatalf("unexpected request status: %#v", got)
	}
}
