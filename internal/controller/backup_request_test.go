package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// TestReceiptFromHARejectsMalformedOrForeignReceipts protects the dispatch
// boundary from invalid or cross-instance annotations.
func TestReceiptFromHARejectsMalformedOrForeignReceipts(t *testing.T) {
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{UID: types.UID("current")}}
	for name, annotation := range map[string]string{
		"invalid JSON":    `{"version":`,
		"unknown version": `{"version":2,"instanceUID":"current"}`,
		"foreign UID":     `{"version":1,"instanceUID":"other"}`,
		"invalid request": `{"version":1,"instanceUID":"current","requestID":"bad id","phase":"Accepted",` +
			`"claimedAt":"2026-11-03T10:00:00Z","expectedAgentIDs":["backup.local"]}`,
		"invalid phase": `{"version":1,"instanceUID":"current","requestID":"request-1","phase":"Retry",` +
			`"claimedAt":"2026-11-03T10:00:00Z","expectedAgentIDs":["backup.local"]}`,
		"duplicate agent": `{"version":1,"instanceUID":"current","requestID":"request-1","phase":"Accepted",` +
			`"claimedAt":"2026-11-03T10:00:00Z","expectedAgentIDs":["backup.local","backup.local"]}`,
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

// TestBackupRequestStatusPreservesNewerObservation checks that replaying an
// unchanged receipt cannot hide a subsequent native backup attempt.
func TestBackupRequestStatusPreservesNewerObservation(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	receipt := &backupReceipt{RequestID: "request-1", Phase: "Accepted", ClaimedAt: time.Now().UTC()}
	setBackupRequestStatus(ha, receipt)
	observed := &hav1.BackupAttemptStatus{Source: "HomeAssistant", BackupID: "scheduled", Outcome: "Succeeded"}
	ha.Status.Backup.LastAttempt = observed
	setBackupRequestStatus(ha, receipt)
	if ha.Status.Backup.LastAttempt != observed {
		t.Fatal("unchanged receipt replaced a newer observed attempt")
	}
	receipt.Phase = "Succeeded"
	setBackupRequestStatus(ha, receipt)
	if ha.Status.Backup.LastAttempt.Source != "Operator" || ha.Status.Backup.LastAttempt.Outcome != "Succeeded" {
		t.Fatal("receipt phase change was not projected")
	}
	receipt.RequestID, receipt.Phase = "request-2", "Dispatching"
	setBackupRequestStatus(ha, receipt)
	if ha.Status.Backup.LastAttempt.RequestID != "request-2" || ha.Status.Backup.LastAttempt.Outcome != "Running" {
		t.Fatal("new receipt was not projected")
	}
}

// TestBackupRequestReportsRejectedRequest verifies that invalid and busy
// requests remain unprocessed and expose safe status reasons.
func TestBackupRequestReportsRejectedRequest(t *testing.T) {
	for _, tc := range []struct{ name, requestID, phase, reason string }{
		{"invalid", "invalid request", "", "InvalidRequest"},
		{"busy", "next-request", "Accepted", "RequestInProgress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = hav1.AddToScheme(scheme)
			ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
				Name: "ha", Namespace: "default", UID: types.UID("uid-1"),
				Annotations: map[string]string{backupRequestAnnotation: tc.requestID},
			}}
			if tc.phase != "" {
				receipt := backupReceipt{Version: 1, InstanceUID: "uid-1", RequestID: "previous", Phase: tc.phase,
					ClaimedAt: time.Now().UTC(), ExpectedAgentIDs: []string{"backup.local"}}
				data, _ := json.Marshal(receipt)
				ha.Annotations[backupReceiptAnnotation] = string(data)
			}
			storage := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha).Build()
			r := &HomeAssistantReconciler{Client: storage}
			if _, err := r.reconcileBackupRequest(context.Background(), ha, nil, "", nil); err != nil {
				t.Fatal(err)
			}
			got := &hav1.HomeAssistant{}
			key := types.NamespacedName{Name: "ha", Namespace: "default"}
			if err := storage.Get(context.Background(), key, got); err != nil {
				t.Fatal(err)
			}
			cond := meta.FindStatusCondition(got.Status.Conditions, "BackupRequestReady")
			if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != tc.reason {
				t.Fatalf("condition = %#v", cond)
			}
			if got.Annotations[backupRequestAnnotation] != tc.requestID {
				t.Fatal("request annotation changed")
			}
		})
	}
}

