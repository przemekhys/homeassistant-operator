package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMigrationModelReceiptStates(t *testing.T) {
	req := ha.MigrationExecutionRequest{Stage: "backup", RequestID: "request", OperationUID: "operation", SourceID: "source", Target: ha.MigrationTarget{UID: "target"}}
	for _, tc := range []struct {
		name        string
		observation migrationStageObservation
		phase       string
	}{
		{"active dispatch", migrationStageObservation{Phase: "Dispatching", Active: true, OperationUID: "operation", RequestID: "request", Stage: "backup", SourceID: "source", TargetUID: "target"}, "BackupInProgress"},
		{"orphan dispatch", migrationStageObservation{Phase: "Dispatching", OperationUID: "operation", RequestID: "request", Stage: "backup", SourceID: "source", TargetUID: "target"}, "Unknown"},
		{"accepted job", migrationStageObservation{Phase: "Accepted", JobID: "job", OperationUID: "operation", RequestID: "request", Stage: "backup", SourceID: "source", TargetUID: "target"}, "BackupInProgress"},
		{"accepted without job", migrationStageObservation{Phase: "Accepted", OperationUID: "operation", RequestID: "request", Stage: "backup", SourceID: "source", TargetUID: "target"}, "Unknown"},
		{"foreign receipt", migrationStageObservation{Phase: "Accepted", JobID: "job", OperationUID: "other", RequestID: "request", Stage: "backup", SourceID: "source", TargetUID: "target"}, "Unknown"},
		{"verified", migrationStageObservation{Phase: "Verified", JobID: "job", Verified: true, OperationUID: "operation", RequestID: "request", Stage: "backup", SourceID: "source", TargetUID: "target"}, "BackupReady"},
		{"unverified result", migrationStageObservation{Phase: "Verified", JobID: "job", OperationUID: "operation", RequestID: "request", Stage: "backup", SourceID: "source", TargetUID: "target"}, "Unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			phase, _ := migrationObservedStage(req, tc.observation)
			if phase != tc.phase {
				t.Fatalf("phase=%s want %s", phase, tc.phase)
			}
		})
	}
}

func TestMigrationModelIsUsedByInjectedReconciler(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	r, err := acceptBackupRequest(ctx, kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	op.Finalizers = []string{migrationOperationFinalizer}
	op.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	kube = fake.NewClientBuilder().WithScheme(kube.Scheme()).WithStatusSubresource(op).WithObjects(op).Build()
	executor := &modelTestExecutor{observations: map[string]migrationStageObservation{"backup": {Phase: "Accepted", JobID: "job", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: "backup", SourceID: op.Spec.SourceID, TargetUID: op.Spec.Target.UID}}}
	reconciler := &HomeAssistantMigrationReconciler{Client: kube, Scheme: kube.Scheme(), stageObserver: executor}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(op)}); err != nil {
		t.Fatal(err)
	}
	current := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != "BackupInProgress" {
		t.Fatalf("phase = %s", current.Status.Phase)
	}
}

func TestMigrationModelRequiresResumeAndManualCutover(t *testing.T) {
	archive := "sha256:" + strings.Repeat("a", 64)
	verification := "sha256:" + strings.Repeat("b", 64)
	scope := "sha256:" + strings.Repeat("d", 64)
	result := "sha256:" + strings.Repeat("c", 64)
	op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{UID: "operation"}, Spec: ha.HomeAssistantMigrationSpec{ExecutionProtocol: "v1", SourceID: "source", Target: ha.MigrationTarget{UID: "target"}}}
	executor := &modelTestExecutor{observations: map[string]migrationStageObservation{}}
	for _, stage := range []string{"backup", "upload", "restore"} {
		r := ha.MigrationExecutionRequest{Stage: stage, RequestID: stage + "-request", OperationUID: "operation", SourceID: "source", Target: op.Spec.Target, ArchiveDigest: archive, ArchiveRef: &ha.MigrationObjectReference{Name: "descriptor", UID: "archive-uid"}, ArchivePVCUID: "pvc-uid", ScopeDigest: scope, VerificationDigest: verification}
		op.Spec.ExecutionRequests = append(op.Spec.ExecutionRequests, r)
		op.Spec.ExecutionAttempts = append(op.Spec.ExecutionAttempts, ha.MigrationExecutionAttempt{Stage: stage, RequestID: r.RequestID})
		executor.observations[stage] = migrationStageObservation{Phase: "Verified", Verified: true, JobID: "job", OperationUID: "operation", RequestID: r.RequestID, Stage: stage, SourceID: "source", TargetUID: "target", ArchiveDigest: archive, ArchiveRef: r.ArchiveRef, ArchivePVCUID: "pvc-uid", ScopeDigest: scope, VerificationDigest: verification, ResultDigest: result, AccessVerified: true}
	}
	assert := func(want string) {
		t.Helper()
		got, _, err := migrationModelPhase(context.Background(), op, executor)
		if err != nil || got != want {
			t.Fatalf("phase=%s want=%s err=%v", got, want, err)
		}
	}
	assert("AwaitingResumeApproval")
	op.Spec.Approvals = append(op.Spec.Approvals, ha.MigrationApproval{ID: "resume", Approved: true, TargetUID: "target", ArchiveDigest: archive, VerificationDigest: verification, Digest: result})
	assert("AwaitingManualCutover")
	op.Spec.Approvals = append(op.Spec.Approvals, ha.MigrationApproval{ID: "manual-cutover", Approved: true, TargetUID: "target", ArchiveDigest: archive, VerificationDigest: verification, Digest: "confirmed"})
	assert("Completed")
	changed := executor.observations["restore"]
	changed.AccessVerified = false
	executor.observations["restore"] = changed
	assert("AwaitingVerification")
}

