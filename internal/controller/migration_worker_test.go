package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestMigrationWorkerJobIsBoundAndDoesNotRetry(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ha.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube, op := requestTestObjects(t)
	r, err := acceptBackupRequest(context.Background(), kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	t.Setenv(migrationWorkerImagePullSecretsEnv, `[{"name":"private-registry"}]`)
	job, err := buildMigrationWorkerJob(scheme, op, *r, "operator:test", "worker-sa", "archive")
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 || job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever || job.Spec.ActiveDeadlineSeconds == nil || job.Spec.TTLSecondsAfterFinished != nil {
		t.Fatal("unsafe job retry or lifecycle")
	}
	if owner := metav1.GetControllerOf(job); owner == nil || owner.UID != op.UID {
		t.Fatal("worker job not owned by migration")
	}
	if job.Spec.Template.Spec.ServiceAccountName != "worker-sa" || len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatal("worker identity missing")
	}
	if len(job.Spec.Template.Spec.ImagePullSecrets) != 1 || job.Spec.Template.Spec.ImagePullSecrets[0].Name != "private-registry" {
		t.Fatal("worker image pull secret missing")
	}
	container := job.Spec.Template.Spec.Containers[0]
	if len(container.Resources.Requests) == 0 || len(container.Resources.Limits) == 0 || container.SecurityContext == nil || container.SecurityContext.ReadOnlyRootFilesystem == nil || !*container.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatal("missing worker resource or security limits")
	}
	encoded, err := json.Marshal(job)
	if err != nil || strings.Contains(string(encoded), "sensitive-token") || strings.Contains(string(encoded), "sensitive-key") {
		t.Fatal("worker specification leaked credentials")
	}
	again, err := buildMigrationWorkerJob(scheme, op, *r, "operator:test", "worker-sa", "archive")
	if err != nil || again.Name != job.Name {
		t.Fatal("job identity is not deterministic")
	}
	r.OperationUID = "foreign"
	if _, err := buildMigrationWorkerJob(scheme, op, *r, "operator:test", "worker-sa", "archive"); err == nil {
		t.Fatal("foreign request accepted")
	}
}

func TestMigrationArchivePVCCreateOnlyAndOwned(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	r, err := acceptBackupRequest(ctx, kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if err := ensureMigrationArchivePVC(ctx, kube, kube.Scheme(), op, *r, resource.MustParse("1Gi"), nil); err != nil {
		t.Fatal(err)
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: "archive"}, pvc); err != nil {
		t.Fatal(err)
	}
	if owner := metav1.GetControllerOf(pvc); owner == nil || owner.UID != op.UID {
		t.Fatal("archive PVC not owned")
	}
	if err := ensureMigrationArchivePVC(ctx, kube, kube.Scheme(), op, *r, resource.MustParse("1Gi"), nil); err != nil {
		t.Fatal(err)
	}
	foreign := pvc.DeepCopy()
	foreign.OwnerReferences = nil
	if err := kube.Update(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := ensureMigrationArchivePVC(ctx, kube, kube.Scheme(), op, *r, resource.MustParse("1Gi"), nil); err == nil {
		t.Fatal("adopted foreign PVC")
	}
}

func TestMigrationWorkerAttemptClaimNeverReopens(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	r, err := acceptBackupRequest(ctx, kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if err := claimMigrationAttempt(ctx, loseMigrationUpdateResponse{kube}, op, *r); err == nil {
		t.Fatal("uncertain claim must not authorize a worker")
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if len(op.Spec.ExecutionAttempts) != 1 {
		t.Fatal("attempt marker not durable")
	}
	if err := claimMigrationAttempt(ctx, kube, op, *r); err == nil {
		t.Fatal("lost response reopened the attempt")
	}
}

func TestMigrationWorkerCreationRequiresIsolationAndNeverRecreates(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	r, err := acceptBackupRequest(ctx, kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	scheme := kube.Scheme()
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "archive", Namespace: op.Namespace, UID: "archive-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: ha.GroupVersion.String(), Kind: "HomeAssistantMigration", Name: op.Name, UID: op.UID, Controller: workerBool(true)}}}}
	if err := kube.Create(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	if err := startMigrationWorker(ctx, kube, scheme, op, *r, "operator:test", "worker-sa", "archive", false); err == nil {
		t.Fatal("unisolated worker accepted")
	}
	if err := startMigrationWorker(ctx, kube, scheme, op, *r, "operator:test", "worker-sa", "archive", true); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	jobs := &batchv1.JobList{}
	if err := kube.List(ctx, jobs, client.InNamespace(op.Namespace)); err != nil || len(jobs.Items) != 1 {
		t.Fatalf("worker jobs: %v %v", len(jobs.Items), err)
	}
	if err := kube.Delete(ctx, &jobs.Items[0]); err != nil {
		t.Fatal(err)
	}
	if err := startMigrationWorker(ctx, kube, scheme, op, *r, "operator:test", "worker-sa", "archive", true); err == nil {
		t.Fatal("attempt reopened")
	}
	jobs = &batchv1.JobList{}
	if err := kube.List(ctx, jobs, client.InNamespace(op.Namespace)); err != nil || len(jobs.Items) != 0 {
		t.Fatal("lost job was recreated")
	}
}

func workerBool(b bool) *bool { return &b }
