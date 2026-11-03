package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

type staticBackupKeyProtection struct{ value BackupKeyProtection }

func (p staticBackupKeyProtection) CurrentBackupKey(
	context.Context,
	*hav1.HomeAssistant,
	*haclient.BackupConfig,
	[]string,
) (BackupKeyProtection, error) {
	return p.value, nil
}

func TestHomeAssistantBackupReconcilerDoesNotDispatchWithoutKeyProtection(t *testing.T) {
	reconciler, operation, calls := newBackupOperationReconciler(t, nil)
	if _, err := reconciler.Reconcile(context.Background(), requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated := &hav1alpha1.HomeAssistantBackup{}
	if err := reconciler.Get(context.Background(), requestFor(operation).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != hav1alpha1.BackupPhaseValidating {
		t.Fatalf("phase = %s", updated.Status.Phase)
	}
	if calls.generate.Load() != 0 {
		t.Fatalf("native generate calls = %d, want 0", calls.generate.Load())
	}
}

func TestRetainedBackupKeyProviderFailsClosedForMissingStaleOrUnprotectedEvidence(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "home", UID: "target-uid"}}
	key := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "retained-key",
			UID:       "retained-key-uid",
			Labels: map[string]string{
				backupKeyInstanceUIDLabelKey: string(target.UID),
				backupKeyPurposeLabelKey:     backupKeyHistoryPurpose,
			},
		},
		Immutable: ptr.To(true),
		Type:      corev1.SecretTypeOpaque,
		Data:      map[string][]byte{backupKeySecretDataKey: []byte("backup-key-canary")},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(key).Build()
	provider := RetainedBackupKeyProvider{Reader: c}

	if _, err := provider.CurrentBackupKey(ctx, target, nil, []string{"backup.local"}); err == nil {
		t.Fatal("missing key reference was accepted")
	}

	target.Status.Backup = &hav1.BackupStatus{CurrentKeySecretRef: &hav1.BackupKeySecretReference{
		Name: key.Name, UID: "stale-uid", Key: backupKeySecretDataKey,
	}}
	if _, err := provider.CurrentBackupKey(ctx, target, &haclient.BackupConfig{}, []string{"backup.local"}); err == nil {
		t.Fatal("stale key reference was accepted")
	}

	target.Status.Backup.CurrentKeySecretRef.UID = string(key.UID)
	protection, err := provider.CurrentBackupKey(
		ctx,
		target,
		&haclient.BackupConfig{Agents: map[string]haclient.BackupAgentConfig{"backup.local": {Protected: false}}},
		[]string{"backup.local"},
	)
	if err == nil || protection.Verified || protection.Password != "" {
		t.Fatalf("unverified destination protection = %#v, %v", protection, err)
	}
}

func TestRetainedBackupKeyProviderReturnsCurrentKeyOnlyForProtectedDestinations(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "home", UID: "target-uid"}}
	key := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: target.Namespace,
			Name:      "retained-key",
			UID:       "retained-key-uid",
			Labels: map[string]string{
				backupKeyInstanceUIDLabelKey: string(target.UID),
				backupKeyPurposeLabelKey:     backupKeyHistoryPurpose,
			},
		},
		Immutable: ptr.To(true),
		Type:      corev1.SecretTypeOpaque,
		Data:      map[string][]byte{backupKeySecretDataKey: []byte("backup-key-canary")},
	}
	target.Status.Backup = &hav1.BackupStatus{
		CurrentKeySecretRef: &hav1.BackupKeySecretReference{
			Name: key.Name,
			UID:  string(key.UID),
			Key:  backupKeySecretDataKey,
		},
	}
	provider := RetainedBackupKeyProvider{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(key).Build()}

	protection, err := provider.CurrentBackupKey(
		ctx,
		target,
		&haclient.BackupConfig{
			Agents: map[string]haclient.BackupAgentConfig{
				"backup.local": {Protected: true},
				"remote":       {Protected: true},
			},
		},
		[]string{"backup.local", "remote"},
	)
	if err != nil || !protection.Verified || protection.Password != "backup-key-canary" {
		t.Fatalf("protection = %#v, %v", protection, err)
	}
}

