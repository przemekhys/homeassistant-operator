package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// TestObservationPublishesKeyStatusWhenReceiptFinalizationFails checks that a
// metadata conflict does not conceal successful protection of the observed key.
func TestObservationPublishesKeyStatusWhenReceiptFinalizationFails(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
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
			result = map[string]any{"state": "idle", "backups": []any{map[string]any{"backup_id": "archive-1"}},
				"agent_errors": map[string]any{"backup.local": "synthetic-secret"}}
		case "backup/details":
			result = map[string]any{"backup": map[string]any{
				"backup_id": "archive-1", "agents": map[string]any{"backup.local": map[string]any{}},
			}}
		default:
			return
		}
		_ = conn.WriteJSON(map[string]any{"id": command.ID, "type": "result", "success": true, "result": result})
	}))
	defer server.Close()
	scheme := runtime.NewScheme()
	_ = hav1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	receipt := backupReceipt{Version: 1, InstanceUID: "uid-1", RequestID: "request-1", Phase: "Accepted",
		ClaimedAt: time.Now().UTC(), ExpectedAgentIDs: []string{"backup.local"}, BackupID: "archive-1"}
	data, _ := json.Marshal(receipt)
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Name: "ha", Namespace: "default", UID: types.UID("uid-1"),
		Annotations: map[string]string{backupReceiptAnnotation: string(data)},
	}}
	storage := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*hav1.HomeAssistant); ok {
					return errors.New("synthetic receipt write failure")
				}
				return c.Update(ctx, obj, opts...)
			},
		}).Build()
	r := &HomeAssistantReconciler{Client: storage}
	key := "synthetic-key"
	config := &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{
		Password: &key, AgentIDs: []string{"backup.local"},
	}}
	api := haclient.NewClient("ws" + strings.TrimPrefix(server.URL, "http"))
	_, err := r.reconcileBackupObservation(context.Background(), ha, api, "test-token", config)
	if err == nil {
		t.Fatal("receipt error was not returned")
	}
	updated := &hav1.HomeAssistant{}
	objectKey := types.NamespacedName{Name: "ha", Namespace: "default"}
	if err := storage.Get(context.Background(), objectKey, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Backup == nil || updated.Status.Backup.KeySecretRef == nil {
		t.Fatal("protected key status was not persisted")
	}
	condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeBackupKeyProtected)
	if condition == nil || condition.Reason != "Protected" {
		t.Fatal("protected condition was not persisted")
	}
	observation := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeBackupObservation)
	if observation == nil || observation.Status != metav1.ConditionFalse || observation.Reason != "AgentReadError" ||
		strings.Contains(observation.Message, "synthetic-secret") {
		t.Fatal("agent read error was not safely reported as an observation failure")
	}
}

func TestBackupStatusProjectsScheduledArchiveWithoutRequest(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{Backups: []haclient.BackupArchive{{
		ID: "scheduled", Date: "2026-11-03T12:00:00Z", Automatic: true,
		Agents: map[string]haclient.BackupAgentCopy{"backup.local": {}},
	}}}, backupConfigForAgents("backup.local"))
	if ha.Status.Backup.Request != nil {
		t.Fatal("scheduled backup created a one-shot request status")
	}
	attempt := ha.Status.Backup.LastAttempt
	if attempt == nil || attempt.Source != "HomeAssistant" || attempt.Outcome != "Succeeded" ||
		attempt.BackupID != "scheduled" {
		t.Fatalf("scheduled archive status = %#v", attempt)
	}
}

func TestEarliestRequeuePreservesBackupRequestDeadline(t *testing.T) {
	result := earliestRequeue(
		ctrl.Result{RequeueAfter: 120 * time.Second},
		ctrl.Result{RequeueAfter: 10 * time.Second},
		ctrl.Result{RequeueAfter: 30 * time.Second},
	)
	if result.RequeueAfter != 10*time.Second {
		t.Fatalf("requeue after = %s, want 10s", result.RequeueAfter)
	}
}

func TestEarliestRequeuePreservesBackupFailureRetry(t *testing.T) {
	result := earliestRequeue(ctrl.Result{RequeueAfter: 2 * time.Minute}, ctrl.Result{RequeueAfter: 30 * time.Second})
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("requeue after = %s, want 30s", result.RequeueAfter)
	}
}

