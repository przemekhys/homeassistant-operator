package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func cleanupTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		hav1alpha1.AddToScheme,
		corev1.AddToScheme,
		batchv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func cleanupTestMigration() *hav1alpha1.HomeAssistantMigration {
	return &hav1alpha1.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "migration",
			Namespace:  "default",
			UID:        "migration-uid",
			Finalizers: []string{migrationOperationFinalizer},
		},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			ExecutionRequests: []hav1alpha1.MigrationExecutionRequest{{
				RequestID: "sha256:" + strings.Repeat("a", 64),
				Stage:     "backup",
			}},
		},
	}
}

func cleanupTestJob(t *testing.T, migration *hav1alpha1.HomeAssistantMigration) *batchv1.Job {
	t.Helper()
	yes := true
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "migration-worker",
			Namespace: migration.Namespace,
			UID:       "job-uid",
			Labels:    map[string]string{migrationRequestLabel: migrationWorkerLabelValue(migration.Spec.ExecutionRequests[0].RequestID)},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: hav1alpha1.GroupVersion.String(),
				Kind:       "HomeAssistantMigration",
				Name:       migration.Name,
				UID:        migration.UID,
				Controller: &yes,
			}},
		},
	}
}

func cleanupTestPod(t *testing.T, migration *hav1alpha1.HomeAssistantMigration) *corev1.Pod {
	t.Helper()
	yes := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "migration-worker-pod",
			Namespace: migration.Namespace,
			UID:       "pod-uid",
			Labels:    map[string]string{migrationRequestLabel: migrationWorkerLabelValue(migration.Spec.ExecutionRequests[0].RequestID)},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "Job",
				Name:       "migration-worker",
				UID:        "job-uid",
				Controller: &yes,
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func cleanupTestArchive(t *testing.T, migration *hav1alpha1.HomeAssistantMigration) *corev1.PersistentVolumeClaim {
	t.Helper()
	yes := true
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "migration-archive",
			Namespace: migration.Namespace,
			UID:       "archive-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: hav1alpha1.GroupVersion.String(),
				Kind:       "HomeAssistantMigration",
				Name:       migration.Name,
				UID:        migration.UID,
				Controller: &yes,
			}},
		},
	}
}

func cleanupTestReceipt(t *testing.T, migration *hav1alpha1.HomeAssistantMigration) *corev1.ConfigMap {
	t.Helper()
	yes := true
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "migration-receipt",
			Namespace: migration.Namespace,
			UID:       "receipt-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: hav1alpha1.GroupVersion.String(),
				Kind:       "HomeAssistantMigration",
				Name:       migration.Name,
				UID:        migration.UID,
				Controller: &yes,
			}},
		},
	}
}

func deleteCleanupMigration(t *testing.T, kube client.Client, migration *hav1alpha1.HomeAssistantMigration) {
	t.Helper()
	if err := kube.Delete(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(migration), stored); err != nil {
		t.Fatal(err)
	}
	if stored.DeletionTimestamp.IsZero() {
		t.Fatal("fake client did not retain the finalizer and set deletionTimestamp")
	}
}