type modelTestExecutor struct {
	observations map[string]migrationStageObservation
}

type modelFailingObserver struct{}

func (modelFailingObserver) Observe(context.Context, *ha.HomeAssistantMigration, ha.MigrationExecutionRequest) (migrationStageObservation, error) {
	return migrationStageObservation{}, errors.New("sensitive external response")
}

func TestMigrationModelDoesNotKeepCompletedWhenObservationFails(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	r, err := acceptBackupRequest(ctx, kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	op.Finalizers = []string{migrationOperationFinalizer}
	op.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	op.Status.Phase = "Completed"
	kube = fake.NewClientBuilder().WithScheme(kube.Scheme()).WithStatusSubresource(op).WithObjects(op).Build()
	reconciler := &HomeAssistantMigrationReconciler{Client: kube, Scheme: kube.Scheme(), stageObserver: modelFailingObserver{}}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(op)}); err != nil {
		t.Fatal(err)
	}
	stored := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != "Unknown" {
		t.Fatalf("stale phase = %s", stored.Status.Phase)
	}
	for _, condition := range stored.Status.Conditions {
		if strings.Contains(condition.Message, "sensitive") {
			t.Fatal("leaked observation error")
		}
	}
}

type modelTestStarter struct {
	modelTestExecutor
	kube        client.Client
	starts      int
	preparation migrationRequestObservation
	stopped     bool
}

func (e *modelTestStarter) Prepare(_ context.Context, _ *ha.HomeAssistantMigration, _ string) (migrationRequestObservation, bool, error) {
	return e.preparation, e.stopped, nil
}

func (e *modelTestStarter) Start(ctx context.Context, op *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest) error {
	e.starts++
	updated := op.DeepCopy()
	updated.Spec.ExecutionAttempts = append(updated.Spec.ExecutionAttempts, ha.MigrationExecutionAttempt{Stage: request.Stage, RequestID: request.RequestID})
	return e.kube.Update(ctx, updated)
}

func TestMigrationModelStartsOnlyUnattemptedRequest(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	r, err := acceptBackupRequest(ctx, kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	op.Finalizers = []string{migrationOperationFinalizer}
	kube = fake.NewClientBuilder().WithScheme(kube.Scheme()).WithStatusSubresource(op).WithObjects(op).Build()
	executor := &modelTestStarter{kube: kube, modelTestExecutor: modelTestExecutor{observations: map[string]migrationStageObservation{"backup": {Phase: "Dispatching", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: "backup", SourceID: op.Spec.SourceID, TargetUID: op.Spec.Target.UID}}}}
	reconciler := &HomeAssistantMigrationReconciler{Client: kube, Scheme: kube.Scheme(), stageObserver: executor}
	for i := 0; i < 2; i++ {
		if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(op)}); err != nil {
			t.Fatal(err)
		}
	}
	if executor.starts != 1 {
		t.Fatalf("started %d times", executor.starts)
	}
	stored := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != "Unknown" {
		t.Fatalf("orphan dispatch phase = %s", stored.Status.Phase)
	}
}

func TestMigrationModelAcceptsApprovedBackupBeforeExecution(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	op.Finalizers = []string{migrationOperationFinalizer}
	if err := kube.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	executor := &modelTestExecutor{observations: map[string]migrationStageObservation{}}
	reconciler := &HomeAssistantMigrationReconciler{Client: kube, Scheme: kube.Scheme(), stageObserver: executor}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(op)}); err != nil {
		t.Fatal(err)
	}
	stored := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Spec.ExecutionRequests) != 1 || stored.Spec.ExecutionRequests[0].Stage != "backup" || len(stored.Spec.ExecutionAttempts) != 0 {
		t.Fatalf("unexpected execution records: %#v", stored.Spec)
	}
}

