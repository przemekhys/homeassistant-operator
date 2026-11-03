package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

func TestReceiptFromHARejectsMalformedOrForeignReceipts(t *testing.T) {
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{UID: types.UID("current")}}
	for name, annotation := range map[string]string{
		"invalid JSON":    `{"version":`,
		"unknown version": `{"version":2,"instanceUID":"current"}`,
		"foreign UID":     `{"version":1,"instanceUID":"other"}`,
		"invalid request": `{"version":1,"instanceUID":"current","requestID":"bad id","phase":"Accepted","claimedAt":"2026-11-03T10:00:00Z","expectedAgentIDs":["backup.local"]}`,
		"invalid phase":   `{"version":1,"instanceUID":"current","requestID":"request-1","phase":"Retry","claimedAt":"2026-11-03T10:00:00Z","expectedAgentIDs":["backup.local"]}`,
		"duplicate agent": `{"version":1,"instanceUID":"current","requestID":"request-1","phase":"Accepted","claimedAt":"2026-11-03T10:00:00Z","expectedAgentIDs":["backup.local","backup.local"]}`,
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

func TestBackupRequestIDPattern(t *testing.T) {
	for _, id := range []string{"request-1", "A", "backup.request_2026"} {
		if !backupRequestIDPattern.MatchString(id) {
			t.Fatalf("valid request ID %q was rejected", id)
		}
	}
	for _, id := range []string{"", "-starts-with-dash", "contains space", strings.Repeat("a", 65)} {
		if backupRequestIDPattern.MatchString(id) {
			t.Fatalf("invalid request ID %q was accepted", id)
		}
	}
}

func TestReconcileBackupRequestDispatchesAcceptedRequestOnlyOnce(t *testing.T) {
	var mu sync.Mutex
	generateCalls := 0
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
		var command struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		}
		if err := conn.ReadJSON(&command); err != nil {
			return
		}
		var result any
		switch command.Type {
		case "backup/agents/info":
			result = map[string]any{"agents": []map[string]string{{"agent_id": "backup.local", "name": "Local"}}}
		case "backup/info":
			result = map[string]any{"state": "idle"}
		case "backup/generate_with_automatic_settings":
			mu.Lock()
			generateCalls++
			mu.Unlock()
			result = map[string]string{"backup_job_id": "backup-1"}
		default:
			t.Errorf("unexpected command %q", command.Type)
			return
		}
		_ = conn.WriteJSON(map[string]any{"id": command.ID, "type": "result", "success": true, "result": result})
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Name: "ha", Namespace: "default", UID: types.UID("instance-uid"),
		Annotations: map[string]string{backupRequestAnnotation: "request-1"},
	}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha).Build()
	r := &HomeAssistantReconciler{Client: client}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	c := haclient.NewClient(wsURL)
	config := &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{AgentIDs: []string{"backup.local"}}}

	for range 10 {
		current := &hav1.HomeAssistant{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: "ha", Namespace: "default"}, current); err != nil {
			t.Fatal(err)
		}
		if _, err := r.reconcileBackupRequest(context.Background(), current, c, "token", config); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if generateCalls != 1 {
		t.Fatalf("generate call count = %d, want 1", generateCalls)
	}
}

func TestReconcileBackupRequestNeverRedispatchesUncertainReceipt(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for name, receipt := range map[string]*backupReceipt{
		"interrupted dispatch": {
			Version: 1, InstanceUID: "instance-uid", RequestID: "request-1", Phase: "Dispatching",
			ClaimedAt: time.Now().UTC(), ExpectedAgentIDs: []string{"backup.local"},
		},
		"accepted timeout": {
			Version: 1, InstanceUID: "instance-uid", RequestID: "request-1", Phase: "Accepted",
			ClaimedAt: time.Now().Add(-16 * time.Minute).UTC(), ExpectedAgentIDs: []string{"backup.local"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
				Name: "ha", Namespace: "default", UID: types.UID("instance-uid"),
				Annotations: map[string]string{backupRequestAnnotation: "request-1", backupReceiptAnnotation: string(data)},
			}}
			client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha).Build()
			r := &HomeAssistantReconciler{Client: client}
			if _, err := r.reconcileBackupRequest(context.Background(), ha, nil, "", nil); err != nil {
				t.Fatal(err)
			}
			updated := &hav1.HomeAssistant{}
			if err := client.Get(context.Background(), types.NamespacedName{Name: "ha", Namespace: "default"}, updated); err != nil {
				t.Fatal(err)
			}
			got, err := receiptFromHA(updated)
			if err != nil {
				t.Fatal(err)
			}
			if got.Phase != "Unknown" {
				t.Fatalf("phase = %q, want Unknown", got.Phase)
			}
		})
	}
}