func reconcileCleanup(t *testing.T, kube client.Client) {
	t.Helper()
	reconciler := &HomeAssistantMigrationReconciler{Client: kube, Scheme: kube.Scheme()}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "default", Name: "migration"}}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationDeletionStopsWorkersBeforeReleasingFinalizer(t *testing.T) {
	ctx := context.Background()
	migration := cleanupTestMigration()
	job := cleanupTestJob(t, migration)
	pod := cleanupTestPod(t, migration)
	archive := cleanupTestArchive(t, migration)
	receipt := cleanupTestReceipt(t, migration)
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithStatusSubresource(migration).WithObjects(migration, job, pod, archive, receipt).Build()

	deleteCleanupMigration(t, kube, migration)
	reconcileCleanup(t, kube)

	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(migration), stored); err != nil {
		t.Fatal(err)
	}
	if stored.DeletionTimestamp.IsZero() {
		t.Fatal("deletion timestamp was not observed")
	}
	if !controllerutil.ContainsFinalizer(stored, migrationOperationFinalizer) {
		t.Fatal("finalizer was released while a worker Job still existed")
	}
	condition := meta.FindStatusCondition(stored.Status.Conditions, "Progressing")
	if condition == nil || condition.Reason != "LocalWorkTerminationInProgress" {
		t.Fatalf("condition=%#v", condition)
	}
	for _, obj := range []client.Object{archive, receipt} {
		if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), obj.DeepCopyObject().(client.Object)); err != nil {
			t.Fatalf("durable evidence was removed before cleanup: %v", err)
		}
	}

	// The fake client removes Jobs immediately. The next reconcile observes that
	// the worker is gone and releases the finalizer.
	reconcileCleanup(t, kube)
	if err := kube.Get(ctx, client.ObjectKeyFromObject(migration), stored); err == nil {
		t.Fatalf("migration still exists after finalizer release: %#v", stored)
	}
}

func TestMigrationDeletionRestartKeepsCleanupIdempotent(t *testing.T) {
	ctx := context.Background()
	migration := cleanupTestMigration()
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithStatusSubresource(migration).WithObjects(migration).Build()
	deleteCleanupMigration(t, kube, migration)

	// A restarted reconciler with no workers must still release the finalizer.
	reconcileCleanup(t, kube)
	if err := kube.Get(ctx, client.ObjectKeyFromObject(migration), &hav1alpha1.HomeAssistantMigration{}); err == nil {
		t.Fatal("migration still exists after restart cleanup")
	}
}

func TestMigrationDeletionBlocksOnStuckPodAndRetainsStorage(t *testing.T) {
	ctx := context.Background()
	migration := cleanupTestMigration()
	job := cleanupTestJob(t, migration)
	pod := cleanupTestPod(t, migration)
	archive := cleanupTestArchive(t, migration)
	past := metav1.NewTime(time.Now().Add(-migrationLocalWorkTerminationTimeout - time.Minute))
	pod.DeletionTimestamp = &past
	pod.Finalizers = []string{"test.example/finalizer"}
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithStatusSubresource(migration).WithObjects(migration, job, pod, archive).Build()
	deleteCleanupMigration(t, kube, migration)

	reconcileCleanup(t, kube)

	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(migration), stored); err != nil {
		t.Fatal(err)
	}
	if stored.DeletionTimestamp.IsZero() {
		t.Fatal("deletion timestamp was not observed")
	}
	if !controllerutil.ContainsFinalizer(stored, migrationOperationFinalizer) {
		t.Fatal("finalizer was released while a Pod could not be confirmed stopped")
	}
	condition := meta.FindStatusCondition(stored.Status.Conditions, "Progressing")
	if condition == nil || condition.Reason != "LocalWorkTerminationBlocked" {
		t.Fatalf("condition=%#v", condition)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(archive), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("archive storage was removed while a Pod may still be writing: %v", err)
	}
}

