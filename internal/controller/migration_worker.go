package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ensureMigrationArchivePVC never adopts an existing volume. The caller must
// choose capacity and storage class explicitly from the migration environment.
func ensureMigrationArchivePVC(ctx context.Context, kube client.Client, scheme *runtime.Scheme, op *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest, size resource.Quantity, storageClass *string) error {
	if ctx == nil || kube == nil || scheme == nil || op == nil || op.UID == "" || op.Namespace == "" || op.Spec.CancelRequested || !op.DeletionTimestamp.IsZero() || request.Stage != "backup" || request.ArchiveStorage == nil || request.OperationUID != string(op.UID) || request.ArchiveStorage.PVCName == op.Spec.Target.Name+"-data" || size.Sign() <= 0 || migrationprotocol.ValidateAccepted(op) != nil {
		return fmt.Errorf("archive PVC inputs are invalid")
	}
	accepted := false
	for _, existing := range op.Spec.ExecutionRequests {
		if reflect.DeepEqual(existing, request) {
			accepted = true
		}
	}
	if !accepted {
		return fmt.Errorf("archive request not accepted")
	}
	key := client.ObjectKey{Namespace: op.Namespace, Name: request.ArchiveStorage.PVCName}
	existing := &corev1.PersistentVolumeClaim{}
	if err := kube.Get(ctx, key, existing); err == nil {
		owner := metav1.GetControllerOf(existing)
		quantity := existing.Spec.Resources.Requests[corev1.ResourceStorage]
		if owner == nil || owner.UID != op.UID || quantity.Cmp(size) != 0 || (storageClass != nil && !reflect.DeepEqual(existing.Spec.StorageClassName, storageClass)) || !reflect.DeepEqual(existing.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}) || (existing.Spec.VolumeMode != nil && *existing.Spec.VolumeMode != corev1.PersistentVolumeFilesystem) {
			return fmt.Errorf("archive PVC collides with existing resource")
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("archive PVC unavailable")
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, StorageClassName: storageClass, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}}}}
	if err := controllerutil.SetControllerReference(op, pvc, scheme); err != nil {
		return fmt.Errorf("cannot set archive owner")
	}
	if err := kube.Create(ctx, pvc); err != nil {
		return fmt.Errorf("cannot create archive PVC")
	}
	return nil
}

// claimMigrationAttempt creates the durable one-shot marker using the observed
// resourceVersion. An uncertain response is not permission to start work.
func claimMigrationAttempt(ctx context.Context, writer client.Client, op *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest) error {
	if writer == nil || op == nil || op.UID == "" || op.ResourceVersion == "" || op.Spec.ExecutionProtocol != "v1" || op.Spec.CancelRequested || migrationprotocol.AbortRequested(op) || !op.DeletionTimestamp.IsZero() || migrationprotocol.ValidateAccepted(op) != nil {
		return fmt.Errorf("migration attempt is not authorized")
	}
	accepted := false
	for _, item := range op.Spec.ExecutionRequests {
		if reflect.DeepEqual(item, request) {
			accepted = true
		}
	}
	if !accepted {
		return fmt.Errorf("migration request is not accepted")
	}
	for _, marker := range op.Spec.ExecutionAttempts {
		if marker.Stage == request.Stage {
			return fmt.Errorf("migration attempt already started or outcome unknown")
		}
	}
	updated := op.DeepCopy()
	updated.Spec.ExecutionAttempts = append(updated.Spec.ExecutionAttempts, ha.MigrationExecutionAttempt{Stage: request.Stage, RequestID: request.RequestID})
	if err := writer.Update(ctx, updated); err != nil {
		return fmt.Errorf("migration attempt claim outcome unknown")
	}
	return nil
}

// startMigrationWorker remains disconnected from the production reconciler.
// The caller must independently establish that isolation is actually enforced.
func startMigrationWorker(ctx context.Context, kube client.Client, scheme *runtime.Scheme, op *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest, image, serviceAccount, pvcName string, isolationVerified bool) error {
	if !isolationVerified || kube == nil || op == nil {
		return fmt.Errorf("worker isolation is unavailable")
	}
	job, err := buildMigrationWorkerJob(scheme, op, request, image, serviceAccount, pvcName)
	if err != nil {
		return err
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: pvcName}, pvc); err != nil || pvc.UID == "" {
		return fmt.Errorf("archive PVC is unavailable")
	}
	owner := metav1.GetControllerOf(pvc)
	if owner == nil || owner.UID != op.UID {
		return fmt.Errorf("archive PVC is not owned by migration")
	}
	if err := claimMigrationAttempt(ctx, kube, op, request); err != nil {
		return err
	}
	// Failure or lost response after the marker was persisted is ambiguous.
	// Neither a missing Job nor a restart authorizes creating it again.
	if err := kube.Create(ctx, job); err != nil {
		return fmt.Errorf("worker creation outcome unknown")
	}
	return nil
}

