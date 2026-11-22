package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/test/utils"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// TestMigrationRestoreCancellationAndGarbageCollection exercises T069 on a real
// API server: cancellation stops local worker Jobs without releasing the
// finalizer, deletion waits for local Pods, and the archive PVC is garbage
// collected only after the finalizer is released. It never contacts Home
// Assistant and makes no claim about an accepted HA request being rolled back.
func TestMigrationRestoreCancellationAndGarbageCollection(t *testing.T) {
	if os.Getenv("HA_MIGRATION_RESTORE_CLUSTER") != "true" {
		t.Skip("set HA_MIGRATION_RESTORE_CLUSTER=true on a disposable cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	scheme := runtime.NewScheme()
	if err := ha.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	kube, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	ns := "migration-restore-" + utils.RandomString(8)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	if err := kube.Create(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		restoreCleanupNamespace(t, kube, namespace)
	})

	operation := &ha.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "restore",
			Namespace:  ns,
			Finalizers: []string{"ha.homeassistant.io/migration-operation-finalizer"},
		},
		Spec: ha.HomeAssistantMigrationSpec{
			SourceID: "https://source.example",
			Target:   ha.MigrationTarget{Name: "target", UID: "target-uid"},
		},
	}
	if err := kube.Create(ctx, operation); err != nil {
		t.Fatal(err)
	}
	operation.UID = readMigrationUID(t, ctx, kube, operation)

	yes := true
	archive := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "archive",
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: ha.GroupVersion.String(),
				Kind:       "HomeAssistantMigration",
				Name:       operation.Name,
				UID:        operation.UID,
				Controller: &yes,
			}},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Mi")},
			},
		},
	}
	if err := kube.Create(ctx, archive); err != nil {
		t.Fatal(err)
	}

	backoff := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "migration-worker",
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: ha.GroupVersion.String(),
				Kind:       "HomeAssistantMigration",
				Name:       operation.Name,
				UID:        operation.UID,
				Controller: &yes,
			}},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "worker",
						Image:   "registry.k8s.io/pause:3.9",
						Command: []string{"/pause"},
					}},
				},
			},
		},
	}
	if err := kube.Create(ctx, job); err != nil {
		t.Fatal(err)
	}

	// Cancellation must stop local work and keep the finalizer and archive.
	operation = readMigration(t, ctx, kube, client.ObjectKeyFromObject(operation))
	operation.Spec.CancelRequested = true
	if err := kube.Update(ctx, operation); err != nil {
		t.Fatal(err)
	}
	backupEventually(t, ctx, func() bool {
		current := &ha.HomeAssistantMigration{}
		if err := kube.Get(ctx, client.ObjectKeyFromObject(operation), current); err != nil {
			return false
		}
		if current.Status.Phase != "Blocked" {
			return false
		}
		for _, condition := range current.Status.Conditions {
			if condition.Type == "Progressing" &&
				(condition.Reason == "CancellationInProgress" || condition.Reason == "CancellationRequested") {
				return true
			}
		}
		return false
	}, "cancellation blocks new work")
	backupEventually(t, ctx, func() bool {
		err := kube.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{})
		return apierrors.IsNotFound(err)
	}, "cancellation stops the worker Job")
	current := readMigration(t, ctx, kube, client.ObjectKeyFromObject(operation))
	if len(current.Finalizers) == 0 {
		t.Fatal("cancellation released the finalizer before the operation was deleted")
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(archive), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("cancellation removed the archive before deletion: %v", err)
	}

	// Deletion must wait for local Pods, then release the finalizer and let GC
	// remove the archive. An accepted HA request is never rolled back locally.
	if err := kube.Delete(ctx, current); err != nil {
		t.Fatal(err)
	}
	backupEventually(t, ctx, func() bool {
		err := kube.Get(ctx, client.ObjectKeyFromObject(operation), &ha.HomeAssistantMigration{})
		return apierrors.IsNotFound(err)
	}, "deletion releases the finalizer after local Pods stop")
	backupEventually(t, ctx, func() bool {
		err := kube.Get(ctx, client.ObjectKeyFromObject(archive), &corev1.PersistentVolumeClaim{})
		return apierrors.IsNotFound(err)
	}, "garbage collection removes the archive PVC")
}

func readMigration(t *testing.T, ctx context.Context, kube client.Client, key client.ObjectKey) *ha.HomeAssistantMigration {
	t.Helper()
	current := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, key, current); err != nil {
		t.Fatal(err)
	}
	return current
}

func readMigrationUID(t *testing.T, ctx context.Context, kube client.Client, operation *ha.HomeAssistantMigration) types.UID {
	t.Helper()
	current := readMigration(t, ctx, kube, client.ObjectKeyFromObject(operation))
	if current.UID == "" {
		t.Fatal("migration UID is empty")
	}
	return current.UID
}

func restoreCleanupNamespace(t *testing.T, kube client.Client, created *corev1.Namespace) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	current := &corev1.Namespace{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(created), current); apierrors.IsNotFound(err) {
		return
	} else if err != nil {
		t.Error(err)
		return
	}
	if current.UID != created.UID {
		t.Error("test namespace UID changed; teardown refused")
		return
	}
	_ = kube.Delete(ctx, current)
}