func TestMigrationCancellationStopsLocalWorkAndKeepsEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		abort  bool
		reason string
	}{
		{name: "cancel", reason: "CancellationInProgress"},
		{name: "abort", abort: true, reason: "CancellationInProgress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			migration := cleanupTestMigration()
			migration.Spec.CancelRequested = !tc.abort
			if tc.abort {
				migration.Spec.Resolutions = []hav1alpha1.MigrationResolution{{
					Version: "v1", Stage: "backup", RequestID: migration.Spec.ExecutionRequests[0].RequestID, Decision: "Abort",
				}}
			}
			job := cleanupTestJob(t, migration)
			pod := cleanupTestPod(t, migration)
			archive := cleanupTestArchive(t, migration)
			receipt := cleanupTestReceipt(t, migration)
			kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithStatusSubresource(migration).WithObjects(migration, job, pod, archive, receipt).Build()

			reconcileCleanup(t, kube)

			stored := &hav1alpha1.HomeAssistantMigration{}
			if err := kube.Get(ctx, client.ObjectKeyFromObject(migration), stored); err != nil {
				t.Fatal(err)
			}
			if stored.Status.Phase != "Blocked" {
				t.Fatalf("phase=%q", stored.Status.Phase)
			}
			condition := meta.FindStatusCondition(stored.Status.Conditions, "Progressing")
			if condition == nil || condition.Reason != tc.reason {
				t.Fatalf("condition=%#v", condition)
			}
			if !strings.Contains(condition.Message, "not rolled back") {
				t.Fatalf("message does not preserve the HA outcome boundary: %q", condition.Message)
			}
			if !controllerutil.ContainsFinalizer(stored, migrationOperationFinalizer) {
				t.Fatal("cancellation released the finalizer before local work stopped")
			}
			for _, obj := range []client.Object{archive, receipt} {
				if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), obj.DeepCopyObject().(client.Object)); err != nil {
					t.Fatalf("cancellation removed durable evidence: %v", err)
				}
			}
		})
	}
}

func TestMigrationCleanupIgnoresForeignWorkers(t *testing.T) {
	ctx := context.Background()
	migration := cleanupTestMigration()
	foreign := cleanupTestJob(t, migration)
	foreign.Name = "foreign-worker"
	foreign.UID = "foreign-job-uid"
	foreign.OwnerReferences[0].UID = "another-migration"
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithStatusSubresource(migration).WithObjects(migration, foreign).Build()
	deleteCleanupMigration(t, kube, migration)

	reconcileCleanup(t, kube)

	if err := kube.Get(ctx, client.ObjectKeyFromObject(foreign), &batchv1.Job{}); err != nil {
		t.Fatalf("foreign Job was deleted: %v", err)
	}
}

func TestMigrationCleanupOnlyDeletesBoundWorkerPods(t *testing.T) {
	ctx := context.Background()
	migration := cleanupTestMigration()
	foreign := cleanupTestPod(t, migration)
	foreign.Name = "foreign-pod"
	foreign.UID = "foreign-pod-uid"
	foreign.OwnerReferences[0].Name = "unrelated-job"
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithStatusSubresource(migration).WithObjects(migration, foreign).Build()
	deleteCleanupMigration(t, kube, migration)

	reconcileCleanup(t, kube)

	if err := kube.Get(ctx, client.ObjectKeyFromObject(foreign), &corev1.Pod{}); err != nil {
		t.Fatalf("unrelated Pod was deleted: %v", err)
	}
}

func TestMigrationCleanupRetainsStorageUntilWorkersStop(t *testing.T) {
	ctx := context.Background()
	migration := cleanupTestMigration()
	job := cleanupTestJob(t, migration)
	pod := cleanupTestPod(t, migration)
	archive := cleanupTestArchive(t, migration)
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithStatusSubresource(migration).WithObjects(migration, job, pod, archive).Build()
	deleteCleanupMigration(t, kube, migration)

	state, err := stopMigrationLocalWork(ctx, kube, kube, migration)
	if err != nil {
		t.Fatal(err)
	}
	if state.Done || state.Blocked {
		t.Fatalf("state=%#v", state)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(archive), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("archive PVC was removed while local work was stopping: %v", err)
	}
}

func TestMigrationCleanupUsesUIDBoundObjects(t *testing.T) {
	ctx := context.Background()
	migration := cleanupTestMigration()
	job := cleanupTestJob(t, migration)
	job.UID = types.UID("job-uid")
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithStatusSubresource(migration).WithObjects(migration, job).Build()
	deleteCleanupMigration(t, kube, migration)
	state, err := stopMigrationLocalWork(ctx, kube, kube, migration)
	if err != nil {
		t.Fatal(err)
	}
	if state.Done {
		t.Fatal("owned Job was not treated as local work")
	}
}