// buildMigrationWorkerJob only constructs a Job. Creating it requires an
// already persisted attempt marker and separately enforced worker isolation.
func buildMigrationWorkerJob(scheme *runtime.Scheme, op *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest, image, serviceAccount, pvc string) (*batchv1.Job, error) {
	if scheme == nil || op == nil || op.UID == "" || op.Namespace == "" || image == "" || len(validation.IsDNS1123Subdomain(serviceAccount)) != 0 || len(validation.IsDNS1123Subdomain(pvc)) != 0 || migrationprotocol.ValidateRequest(request) != nil || request.OperationUID != string(op.UID) || request.SourceID != op.Spec.SourceID || request.Target != op.Spec.Target {
		return nil, fmt.Errorf("invalid migration worker inputs")
	}
	accepted := false
	for _, r := range op.Spec.ExecutionRequests {
		if r.Stage == request.Stage && r.RequestID == request.RequestID {
			accepted = true
		}
	}
	if !accepted || (request.Stage == "backup" && (request.ArchiveStorage == nil || request.ArchiveStorage.PVCName != pvc)) {
		return nil, fmt.Errorf("worker request or archive is not bound")
	}
	if request.Stage != "backup" {
		binding := op.Spec.ProxyCredentialBinding
		if binding == nil || binding.RequestID == "" || (request.Stage == "upload" && binding.RequestID != request.RequestID) || (request.Stage == "restore" && binding.RequestID != request.UploadRequestID) {
			return nil, fmt.Errorf("worker proxy credentials are not bound")
		}
	}
	name := migrationWorkerJobName(request.RequestID)
	zero, deadline := int32(0), int64(3300)
	no, yes := false, true
	uid, gid := int64(65532), int64(65532)
	labels := map[string]string{migrationRequestLabel: strings.TrimPrefix(request.RequestID, "sha256:")[:40]}
	if request.Stage != "backup" {
		// The target NetworkPolicy admits the label derived from the upload
		// request, which also names the credential binding. Restore therefore
		// keeps that network label and is listed by its own request label.
		labels[migrationRequestLabel] = strings.TrimPrefix(op.Spec.ProxyCredentialBinding.RequestID, "sha256:")[:40]
		labels[migrationOnboardingRequestLabel] = strings.TrimPrefix(request.RequestID, "sha256:")[:40]
	}
	replaceFailed := batchv1.Failed
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: op.Namespace, Labels: labels}, Spec: batchv1.JobSpec{
		BackoffLimit: &zero, ActiveDeadlineSeconds: &deadline, PodReplacementPolicy: &replaceFailed,
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
			ServiceAccountName: serviceAccount, RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &gid, FSGroup: &gid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{Name: "worker", Image: image, Command: []string{"/migrate-worker"}, Args: []string{"--namespace", op.Namespace, "--migration", op.Name, "--stage", request.Stage, "--request-id", request.RequestID},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("256Mi")}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "archive", MountPath: "/archive"}},
			}},
			Volumes: []corev1.Volume{{Name: "archive", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc}}}},
		}},
	}}
	if request.Stage != "backup" {
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{Name: "migration-proxy-worker", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: proxyWorkerCredentialName(op.Spec.ProxyCredentialBinding.Bundle.Name)}}})
		container := &job.Spec.Template.Spec.Containers[0]
		// Upload and restore only read the archive; a writable mount would let a
		// compromised worker alter the bytes its descriptor vouches for.
		container.VolumeMounts[0].ReadOnly = true
		job.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly = true
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "migration-proxy-worker", MountPath: "/var/run/migration-worker", ReadOnly: true})
		container.Env = append(container.Env, corev1.EnvVar{Name: "MIGRATION_ONBOARDING_ENDPOINT", Value: fmt.Sprintf("https://%s.%s.svc:8124", request.Target.Name, op.Namespace)})
		container.Args = append(container.Args, "--mode", "dispatch-onboarding")
	}
	if err := controllerutil.SetControllerReference(op, job, scheme); err != nil {
		return nil, fmt.Errorf("cannot set worker owner")
	}
	return job, nil
}

func migrationWorkerJobName(requestID string) string {
	return "migration-" + strings.TrimPrefix(requestID, "sha256:")[:40]
}