func TestHomeAssistantPrimaryPredicateKeepsBackupRequestAnnotation(t *testing.T) {
	oldHA := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"other": "value"}}}
	newHA := oldHA.DeepCopy()
	newHA.Annotations[backupRequestAnnotation] = "request-1"
	if !homeAssistantPrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldHA, ObjectNew: newHA}) {
		t.Fatal("backup request annotation update was filtered")
	}
	statusOnly := oldHA.DeepCopy()
	statusOnly.Status.Backup = &hav1.BackupStatus{}
	if homeAssistantPrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldHA, ObjectNew: statusOnly}) {
		t.Fatal("status-only update was not filtered")
	}
}

// TestBackupStatusAgentErrorDoesNotInventAttempt separates an agent read error
// from an unobserved backup execution failure.
func TestBackupStatusAgentErrorDoesNotInventAttempt(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{AgentErrors: map[string]any{
		"backup.local": "native create failed",
	}}, backupConfigForAgents("backup.local"))
	if ha.Status.Backup.LastAttempt != nil {
		t.Fatal("agent read error invented a backup attempt")
	}
	if ha.Status.Backup.LastSuccessfulBackupTime != nil {
		t.Fatal("failure without archive invented a successful backup time")
	}
}

// TestBackupStatusKeepsArchiveResultWhenAgentReadFails preserves existing
// archive evidence when another destination cannot be observed.
func TestBackupStatusKeepsArchiveResultWhenAgentReadFails(t *testing.T) {
	ha := &hav1.HomeAssistant{}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{
		State: "idle", AgentErrors: map[string]any{"backup.local": "unavailable"},
		Backups: []haclient.BackupArchive{{ID: "archive-1", Date: "2026-11-03T12:00:00Z",
			Agents: map[string]haclient.BackupAgentCopy{"backup.local": {}}}},
	}, backupConfigForAgents("backup.local"))
	if ha.Status.Backup.LastAttempt == nil || ha.Status.Backup.LastAttempt.Outcome != "Succeeded" {
		t.Fatal("agent read error replaced independently observed archive outcome")
	}
}

func TestFinalizeInvalidReceiptDoesNotBlockObservation(t *testing.T) {
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		backupReceiptAnnotation: "invalid receipt",
	}}}
	err := (&HomeAssistantReconciler{}).finalizeAcceptedBackupReceipt(context.Background(), ha, nil, "", nil)
	if err != nil || ha.Annotations[backupReceiptAnnotation] != "invalid receipt" {
		t.Fatal("invalid receipt must be left for the request reconciler")
	}
}

func TestBackupStatusKeepsNewerConfirmedSuccessWhenHistoryIsOld(t *testing.T) {
	prior := metav1.NewTime(mustTime(t, "2026-11-03T12:00:00Z"))
	ha := &hav1.HomeAssistant{Status: hav1.HomeAssistantStatus{
		Backup: &hav1.BackupStatus{LastSuccessfulBackupTime: &prior},
	}}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{Backups: []haclient.BackupArchive{{
		ID: "old", Date: "2026-11-03T11:00:00Z", Agents: map[string]haclient.BackupAgentCopy{"backup.local": {}},
	}}}, backupConfigForAgents("backup.local"))
	if got := ha.Status.Backup.LastSuccessfulBackupTime.Time; !got.Equal(prior.Time) {
		t.Fatalf("old backup history regressed success time to %s", got)
	}
}

func TestBackupStatusUnknownReceiptCanLaterResolveWithoutRedispatch(t *testing.T) {
	claimedAt := time.Now().Add(-16 * time.Minute)
	ha := &hav1.HomeAssistant{}
	setBackupRequestStatus(ha, &backupReceipt{
		RequestID: "request-1", BackupID: "archive-1", Phase: "Unknown", ClaimedAt: claimedAt,
		ExpectedAgentIDs: []string{"backup.local"},
	})
	if ha.Status.Backup.LastAttempt.Outcome != "Unknown" {
		t.Fatalf("expired receipt outcome = %q, want Unknown", ha.Status.Backup.LastAttempt.Outcome)
	}
	(&HomeAssistantReconciler{}).projectBackupInfo(ha, &haclient.BackupInfo{Backups: []haclient.BackupArchive{{
		ID: "archive-1", Date: "2026-11-03T12:00:00Z", Agents: map[string]haclient.BackupAgentCopy{"backup.local": {}},
	}}}, backupConfigForAgents("backup.local"))
	if ha.Status.Backup.LastAttempt.Outcome != "Succeeded" {
		t.Fatalf("later archive evidence did not update observation: %#v", ha.Status.Backup.LastAttempt)
	}
}