// TestReconcileBackupRequestDispatchesAcceptedRequestOnlyOnce counts native
// generation across repeated reconciles and a reconciler restart.
func TestReconcileBackupRequestDispatchesAcceptedRequestOnlyOnce(t *testing.T) {
	var mu sync.Mutex
	generateCalls := 0
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
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

	for i := range 10 {
		if i == 5 {
			// A new reconciler simulates a process restart while the durable
			// receipt remains in the API server.
			r = &HomeAssistantReconciler{Client: client}
		}
		current := &hav1.HomeAssistant{}
		key := types.NamespacedName{Name: "ha", Namespace: "default"}
		if err := client.Get(context.Background(), key, current); err != nil {
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

// TestReconcileBackupRequestNeverRedispatchesUncertainReceipt verifies that
// ambiguous dispatches and timed-out accepted requests are not retried.
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
			key := types.NamespacedName{Name: "ha", Namespace: "default"}
			if err := client.Get(context.Background(), key, updated); err != nil {
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

// TestBackupDispatchContinuesWhenInitialStatusWriteFails verifies that a
// durably claimed request is dispatched once despite a status-only failure.
func TestBackupDispatchContinuesWhenInitialStatusWriteFails(t *testing.T) {
	var calls atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
		var command struct {
			ID   int    `json:"id"`
			Type string `json:"type"`
		}
		if conn.ReadJSON(&command) != nil {
			return
		}
		var result any
		switch command.Type {
		case "backup/agents/info":
			result = map[string]any{"agents": []any{map[string]string{"agent_id": "backup.local"}}}
		case "backup/info":
			result = map[string]any{"state": "idle"}
		case "backup/generate_with_automatic_settings":
			calls.Add(1)
			result = map[string]string{"backup_job_id": "backup-1"}
		default:
			return
		}
		_ = conn.WriteJSON(map[string]any{"id": command.ID, "type": "result", "success": true, "result": result})
	}))
	defer server.Close()
	scheme := runtime.NewScheme()
	_ = hav1.AddToScheme(scheme)
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "default", UID: types.UID("uid-1"),
		Annotations: map[string]string{backupRequestAnnotation: "request-1"}}}
	var statusWrites atomic.Int32
	storage := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c k8sclient.Client,
			name string, obj k8sclient.Object, opts ...k8sclient.SubResourceUpdateOption) error {
			if name == "status" && statusWrites.Add(1) == 1 {
				return errors.New("synthetic status failure")
			}
			return c.SubResource(name).Update(ctx, obj, opts...)
		}}).Build()
	r := &HomeAssistantReconciler{Client: storage}
	config := &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{AgentIDs: []string{"backup.local"}}}
	api := haclient.NewClient(server.URL)
	if _, err := r.reconcileBackupRequest(context.Background(), ha, api, "token", config); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("generate calls = %d, want 1", calls.Load())
	}
	updated := &hav1.HomeAssistant{}
	key := types.NamespacedName{Name: "ha", Namespace: "default"}
	if err := storage.Get(context.Background(), key, updated); err != nil {
		t.Fatal(err)
	}
	stored, err := receiptFromHA(updated)
	if err != nil || stored == nil || stored.Phase != "Accepted" {
		t.Fatal("accepted receipt not persisted")
	}
	if _, err := r.reconcileBackupRequest(context.Background(), updated, api, "token", config); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("generate calls after reconcile = %d, want 1", calls.Load())
	}
}