func TestMigrationModelCompleteWorkflow(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	op.Finalizers = []string{migrationOperationFinalizer}
	if err := kube.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	token, recovery := &corev1.Secret{}, &corev1.Secret{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: "token"}, token); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: "key"}, recovery); err != nil {
		t.Fatal(err)
	}
	kube = fake.NewClientBuilder().WithScheme(kube.Scheme()).WithStatusSubresource(op).WithObjects(op, token, recovery).Build()
	archive, scope, result := "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64), "sha256:"+strings.Repeat("e", 64)
	vd, err := migrationprotocol.VerificationDigest(op.Spec.Verification)
	if err != nil {
		t.Fatal(err)
	}
	executor := &modelTestStarter{kube: kube, modelTestExecutor: modelTestExecutor{observations: map[string]migrationStageObservation{}}, preparation: migrationRequestObservation{ArchiveRef: &ha.MigrationObjectReference{Name: "descriptor", UID: "archive-uid"}, ArchiveDigest: archive, ArchivePVCUID: "pvc-uid"}}
	reconciler := &HomeAssistantMigrationReconciler{Client: kube, Scheme: kube.Scheme(), stageObserver: executor}
	key := client.ObjectKeyFromObject(op)
	reconcile := func() *ha.HomeAssistantMigration {
		t.Helper()
		if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		current := &ha.HomeAssistantMigration{}
		if err := kube.Get(ctx, key, current); err != nil {
			t.Fatal(err)
		}
		return current
	}
	current := reconcile()
	if len(current.Spec.ExecutionRequests) != 1 {
		t.Fatal("backup not accepted")
	}
	current = reconcile()
	if len(current.Spec.ExecutionAttempts) != 1 {
		t.Fatal("backup not started")
	}
	backup := current.Spec.ExecutionRequests[0]
	observation := migrationStageObservation{Phase: "Accepted", JobID: "job", OperationUID: string(op.UID), RequestID: backup.RequestID, Stage: "backup", SourceID: op.Spec.SourceID, TargetUID: op.Spec.Target.UID}
	executor.observations["backup"] = observation
	if current = reconcile(); current.Status.Phase != "BackupInProgress" {
		t.Fatal(current.Status.Phase)
	}
	observation.Phase, observation.Verified, observation.ArchiveDigest, observation.VerificationDigest = "Verified", true, archive, vd
	observation.ScopeDigest = backup.ScopeDigest
	observation.ArchiveRef, observation.ArchivePVCUID = executor.preparation.ArchiveRef, executor.preparation.ArchivePVCUID
	executor.observations["backup"] = observation
	if current = reconcile(); current.Status.Phase != "BackupReady" {
		t.Fatal(current.Status.Phase)
	}
	current.Spec.Archive = &ha.MigrationArchive{BackupID: "external-backup", AgentID: "backup.local", Digest: archive}
	current.Spec.RestoreScopeDigest = scope
	current.Generation++
	current.Spec.Approvals = append(current.Spec.Approvals, ha.MigrationApproval{ID: "restore", Approved: true, Digest: "decision", TargetUID: op.Spec.Target.UID, ScopeDigest: scope, ArchiveDigest: archive, VerificationDigest: vd}, ha.MigrationApproval{ID: "source-stop", Approved: true, Digest: op.Spec.ReportID, TargetUID: op.Spec.Target.UID})
	if err := kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	current = reconcile()
	if current.Status.Phase != "AwaitingSourceStop" {
		t.Fatalf("source not stopped: %s", current.Status.Phase)
	}
	executor.stopped = true
	for _, stage := range []string{"upload", "restore"} {
		current = reconcile()
		var request ha.MigrationExecutionRequest
		for _, r := range current.Spec.ExecutionRequests {
			if r.Stage == stage {
				request = r
			}
		}
		if request.RequestID == "" {
			t.Fatalf("%s not accepted", stage)
		}
		current = reconcile()
		executor.observations[stage] = migrationStageObservation{Phase: "Verified", Verified: true, JobID: "job", OperationUID: string(op.UID), RequestID: request.RequestID, Stage: stage, SourceID: op.Spec.SourceID, TargetUID: op.Spec.Target.UID, ArchiveDigest: archive, ScopeDigest: scope, VerificationDigest: vd, ResultDigest: result, AccessVerified: true}
	}
	current = reconcile()
	if current.Status.Phase != "AwaitingResumeApproval" {
		t.Fatal(current.Status.Phase)
	}
	current.Spec.Approvals = append(current.Spec.Approvals, ha.MigrationApproval{ID: "resume", Approved: true, Digest: result, TargetUID: op.Spec.Target.UID, ArchiveDigest: archive, VerificationDigest: vd})
	current.Generation++
	if err := kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	current = reconcile()
	if current.Status.Phase != "AwaitingManualCutover" {
		t.Fatal(current.Status.Phase)
	}
	current.Spec.Approvals = append(current.Spec.Approvals, ha.MigrationApproval{ID: "manual-cutover", Approved: true, Digest: "confirmed", TargetUID: op.Spec.Target.UID, ArchiveDigest: archive, VerificationDigest: vd})
	if err := kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	current = reconcile()
	if current.Status.Phase != "Completed" {
		t.Fatal(current.Status.Phase)
	}
	current.Status = ha.HomeAssistantMigrationStatus{}
	if err := kube.Status().Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	reconciler = &HomeAssistantMigrationReconciler{Client: kube, Scheme: kube.Scheme(), stageObserver: executor}
	current = reconcile()
	if current.Status.Phase != "Completed" {
		t.Fatalf("phase after restart and status loss: %s", current.Status.Phase)
	}
	if executor.starts != 3 {
		t.Fatalf("started %d stages", executor.starts)
	}
}

