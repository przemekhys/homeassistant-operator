package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type testResolutionObserver struct {
	calls   int
	result  migrationStageObservation
	failure bool
}

func TestBackupResolutionObservationDoesNotGrantMutation(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	if err := batchv1.AddToScheme(kube.Scheme()); err != nil {
		t.Fatal(err)
	}
	r, err := acceptBackupRequest(ctx, kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	op.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	op.Spec.ExecutionDispatches = append([]ha.MigrationExecutionAttempt(nil), op.Spec.ExecutionAttempts...)
	if err := kube.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	job, err := buildMigrationWorkerJob(kube.Scheme(), op, *r, "operator:test", "worker", "archive")
	if err != nil {
		t.Fatal(err)
	}
	job.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Minute))
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	if err := kube.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "archive", Namespace: op.Namespace, UID: "pvc-uid"}}
	if err := controllerutil.SetControllerReference(op, pvc, kube.Scheme()); err != nil {
		t.Fatal(err)
	}
	if err := kube.Create(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	e := migrationprotocol.Evidence{Version: "v1", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: "backup", SourceID: r.SourceID, TargetUID: r.Target.UID, JobID: "actual-job"}
	calls := 0
	o := &migrationBackupQualification{migrationWorkerObserver: migrationWorkerObserver{reader: kube}, kube: kube, scheme: kube.Scheme(), options: BackupQualificationOptions{Image: "operator:test", ServiceAccount: "worker", VerifyIsolation: func(context.Context, *ha.HomeAssistantMigration) error { return nil }}, readResolution: func(_ context.Context, _ string, _ []byte, _ ha.MigrationExecutionRequest, _ migrationprotocol.Evidence) (migration.BackupResolutionObservation, error) {
		calls++
		return migration.BackupResolutionObservation{BackupID: "actual-job", AgentID: "backup.local", Size: 100}, nil
	}}
	result, err := o.ObserveExternalResult(ctx, op, *r, e)
	if err != nil || calls != 1 {
		t.Fatal("read-only result not observed", err)
	}
	if phase, _ := migrationObservedStage(*r, result); phase != "Unknown" {
		t.Fatal("source observation falsely confirmed archive", phase)
	}
	jobs := &batchv1.JobList{}
	if err := kube.List(ctx, jobs, client.InNamespace(op.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 2 {
		t.Fatal("read-only observation job not created")
	}
	for _, j := range jobs.Items {
		if j.Name == job.Name {
			continue
		}
		if !strings.Contains(strings.Join(j.Spec.Template.Spec.Containers[0].Args, " "), "--mode observe-backup") {
			t.Fatal("resolution created mutating worker")
		}
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if len(op.Spec.ExecutionAttempts) != 1 || len(op.Spec.ExecutionDispatches) != 1 {
		t.Fatal("resolution changed execution history")
	}
	op.Spec.ExecutionDispatches = nil
	if _, err := o.ObserveExternalResult(ctx, op, *r, e); err == nil || calls != 1 {
		t.Fatal("foreign dispatch accepted")
	}
}

func (o *testResolutionObserver) Observe(context.Context, *ha.HomeAssistantMigration, ha.MigrationExecutionRequest) (migrationStageObservation, error) {
	return migrationStageObservation{}, errors.New("lost response")
}
func (o *testResolutionObserver) ObserveExternalResult(_ context.Context, _ *ha.HomeAssistantMigration, _ ha.MigrationExecutionRequest, _ migrationprotocol.Evidence) (migrationStageObservation, error) {
	o.calls++
	if o.failure {
		return migrationStageObservation{}, errors.New("private error")
	}
	return o.result, nil
}

func TestResolutionEvidenceBindsImmutableConfigMap(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	r, err := acceptBackupRequest(ctx, kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	e := migrationprotocol.Evidence{Version: "v1", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: "backup", SourceID: r.SourceID, TargetUID: r.Target.UID, JobID: "job-id"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "external-evidence", Namespace: op.Namespace, UID: "evidence-uid"}, Immutable: &yes, Data: map[string]string{"result.json": string(b)}}
	if err := kube.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	resolution := ha.MigrationResolution{Version: "v1", Stage: "backup", RequestID: r.RequestID, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: cm.Name, UID: string(cm.UID), Key: "result.json"}}
	if _, err := readMigrationResolutionEvidence(ctx, kube, op, *r, resolution); err != nil {
		t.Fatal(err)
	}
	observer := &testResolutionObserver{result: migrationStageObservation{Phase: "Accepted", JobID: "job-id", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: r.Stage, SourceID: r.SourceID, TargetUID: r.Target.UID}}
	op.Spec.Resolutions = []ha.MigrationResolution{resolution}
	wrapped := resolutionAwareObserver{reader: kube, delegate: observer}
	result, err := wrapped.Observe(ctx, op, *r)
	if err != nil || observer.calls != 1 || result.JobID != "job-id" {
		t.Fatal("read-only observation did not resume", err)
	}
	observer.result.JobID = "foreign-job"
	if _, err := wrapped.Observe(ctx, op, *r); err == nil {
		t.Fatal("foreign observed job accepted")
	}
	observer.result.JobID = "job-id"
	observer.failure = true
	if _, err := wrapped.Observe(ctx, op, *r); err == nil || strings.Contains(err.Error(), "private error") {
		t.Fatal("unavailable result accepted or leaked")
	}
	foreign := resolution
	ref := *resolution.EvidenceRef
	ref.UID = "foreign"
	foreign.EvidenceRef = &ref
	if _, err := readMigrationResolutionEvidence(ctx, kube, op, *r, foreign); err == nil {
		t.Fatal("foreign UID accepted")
	}
	e.TargetUID = "foreign"
	b, _ = json.Marshal(e)
	cm.Data["result.json"] = string(b)
	if err := kube.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	if _, err := readMigrationResolutionEvidence(ctx, kube, op, *r, resolution); err == nil {
		t.Fatal("foreign target accepted")
	}
	before := observer.calls
	if _, err := wrapped.Observe(ctx, op, *r); err == nil || observer.calls != before {
		t.Fatal("foreign evidence reached observer")
	}
	op.Spec.Resolutions = append(op.Spec.Resolutions, resolution)
	if _, err := wrapped.Observe(ctx, op, *r); err == nil {
		t.Fatal("ambiguous set of observations accepted")
	}
}

func TestResolutionAbortBlocksAllStagesWithoutSuccess(t *testing.T) {
	op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{UID: "operation"}, Spec: ha.HomeAssistantMigrationSpec{ExecutionProtocol: "v1", SourceID: "source", Target: ha.MigrationTarget{UID: "target"}}}
	r := ha.MigrationExecutionRequest{Stage: "backup", RequestID: "request", OperationUID: "operation", SourceID: "source", Target: op.Spec.Target}
	op.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{r}
	op.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	op.Spec.Resolutions = []ha.MigrationResolution{{Version: "v1", Stage: "backup", RequestID: r.RequestID, Decision: "Abort"}}
	phase, reason, err := migrationModelPhase(context.Background(), op, &modelTestExecutor{})
	if err != nil || phase != "Blocked" || reason != "AbortRequested" {
		t.Fatalf("phase=%s reason=%s err=%v", phase, reason, err)
	}
}

func TestResolutionReconstructsVerifiedStageAfterStatusLoss(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	r, err := acceptBackupRequest(ctx, kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	op.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	archive := "sha256:" + strings.Repeat("d", 64)
	ref := &ha.MigrationObjectReference{Name: "descriptor", UID: "descriptor-uid"}
	e := migrationprotocol.Evidence{Version: "v1", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: r.Stage, SourceID: r.SourceID, TargetUID: r.Target.UID, JobID: "job-id", ArchiveDescriptorRef: ref, ArchiveDigest: archive}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "evidence", Namespace: op.Namespace, UID: "evidence-uid"}, Immutable: &yes, Data: map[string]string{"evidence.json": string(b)}}
	if err := kube.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	op.Spec.Resolutions = []ha.MigrationResolution{{Version: "v1", Stage: r.Stage, RequestID: r.RequestID, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: cm.Name, UID: string(cm.UID), Key: "evidence.json"}}}
	verified := migrationStageObservation{Phase: "Verified", Verified: true, JobID: "job-id", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: r.Stage, SourceID: r.SourceID, TargetUID: r.Target.UID, ArchiveDigest: archive, ArchiveRef: ref, ArchivePVCUID: "pvc-uid", ScopeDigest: r.ScopeDigest, VerificationDigest: r.VerificationDigest}
	for i := 0; i < 2; i++ {
		op.Status = ha.HomeAssistantMigrationStatus{}
		observer := &testResolutionObserver{result: verified}
		phase, reason, err := migrationModelPhase(ctx, op, resolutionAwareObserver{reader: kube, delegate: observer})
		if err != nil || phase != "BackupReady" || reason != "UploadRequestMissing" || observer.calls != 1 {
			t.Fatalf("restart %d phase=%s reason=%s err=%v", i, phase, reason, err)
		}
	}
	verified.ArchiveDigest = "sha256:" + strings.Repeat("e", 64)
	phase, _, err := migrationModelPhase(ctx, op, resolutionAwareObserver{reader: kube, delegate: &testResolutionObserver{result: verified}})
	if err == nil || phase != "Unknown" {
		t.Fatal("foreign archive qualified")
	}
}