func TestHomeAssistantBackupReconcilerRecordsDispatchBeforeCallingNativeAPI(t *testing.T) {
	reconciler, operation, calls := newBackupOperationReconciler(
		t,
		staticBackupKeyProtection{value: BackupKeyProtection{Password: "synthetic-key", Verified: true}},
	)
	ctx := context.Background()
	if _, err := reconciler.Reconcile(ctx, requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated := &hav1alpha1.HomeAssistantBackup{}
	if err := reconciler.Get(ctx, requestFor(operation).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != hav1alpha1.BackupPhaseRunning || updated.Status.BackupJobID != "native-job" {
		t.Fatalf("status = %#v", updated.Status)
	}
	if calls.generate.Load() != 1 {
		t.Fatalf("native generate calls = %d, want 1", calls.generate.Load())
	}
	target := &hav1.HomeAssistant{}
	targetKey := types.NamespacedName{Namespace: operation.Namespace, Name: operation.Spec.HomeAssistantRef.Name}
	if err := reconciler.Get(ctx, targetKey, target); err != nil {
		t.Fatal(err)
	}
	if target.Status.Backup == nil || target.Status.Backup.LastAttempt == nil ||
		target.Status.Backup.LastAttempt.IsZero() {
		t.Fatalf("last backup attempt was not recorded: %#v", target.Status.Backup)
	}
	health := meta.FindStatusCondition(target.Status.Conditions, conditionTypeBackupHealthy)
	if health == nil || health.Status != metav1.ConditionUnknown || health.Reason != "BackupOutcomePending" {
		t.Fatalf("backup health after accepted request = %#v", health)
	}
}

func TestHomeAssistantBackupReconcilerPollsCorrelatedArchiveAndRecordsSuccess(t *testing.T) {
	reconciler, operation, _ := newBackupOperationReconciler(
		t,
		staticBackupKeyProtection{value: BackupKeyProtection{Password: "synthetic-key", Verified: true}},
	)
	ctx := context.Background()
	if _, err := reconciler.Reconcile(ctx, requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated := getBackupOperation(t, reconciler, operation)
	updated.Status.BackupID = "archive-id"
	if err := reconciler.Status().Update(ctx, updated); err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{}
	targetKey := types.NamespacedName{Namespace: operation.Namespace, Name: operation.Spec.HomeAssistantRef.Name}
	if err := reconciler.Get(ctx, targetKey, target); err != nil {
		t.Fatal(err)
	}
	client := newHAClientForHA(target, reconciler.NewHAClient)
	if _, err := reconciler.observeBackup(ctx, updated, target, client, "test-token"); err != nil {
		t.Fatal(err)
	}
	updated = getBackupOperation(t, reconciler, operation)
	if updated.Status.Phase != hav1alpha1.BackupPhaseSucceeded {
		t.Fatalf("phase = %s, want succeeded", updated.Status.Phase)
	}
	if err := reconciler.Get(ctx, targetKey, target); err != nil {
		t.Fatal(err)
	}
	if target.Status.Backup == nil || target.Status.Backup.LastSuccess == nil ||
		target.Status.Backup.LastSuccess.IsZero() {
		t.Fatalf("last backup success was not recorded: %#v", target.Status.Backup)
	}
	if target.Status.Backup.LastSuccess.Before(target.Status.Backup.LastAttempt) {
		t.Fatalf(
			"success timestamp %s precedes attempt %s",
			target.Status.Backup.LastSuccess.Format(time.RFC3339Nano),
			target.Status.Backup.LastAttempt.Format(time.RFC3339Nano),
		)
	}
}

func TestHomeAssistantBackupReconcilerRejectsTargetUIDMismatchBeforeNativeCalls(t *testing.T) {
	reconciler, operation, calls := newBackupOperationReconciler(
		t,
		staticBackupKeyProtection{value: BackupKeyProtection{Password: "synthetic-key", Verified: true}},
		backupServerOptions{operationTargetUID: "old-target-uid"},
	)
	if _, err := reconciler.Reconcile(context.Background(), requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated := getBackupOperation(t, reconciler, operation)
	if updated.Status.Phase != hav1alpha1.BackupPhaseFailed || backupConditionReason(updated) != "TargetUIDMismatch" {
		t.Fatalf("status = %#v", updated.Status)
	}
	if calls.config.Load() != 0 || calls.agents.Load() != 0 || calls.generate.Load() != 0 {
		t.Fatalf("native calls = %#v, want none", calls)
	}
}

func TestHomeAssistantBackupReconcilerWaitsForMissingAgent(t *testing.T) {
	reconciler, operation, calls := newBackupOperationReconciler(
		t,
		staticBackupKeyProtection{value: BackupKeyProtection{Password: "synthetic-key", Verified: true}},
		backupServerOptions{agentIDs: []string{"missing"}},
	)
	if _, err := reconciler.Reconcile(context.Background(), requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated := getBackupOperation(t, reconciler, operation)
	if updated.Status.Phase != hav1alpha1.BackupPhaseValidating ||
		backupConditionReason(updated) != "BackupDestinationUnavailable" {
		t.Fatalf("status = %#v", updated.Status)
	}
	if calls.generate.Load() != 0 {
		t.Fatalf("native generate calls = %d, want 0", calls.generate.Load())
	}
}

func TestHomeAssistantBackupReconcilerWaitsWhileSynchronizationIsPaused(t *testing.T) {
	reconciler, operation, calls := newBackupOperationReconciler(
		t,
		staticBackupKeyProtection{value: BackupKeyProtection{Password: "synthetic-key", Verified: true}},
		backupServerOptions{paused: true},
	)
	if _, err := reconciler.Reconcile(context.Background(), requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated := getBackupOperation(t, reconciler, operation)
	if updated.Status.Phase != hav1alpha1.BackupPhaseWaitingForGate ||
		backupConditionReason(updated) != "SynchronizationPaused" {
		t.Fatalf("status = %#v", updated.Status)
	}
	if calls.config.Load() != 0 || calls.agents.Load() != 0 || calls.generate.Load() != 0 {
		t.Fatalf("native calls = %#v, want none", calls)
	}
}

func TestHomeAssistantBackupReconcilerFailsForUnsupportedNativeCapability(t *testing.T) {
	reconciler, operation, calls := newBackupOperationReconciler(
		t,
		staticBackupKeyProtection{value: BackupKeyProtection{Password: "synthetic-key", Verified: true}},
		backupServerOptions{unsupportedGenerate: true},
	)
	if _, err := reconciler.Reconcile(context.Background(), requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated := getBackupOperation(t, reconciler, operation)
	if updated.Status.Phase != hav1alpha1.BackupPhaseFailed ||
		backupConditionReason(updated) != "UnsupportedCapability" {
		t.Fatalf("status = %#v", updated.Status)
	}
	if calls.generate.Load() != 1 {
		t.Fatalf("native generate calls = %d, want 1", calls.generate.Load())
	}
}

func TestHomeAssistantBackupReconcilerDoesNotRedispatchAfterLostNativeResponse(t *testing.T) {
	reconciler, operation, calls := newBackupOperationReconciler(
		t,
		staticBackupKeyProtection{value: BackupKeyProtection{Password: "synthetic-key", Verified: true}},
		backupServerOptions{dropGenerateResponse: true},
	)
	ctx := context.Background()
	if _, err := reconciler.Reconcile(ctx, requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated := getBackupOperation(t, reconciler, operation)
	if updated.Status.Phase != hav1alpha1.BackupPhaseOutcomeUnknown ||
		backupConditionReason(updated) != "DispatchOutcomeUnknown" {
		t.Fatalf("status = %#v", updated.Status)
	}
	if calls.generate.Load() != 1 {
		t.Fatalf("native generate calls = %d, want 1", calls.generate.Load())
	}

	// Simulate a status write lost after process replacement. Durable evidence,
	// not the status phase, must prevent a second native command.
	updated.Status = hav1alpha1.HomeAssistantBackupStatus{}
	if err := reconciler.Status().Update(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated = getBackupOperation(t, reconciler, operation)
	if updated.Status.Phase != hav1alpha1.BackupPhaseRunning || backupConditionReason(updated) != "DispatchRecorded" {
		t.Fatalf("status after status loss = %#v", updated.Status)
	}
	if calls.generate.Load() != 1 {
		t.Fatalf("native generate calls after status loss = %d, want 1", calls.generate.Load())
	}
}

func TestRequiredDestinationOutcomeRequiresEverySelectedDestination(t *testing.T) {
	phase, reason, _, success := requiredDestinationOutcome(
		[]string{"backup.local", "remote"},
		[]haclient.BackupDestinationResult{
			{AgentID: "backup.local", Protected: true},
			{AgentID: "remote", Error: haclient.BackupErrorSourceUnavailable},
		},
	)
	if phase != hav1alpha1.BackupPhasePartiallySucceeded || reason != "RequiredDestinationFailed" || success {
		t.Fatalf("partial destination result = (%s, %q, %t)", phase, reason, success)
	}

	phase, reason, _, success = requiredDestinationOutcome(
		[]string{"backup.local", "remote"},
		[]haclient.BackupDestinationResult{
			{AgentID: "backup.local", Protected: true}, {AgentID: "remote", Protected: true},
		},
	)
	if phase != hav1alpha1.BackupPhaseSucceeded || reason != "RequiredDestinationsSucceeded" || !success {
		t.Fatalf("full destination result = (%s, %q, %t)", phase, reason, success)
	}
	_, _, _, success = requiredDestinationOutcome([]string{"backup.local"},
		[]haclient.BackupDestinationResult{{AgentID: "backup.local", Protected: false}})
	if success {
		t.Fatal("an unprotected archive was reported as a successful protected backup")
	}
}

func TestHomeAssistantBackupRecoversAcceptedJobAndPollsQualifiedContainerArchive(t *testing.T) {
	protection := staticBackupKeyProtection{value: BackupKeyProtection{Password: "synthetic-key", Verified: true}}
	r, operation, calls := newBackupOperationReconciler(t, protection)
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated := getBackupOperation(t, r, operation)
	updated.Status = hav1alpha1.HomeAssistantBackupStatus{}
	if err := r.Status().Update(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated = getBackupOperation(t, r, operation)
	if updated.Status.BackupJobID != "native-job" {
		t.Fatal("accepted job identity was not recovered from evidence")
	}
	target := &hav1.HomeAssistant{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: operation.Namespace, Name: "home"}, target); err != nil {
		t.Fatal(err)
	}
	haClient := newHAClientForHA(target, r.NewHAClient)
	if _, err := r.observeBackup(ctx, updated, target, haClient, "test-token"); err != nil {
		t.Fatal(err)
	}
	updated = getBackupOperation(t, r, operation)
	if updated.Status.Phase != hav1alpha1.BackupPhaseSucceeded || updated.Status.BackupID != "native-job" {
		t.Fatal("qualified Container archive was not confirmed by native details")
	}
	if calls.generate.Load() != 1 {
		t.Fatal("recovered operation dispatched another native command")
	}
	root, err := r.store().GetRoot(ctx, operation.Namespace, target.UID)
	if err != nil {
		t.Fatal(err)
	}
	if root.ActivePermit != nil {
		t.Fatal("confirmed backup left the instance writer permit held")
	}
	detailsCalls := calls.details.Load()
	updated.Status = hav1alpha1.HomeAssistantBackupStatus{}
	if err := r.Status().Update(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	if getBackupOperation(t, r, operation).Status.Phase != hav1alpha1.BackupPhaseSucceeded {
		t.Fatal("completed result was not reconstructed from durable evidence")
	}
	if calls.generate.Load() != 1 || calls.details.Load() != detailsCalls {
		t.Fatal("recorded terminal result caused another dispatch or unnecessary archive lookup")
	}
}

func TestBackupEventMatchesOnlyTheAcceptedNativeJob(t *testing.T) {
	operation := &hav1alpha1.HomeAssistantBackup{
		Status: hav1alpha1.HomeAssistantBackupStatus{BackupJobID: "native-job"},
	}
	if !backupEventMatches(operation, haclient.BackupEvent{BackupJobID: "native-job"}) {
		t.Fatal("event for accepted job was not correlated")
	}
	if backupEventMatches(operation, haclient.BackupEvent{BackupJobID: "other-job"}) {
		t.Fatal("event for another job was correlated")
	}
	if backupEventMatches(operation, haclient.BackupEvent{BackupID: "archive-without-job"}) {
		t.Fatal("event without the native job identifier was correlated")
	}
}

func TestHomeAssistantBackupDoesNotCompleteVisibleArchiveWhileNativeManagerIsBusy(t *testing.T) {
	protection := staticBackupKeyProtection{value: BackupKeyProtection{Password: "synthetic-key", Verified: true}}
	r, operation, calls := newBackupOperationReconciler(t, protection,
		backupServerOptions{nativeState: "create_backup"})
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	updated := getBackupOperation(t, r, operation)
	target := &hav1.HomeAssistant{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: operation.Namespace, Name: "home"}, target); err != nil {
		t.Fatal(err)
	}
	haClient := newHAClientForHA(target, r.NewHAClient)
	if _, err := r.observeBackup(ctx, updated, target, haClient, "test-token"); err != nil {
		t.Fatal(err)
	}
	root, err := r.store().GetRoot(ctx, operation.Namespace, target.UID)
	if err != nil {
		t.Fatal(err)
	}
	if root.ActivePermit == nil || calls.details.Load() != 0 {
		t.Fatal("pending native distribution was treated as completed work")
	}
	if getBackupOperation(t, r, operation).Status.Phase != hav1alpha1.BackupPhaseRunning {
		t.Fatal("pending native distribution was reported terminal")
	}
}

type backupOperationCalls struct {
	config   atomic.Int32
	agents   atomic.Int32
	generate atomic.Int32
	details  atomic.Int32
}

type backupServerOptions struct {
	operationTargetUID   string
	agentIDs             []string
	paused               bool
	unsupportedGenerate  bool
	dropGenerateResponse bool
	nativeState          string
}

func newBackupOperationReconciler(
	t *testing.T,
	protection BackupKeyProtectionProvider,
	options ...backupServerOptions,
) (*HomeAssistantBackupReconciler, *hav1alpha1.HomeAssistantBackup, *backupOperationCalls) {
	t.Helper()
	option := backupServerOptions{}
	if len(options) > 0 {
		option = options[0]
	}
	if option.operationTargetUID == "" {
		option.operationTargetUID = "target-uid"
	}
	if len(option.agentIDs) == 0 {
		option.agentIDs = []string{"backup.local"}
	}
	if option.nativeState == "" {
		option.nativeState = "idle"
	}
	calls := &backupOperationCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
		var ignored map[string]any
		if conn.ReadJSON(&ignored) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
		var command map[string]any
		if conn.ReadJSON(&command) != nil {
			return
		}
		response := map[string]any{"id": command["id"], "type": "result", "success": true}
		switch command["type"] {
		case "backup/config/info":
			calls.config.Add(1)
			response["result"] = map[string]any{
				"config": map[string]any{
					"create_backup": map[string]any{"agent_ids": option.agentIDs, "include_database": true},
				},
			}
		case "backup/agents/info":
			calls.agents.Add(1)
			response["result"] = map[string]any{
				"agents": []map[string]string{{"agent_id": "backup.local", "name": "Local"}},
			}
		case "backup/generate":
			calls.generate.Add(1)
			if option.dropGenerateResponse {
				return
			}
			if option.unsupportedGenerate {
				response["success"] = false
				response["error"] = map[string]string{"code": "unknown_command"}
				break
			}
			response["result"] = map[string]any{"backup_job_id": "native-job"}
		case "backup/details":
			calls.details.Add(1)
			response["result"] = map[string]any{
				"backup": map[string]any{
					"backup_id": command["backup_id"],
					"agents":    map[string]any{"backup.local": map[string]any{"protected": true}},
				},
			}
		case "backup/info":
			response["result"] = map[string]any{"state": option.nativeState}
		default:
			response["success"] = false
			response["error"] = map[string]string{"code": "unknown_command"}
		}
		_ = conn.WriteJSON(response)
	}))
	t.Cleanup(server.Close)

	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default", UID: "target-uid"}}
	if option.paused {
		target.Spec.Synchronization = &hav1.SynchronizationSpec{Paused: true}
	}
	operation := &hav1alpha1.HomeAssistantBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", UID: "operation-uid"},
		Spec: hav1alpha1.HomeAssistantBackupSpec{
			HomeAssistantRef: hav1alpha1.HomeAssistantTargetReference{Name: "home", UID: option.operationTargetUID},
			AgentIDs:         option.agentIDs,
		},
	}
	token := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "home-homeassistant-api-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("test-token")},
	}
	_, sts, pod := backupCapabilityFixture(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(target, operation).
		WithObjects(target, operation, token, sts, pod).Build()
	catalog, err := haclient.NewCapabilityCatalog([]haclient.QualificationRecord{{
		ImageDigest: qualifiedBackupDigest, Capabilities: map[haclient.BackupCapability]bool{haclient.CapabilityBackup: true},
		JobIDIsArchiveID: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &HomeAssistantBackupReconciler{
		Client: c, APIReader: c, Scheme: scheme, KeyProtection: protection,
		CapabilityGate: NativeBackupCapabilityGate{Reader: c, Catalog: catalog},
		NewHAClient: func(string) *haclient.Client {
			return haclient.NewClient("ws" + strings.TrimPrefix(server.URL, "http"))
		},
	}, operation, calls
}

func getBackupOperation(
	t *testing.T,
	reconciler *HomeAssistantBackupReconciler,
	operation *hav1alpha1.HomeAssistantBackup,
) *hav1alpha1.HomeAssistantBackup {
	t.Helper()
	updated := &hav1alpha1.HomeAssistantBackup{}
	if err := reconciler.Get(context.Background(), requestFor(operation).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	return updated
}

func backupConditionReason(operation *hav1alpha1.HomeAssistantBackup) string {
	if len(operation.Status.Conditions) == 0 {
		return ""
	}
	return operation.Status.Conditions[len(operation.Status.Conditions)-1].Reason
}

func requestFor(operation *hav1alpha1.HomeAssistantBackup) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name}}
}