func (e *modelTestExecutor) Observe(_ context.Context, _ *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest) (migrationStageObservation, error) {
	return e.observations[request.Stage], nil
}

func TestMigrationModelReconstructsProgress(t *testing.T) {
	op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{UID: "operation"}, Spec: ha.HomeAssistantMigrationSpec{ExecutionProtocol: "v1", SourceID: "source", Target: ha.MigrationTarget{UID: "target"}}}
	request := ha.MigrationExecutionRequest{Stage: "backup", RequestID: "request", OperationUID: "operation", SourceID: "source", Target: op.Spec.Target}
	op.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{request}
	op.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: "request"}}
	executor := &modelTestExecutor{observations: map[string]migrationStageObservation{"backup": {Phase: "Accepted", JobID: "job", OperationUID: "operation", RequestID: "request", Stage: "backup", SourceID: "source", TargetUID: "target"}}}
	for _, status := range []string{"", "Blocked", "Unknown"} {
		op.Status.Phase = status
		phase, _, err := migrationModelPhase(context.Background(), op, executor)
		if err != nil || phase != "BackupInProgress" {
			t.Fatalf("phase=%s err=%v", phase, err)
		}
	}
}

func TestMigrationModelRequiresEveryStage(t *testing.T) {
	op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{UID: "operation"}, Spec: ha.HomeAssistantMigrationSpec{ExecutionProtocol: "v1", SourceID: "source", Target: ha.MigrationTarget{UID: "target"}}}
	executor := &modelTestExecutor{observations: map[string]migrationStageObservation{}}
	archive := "sha256:" + strings.Repeat("a", 64)
	verification := "sha256:" + strings.Repeat("b", 64)
	scope := "sha256:" + strings.Repeat("d", 64)
	for _, stage := range []string{"backup", "upload", "restore"} {
		request := ha.MigrationExecutionRequest{Stage: stage, RequestID: stage + "-request", OperationUID: "operation", SourceID: "source", Target: op.Spec.Target, ArchiveDigest: archive, ArchiveRef: &ha.MigrationObjectReference{Name: "descriptor", UID: "archive-uid"}, ArchivePVCUID: "pvc-uid", ScopeDigest: scope, VerificationDigest: verification}
		op.Spec.ExecutionRequests = append(op.Spec.ExecutionRequests, request)
		op.Spec.ExecutionAttempts = append(op.Spec.ExecutionAttempts, ha.MigrationExecutionAttempt{Stage: stage, RequestID: request.RequestID})
		executor.observations[stage] = migrationStageObservation{Phase: "Verified", Verified: true, JobID: "job", OperationUID: "operation", RequestID: request.RequestID, Stage: stage, SourceID: "source", TargetUID: "target", ArchiveDigest: archive, ArchiveRef: request.ArchiveRef, ArchivePVCUID: "pvc-uid", ScopeDigest: scope, VerificationDigest: verification}
		phase, _, err := migrationModelPhase(context.Background(), op, executor)
		want := map[string]string{"backup": "BackupReady", "upload": "RestoreInProgress", "restore": "AwaitingVerification"}[stage]
		if err != nil || phase != want {
			t.Fatalf("%s: phase=%s err=%v", stage, phase, err)
		}
	}
	delete(executor.observations, "upload")
	phase, _, err := migrationModelPhase(context.Background(), op, executor)
	if err != nil || phase != "Unknown" {
		t.Fatalf("missing historical observation: phase=%s err=%v", phase, err)
	}
	backup := executor.observations["backup"]
	backup.ArchiveRef = nil
	executor.observations["backup"] = backup
	phase, _, err = migrationModelPhase(context.Background(), op, executor)
	if err != nil || phase != "Unknown" {
		t.Fatalf("missing archive descriptor: phase=%s err=%v", phase, err)
	}
}
