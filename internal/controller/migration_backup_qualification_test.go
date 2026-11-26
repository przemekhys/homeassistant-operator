package controller

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

//nolint:gocyclo // The ordered fault scenario keeps each safety assertion explicit.
func TestBackupQualificationContinuationSerializesWithoutDispatch(t *testing.T) {
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
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	job.Status.Succeeded = 1
	if err := kube.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "archive", Namespace: op.Namespace, UID: "archive-uid"}}
	if err := controllerutil.SetControllerReference(op, pvc, kube.Scheme()); err != nil {
		t.Fatal(err)
	}
	if err := kube.Create(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	receipt := migration.BackupDispatchReceipt{RequestID: r.RequestID, OperationUID: string(op.UID), TargetUID: r.Target.UID, ReportID: r.ReportID, ScopeDigest: r.ScopeDigest, Phase: "Accepted", JobID: "exact-job"}
	b, _ := json.Marshal(receipt)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: migration.WorkerReceiptName(r.RequestID), Namespace: op.Namespace}, Data: map[string]string{"receipt.json": string(b)}}
	if err := controllerutil.SetControllerReference(op, cm, kube.Scheme()); err != nil {
		t.Fatal(err)
	}
	if err := kube.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	o := &migrationBackupQualification{migrationWorkerObserver: migrationWorkerObserver{reader: kube}, kube: kube, scheme: kube.Scheme(), options: BackupQualificationOptions{Image: "operator:test", ServiceAccount: "worker", ArchiveSize: resource.MustParse("1Gi"), VerifyIsolation: func(context.Context, *ha.HomeAssistantMigration) error { return nil }}}
	// An orphaned local Pod blocks mounting the archive, even though the Job
	// already appears terminal. No second producer may race its termination.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "still-running", Namespace: op.Namespace, Labels: job.Labels}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := kube.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Observe(ctx, op, *r); err != nil {
		t.Fatal(err)
	}
	jobs := &batchv1.JobList{}
	if err := kube.List(ctx, jobs); err != nil || len(jobs.Items) != 1 {
		t.Fatal("observer raced an active Pod", err)
	}
	if err := kube.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := o.Observe(ctx, op, *r); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := kube.List(ctx, jobs); err != nil || len(jobs.Items) != 2 {
		t.Fatal("concurrent reconcile duplicated observation", err)
	}
	for _, j := range jobs.Items {
		if j.Name == job.Name {
			continue
		}
		args := strings.Join(j.Spec.Template.Spec.Containers[0].Args, " ")
		if !strings.Contains(args, "--mode observe-backup") || j.Spec.BackoffLimit == nil || *j.Spec.BackoffLimit != 0 || j.Spec.TTLSecondsAfterFinished != nil {
			t.Fatal("unsafe continuation Job")
		}
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if len(op.Spec.ExecutionAttempts) != 1 || len(op.Spec.ExecutionDispatches) != 1 {
		t.Fatal("observation changed dispatch history")
	}
	// Permanent local corruption is not retried and cannot become success.
	receipt.BlockedReason = "ArchiveValidationFailed"
	b, _ = json.Marshal(receipt)
	cm.Data["receipt.json"] = string(b)
	if err := kube.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	observed, err := o.Observe(ctx, op, *r)
	if err != nil || observed.BlockedReason != "ArchiveValidationFailed" {
		t.Fatal("permanent corruption not blocked", err)
	}
	if phase, reason := migrationObservedStage(*r, observed); phase != "Blocked" || reason != "ArchiveValidationFailed" {
		t.Fatal(phase, reason)
	}
	receipt.BlockedReason = "BackupObservationUnavailable"
	b, _ = json.Marshal(receipt)
	cm.Data["receipt.json"] = string(b)
	if err := kube.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := kube.List(ctx, jobs); err != nil {
			t.Fatal(err)
		}
		for i := range jobs.Items {
			j := jobs.Items[i].DeepCopy()
			j.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Minute))
			if err := kube.Update(ctx, j); err != nil {
				t.Fatal(err)
			}
			j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
			j.Status.Failed = 1
			j.Status.Succeeded = 0
			if err := kube.Status().Update(ctx, j); err != nil {
				t.Fatal(err)
			}
		}
		observed, err = o.Observe(ctx, op, *r)
		if err != nil {
			t.Fatal(err)
		}
	}
	if observed.BlockedReason != "BackupObservationRetryExhausted" {
		t.Fatal("retry limit not enforced", observed.BlockedReason)
	}
	if err := kube.List(ctx, jobs); err != nil || len(jobs.Items) != 4 {
		t.Fatal("retry history changed", err)
	}
	e := migrationprotocol.Evidence{Version: "v1", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: r.Stage, SourceID: r.SourceID, TargetUID: r.Target.UID, JobID: "exact-job"}
	if _, err := o.startResolutionObservation(ctx, op, *r, e, jobs.Items, observed); err != nil {
		t.Fatal(err)
	}
	if _, err := o.startResolutionObservation(ctx, op, *r, e, jobs.Items, observed); err != nil {
		t.Fatal(err)
	}
	if err := kube.List(ctx, jobs); err != nil || len(jobs.Items) != 5 {
		t.Fatal("resolution did not create exactly one read-only job", err)
	}
	// An earlier resolution Job without a descriptor proof is historical data,
	// not a reason to suppress the bounded fresh validation required for
	// BackupReady.
	if _, err := o.startResolutionObservation(ctx, op, *r, e, jobs.Items, observed); err != nil {
		t.Fatal(err)
	}
	if err := kube.List(ctx, jobs); err != nil || len(jobs.Items) != 6 {
		t.Fatal("missing fresh resolution validation", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := o.startResolutionObservation(ctx, op, *r, e, jobs.Items, observed); err != nil {
			t.Fatal(err)
		}
		if err := kube.List(ctx, jobs); err != nil {
			t.Fatal(err)
		}
	}
	blocked, err := o.startResolutionObservation(ctx, op, *r, e, jobs.Items, observed)
	if err != nil || blocked.BlockedReason != "BackupObservationRetryExhausted" {
		t.Fatal("resolution validation retry limit not enforced", err)
	}
	for _, j := range jobs.Items {
		if strings.HasPrefix(j.Name, "migration-resolve-") && !strings.Contains(strings.Join(j.Spec.Template.Spec.Containers[0].Args, " "), "--mode observe-backup") {
			t.Fatal("manual observation can mutate HA")
		}
	}
}

func TestBackupValidationPodProofBindsDescriptorUID(t *testing.T) {
	want := migration.ArchiveValidationProof{Version: "v1", OperationUID: "op", RequestID: "request", DescriptorName: "descriptor", DescriptorUID: "descriptor-uid", Digest: "digest", PVCUID: "pvc-uid"}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	job := batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "observer", UID: "job-uid"}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "observer-pod", OwnerReferences: []metav1.OwnerReference{{Kind: "Job", Name: job.Name, UID: job.UID, Controller: &yes}}}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: "worker", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: string(b)}}}}}}
	if !validationPodProves(job, []corev1.Pod{pod}, want) {
		t.Fatal("completed validation proof rejected")
	}
	changed := want
	changed.DescriptorUID = "recreated-descriptor"
	if validationPodProves(job, []corev1.Pod{pod}, changed) {
		t.Fatal("old Job proved a recreated descriptor")
	}
	if validationPodProves(job, nil, want) {
		t.Fatal("missing Pod treated as validation")
	}
	pod.OwnerReferences[0].UID = "foreign-job"
	if validationPodProves(job, []corev1.Pod{pod}, want) {
		t.Fatal("foreign Pod proof accepted")
	}
}
