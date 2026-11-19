package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const migrationRequestLabel = "ha.homeassistant.io/migration-worker"

// migrationOnboardingRequestLabel identifies the exact upload or restore
// request a worker belongs to, independent of its network-policy identity.
const migrationOnboardingRequestLabel = "ha.homeassistant.io/migration-onboarding-request"

// BackupQualificationOptions is deliberately not exposed as a production flag.
// The caller must qualify an isolated disposable source and restrict the worker
// ServiceAccount before every start. Production dispatch stays disabled until
// onboarding isolation and safe cancellation are available together.
type BackupQualificationOptions struct {
	Image                string
	ServiceAccount       string
	ArchiveSize          resource.Quantity
	StorageClass         *string
	VerifyIsolation      func(context.Context, *ha.HomeAssistantMigration) error
	ReadBackupResolution func(context.Context, string, []byte, ha.MigrationExecutionRequest, migrationprotocol.Evidence) (migration.BackupResolutionObservation, error)
}

func NewIsolatedBackupQualificationReconciler(
	kube client.Client, scheme *runtime.Scheme, options BackupQualificationOptions,
) (*HomeAssistantMigrationReconciler, error) {
	if kube == nil || scheme == nil || options.Image == "" || options.ServiceAccount == "" ||
		options.ArchiveSize.Sign() <= 0 || options.VerifyIsolation == nil {
		return nil, fmt.Errorf("backup qualification prerequisites unavailable")
	}
	observer := &migrationBackupQualification{
		migrationWorkerObserver: migrationWorkerObserver{reader: kube}, kube: kube, scheme: scheme, options: options,
	}
	return &HomeAssistantMigrationReconciler{Client: kube, APIReader: kube, Scheme: scheme, stageObserver: observer}, nil
}

type migrationBackupQualification struct {
	migrationWorkerObserver
	kube           client.Client
	scheme         *runtime.Scheme
	options        BackupQualificationOptions
	readResolution func(context.Context, string, []byte, ha.MigrationExecutionRequest, migrationprotocol.Evidence) (migration.BackupResolutionObservation, error)
	// clientForHomeAssistant is injectable for tests. Production leaves it nil
	// and verification builds the plain in-cluster HTTP client.
	clientForHomeAssistant func(*hav1.HomeAssistant) *haclient.Client
}

// ObserveExternalResult reads the exact externally indicated backup; it does
// not convert that observation into an accepted receipt or verified archive.
// A lost receipt still needs durable, independently verified archive evidence.
func (o *migrationBackupQualification) ObserveExternalResult(
	ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest, e migrationprotocol.Evidence,
) (migrationStageObservation, error) {
	var unknown migrationStageObservation
	if op == nil || r.Stage != "backup" || o.options.VerifyIsolation == nil || o.options.VerifyIsolation(ctx, op) != nil {
		return unknown, fmt.Errorf("external observation isolation unavailable")
	}
	dispatched := false
	for _, marker := range op.Spec.ExecutionDispatches {
		if marker.Stage == r.Stage && marker.RequestID == r.RequestID {
			dispatched = true
		}
	}
	if !dispatched || migrationprotocol.ValidateAccepted(op) != nil || op.Spec.CancelRequested || migrationprotocol.AbortRequested(op) {
		return unknown, fmt.Errorf("external observation is not authorised")
	}
	jobID := e.JobID
	if jobID == "" {
		jobID = e.BackupID
	}
	if existing, err := migration.ReadWorkerBackupReceipt(ctx, o.reader, op, r); err == nil && existing.Phase == "Accepted" && jobID != "" && existing.JobID != jobID {
		return unknown, fmt.Errorf("external result differs from accepted receipt")
	}
	jobs, pods, err := o.localWorkers(ctx, op, r)
	if err != nil {
		return unknown, err
	}
	base := migrationStageObservation{OperationUID: r.OperationUID, RequestID: r.RequestID, Stage: r.Stage, SourceID: r.SourceID, TargetUID: r.Target.UID, Phase: "Dispatching", JobID: jobID}
	if localBackupWorkersActive(jobs, pods) {
		return base, nil
	}
	if d, cm, err := migration.ReadValidatedBackupEvidence(ctx, o.reader, op, r); err == nil {
		for _, j := range jobs {
			if isBackupReadOnlyJob(j.Name) && j.Status.Succeeded > 0 && validationPodProves(j, pods, migration.ProofForValidatedArchive(d, cm)) {
				base.Phase = "Verified"
				base.JobID = d.JobID
				base.Verified = true
				base.ArchiveDigest = d.Digest
				base.ArchivePVCUID = d.PVCUID
				base.ArchiveRef = &ha.MigrationObjectReference{Name: cm.Name, UID: string(cm.UID)}
				base.ScopeDigest = r.ScopeDigest
				base.VerificationDigest = r.VerificationDigest
				return base, nil
			}
		}
	}
	token, err := migration.ReadPinnedCredential(ctx, o.reader, op.Namespace, r.SourceToken)
	if err != nil {
		return unknown, err
	}
	key, err := migration.ReadPinnedCredential(ctx, o.reader, op.Namespace, r.RecoveryKey)
	if err != nil {
		return unknown, err
	}
	read := o.readResolution
	if read == nil {
		read = o.options.ReadBackupResolution
	}
	if read == nil {
		read = func(ctx context.Context, token string, key []byte, r ha.MigrationExecutionRequest, e migrationprotocol.Evidence) (migration.BackupResolutionObservation, error) {
			return migration.ReadBackupResolution(ctx, haclient.NewClient(r.SourceEndpoint), token, key, r, e)
		}
	}
	observation, err := read(ctx, string(token), key, r, e)
	if err != nil || observation.BackupID != jobID || observation.AgentID == "" || observation.Size <= 0 {
		return unknown, fmt.Errorf("external result is not verified")
	}
	return o.startResolutionObservation(ctx, op, r, e, jobs, base)
}

func isBackupReadOnlyJob(name string) bool {
	return strings.HasPrefix(name, "migration-observe-") || strings.HasPrefix(name, "migration-resolve-")
}

func (o *migrationBackupQualification) startResolutionObservation(
	ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest, e migrationprotocol.Evidence,
	jobs []batchv1.Job, observed migrationStageObservation,
) (migrationStageObservation, error) {
	if len(jobs) == 0 || r.ArchiveStorage == nil {
		return observed, fmt.Errorf("local worker history unavailable")
	}
	digest, err := migrationprotocol.Digest(e)
	if err != nil {
		return observed, fmt.Errorf("resolution evidence invalid")
	}
	name := "migration-resolve-" + strings.TrimPrefix(digest, "sha256:")[:40]
	for _, job := range jobs {
		if job.Name == name {
			return observed, nil
		}
	}
	if o.options.VerifyIsolation(ctx, op) != nil {
		return observed, fmt.Errorf("backup observation isolation unavailable")
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if o.reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: r.ArchiveStorage.PVCName}, pvc) != nil || pvc.UID == "" {
		return observed, fmt.Errorf("archive PVC unavailable")
	}
	owner := metav1.GetControllerOf(pvc)
	if owner == nil || owner.UID != op.UID {
		return observed, fmt.Errorf("archive PVC binding invalid")
	}
	job, err := buildMigrationWorkerJob(o.scheme, op, r, o.options.Image, o.options.ServiceAccount, r.ArchiveStorage.PVCName)
	if err != nil {
		return observed, err
	}
	job.Name = name
	job.Spec.Template.Spec.Containers[0].Args = append(job.Spec.Template.Spec.Containers[0].Args, "--mode", "observe-backup")
	if err := o.kube.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return observed, fmt.Errorf("resolution job creation unavailable")
	}
	return observed, nil
}

func (o *migrationBackupQualification) Start(
	ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest,
) error {
	if r.Stage == "upload" || r.Stage == "restore" {
		return o.startOnboarding(ctx, op, r)
	}
	if r.Stage != "backup" || o.options.VerifyIsolation(ctx, op) != nil {
		return fmt.Errorf("backup qualification isolation unavailable")
	}
	if err := ensureMigrationArchivePVC(ctx, o.kube, o.scheme, op, r, o.options.ArchiveSize, o.options.StorageClass); err != nil {
		return err
	}
	return startMigrationWorker(
		ctx, o.kube, o.scheme, op, r, o.options.Image, o.options.ServiceAccount, r.ArchiveStorage.PVCName, true,
	)
}

func backupTerminalReason(reason string) bool {
	switch reason {
	case "ArchiveValidationFailed", "ArchiveValidationLimitExceeded", "ArchiveProvenanceUnavailable",
		"BackupResultMismatch", "BackupSettingsChanged", "CredentialVersionChanged",
		"ArchiveDescriptorConflict", "UnsupportedArchiveProfile":
		return true
	}
	return false
}

// Observe only starts read-only continuations. The initial POST permit is never
// recreated, even if the receipt or dispatch Job has disappeared.
func (o *migrationBackupQualification) Observe(
	ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest,
) (migrationStageObservation, error) {
	observed := migrationStageObservation{
		OperationUID: string(op.UID), RequestID: r.RequestID, Stage: r.Stage,
		SourceID: r.SourceID, TargetUID: r.Target.UID, Phase: "Dispatching",
	}
	if r.Stage == "upload" || r.Stage == "restore" {
		return o.observeOnboarding(ctx, op, r)
	}
	if r.Stage != "backup" {
		return observed, fmt.Errorf("unsupported migration stage")
	}
	jobs, pods, err := o.localWorkers(ctx, op, r)
	if err != nil {
		return observed, err
	}
	active := localBackupWorkersActive(jobs, pods)
	receipt, err := migration.ReadWorkerBackupReceipt(ctx, o.reader, op, r)
	if err != nil {
		observed.Active = active
		return observed, nil
	}
	dispatched := false
	for _, marker := range op.Spec.ExecutionDispatches {
		if marker.Stage == "backup" && marker.RequestID == r.RequestID {
			dispatched = true
		}
	}
	if !dispatched {
		return observed, fmt.Errorf("backup dispatch binding unavailable")
	}
	observed.Phase, observed.JobID, observed.Active = receipt.Phase, receipt.JobID, active
	if backupTerminalReason(receipt.BlockedReason) {
		observed.Phase = "Blocked"
		observed.BlockedReason = receipt.BlockedReason
		return observed, nil
	}
	if active {
		return observed, nil
	}
	if receipt.Phase != "Accepted" || receipt.JobID == "" {
		return observed, nil
	}
	if d, cm, err := migration.ReadValidatedBackupEvidence(ctx, o.reader, op, r); err == nil {
		// A descriptor is not sufficient by itself: a bound local Job must have
		// completed the byte validation, not merely started or published intent.
		for _, j := range jobs {
			if isBackupReadOnlyJob(j.Name) && j.Status.Succeeded > 0 &&
				validationPodProves(j, pods, migration.ProofForValidatedArchive(d, cm)) {
				observed.Phase = "Verified"
				observed.Verified = true
				observed.ArchiveDigest = d.Digest
				observed.ArchivePVCUID = d.PVCUID
				observed.ArchiveRef = &ha.MigrationObjectReference{Name: cm.Name, UID: string(cm.UID)}
				observed.ScopeDigest = r.ScopeDigest
				observed.VerificationDigest = r.VerificationDigest
				return observed, nil
			}
		}
	}
	return o.continueObservation(ctx, op, r, jobs, observed)
}

func validationPodProves(job batchv1.Job, pods []corev1.Pod, want migration.ArchiveValidationProof) bool {
	for _, p := range pods {
		owner := metav1.GetControllerOf(&p)
		if owner == nil || owner.UID != job.UID || owner.Kind != "Job" || p.Status.Phase != corev1.PodSucceeded {
			continue
		}
		for _, status := range p.Status.ContainerStatuses {
			terminated := status.State.Terminated
			if status.Name != "worker" || terminated == nil || terminated.ExitCode != 0 || len(terminated.Message) > 4096 {
				continue
			}
			var proof migration.ArchiveValidationProof
			if migrationprotocol.Decode([]byte(terminated.Message), &proof) == nil && proof == want {
				return true
			}
		}
	}
	return false
}

func localBackupWorkersActive(jobs []batchv1.Job, pods []corev1.Pod) bool {
	for _, p := range pods {
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			return true
		}
	}
	for _, j := range jobs {
		terminal := false
		for _, c := range j.Status.Conditions {
			if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
				terminal = true
			}
		}
		if !terminal {
			return true
		}
	}
	return false
}

func (o *migrationBackupQualification) continueObservation(
	ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest,
	jobs []batchv1.Job, observed migrationStageObservation,
) (migrationStageObservation, error) {
	if len(jobs) == 0 {
		return observed, fmt.Errorf("local worker history unavailable")
	}
	var continuations int
	latest := time.Time{}
	for _, j := range jobs {
		if strings.HasPrefix(j.Name, "migration-observe-") {
			continuations++
		}
		if j.CreationTimestamp.After(latest) {
			latest = j.CreationTimestamp.Time
		}
	}
	if continuations >= 3 {
		observed.Phase = "Blocked"
		observed.BlockedReason = "BackupObservationRetryExhausted"
		return observed, nil
	}
	if time.Since(latest) < time.Minute {
		return observed, nil
	}
	if o.options.VerifyIsolation(ctx, op) != nil {
		return observed, fmt.Errorf("backup observation isolation unavailable")
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if r.ArchiveStorage == nil {
		return observed, fmt.Errorf("archive storage unavailable")
	}
	pvcKey := client.ObjectKey{Namespace: op.Namespace, Name: r.ArchiveStorage.PVCName}
	if o.reader.Get(ctx, pvcKey, pvc) != nil || pvc.UID == "" {
		return observed, fmt.Errorf("archive PVC unavailable")
	}
	owner := metav1.GetControllerOf(pvc)
	if owner == nil || owner.UID != op.UID {
		return observed, fmt.Errorf("archive PVC binding invalid")
	}
	job, err := buildMigrationWorkerJob(o.scheme, op, r, o.options.Image, o.options.ServiceAccount, r.ArchiveStorage.PVCName)
	if err != nil {
		return observed, err
	}
	job.Name = fmt.Sprintf("migration-observe-%s-%d", strings.TrimPrefix(r.RequestID, "sha256:")[:32], continuations+1)
	job.Spec.Template.Spec.Containers[0].Args = append(job.Spec.Template.Spec.Containers[0].Args, "--mode", "observe-backup")
	if err := o.kube.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return observed, fmt.Errorf("observation job creation unavailable")
	} else if apierrors.IsAlreadyExists(err) {
		existing := &batchv1.Job{}
		if o.reader.Get(ctx, client.ObjectKeyFromObject(job), existing) != nil || !o.boundWorkerJob(existing, op, r) {
			return observed, fmt.Errorf("observation job identity collision")
		}
	}
	return observed, nil
}

func (o *migrationBackupQualification) localWorkers(
	ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest,
) ([]batchv1.Job, []corev1.Pod, error) {
	label := strings.TrimPrefix(r.RequestID, "sha256:")[:40]
	jobs := &batchv1.JobList{}
	pods := &corev1.PodList{}
	selector := client.MatchingLabels{migrationRequestLabel: label}
	if err := o.reader.List(ctx, jobs, client.InNamespace(op.Namespace), selector); err != nil {
		return nil, nil, fmt.Errorf("worker history unavailable")
	}
	if err := o.reader.List(ctx, pods, client.InNamespace(op.Namespace), selector); err != nil {
		return nil, nil, fmt.Errorf("worker pods unavailable")
	}
	for _, j := range jobs.Items {
		if !o.boundWorkerJob(&j, op, r) {
			return nil, nil, fmt.Errorf("worker history binding invalid")
		}
	}
	return jobs.Items, pods.Items, nil
}

func (o *migrationBackupQualification) boundWorkerJob(
	j *batchv1.Job, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest,
) bool {
	owner := metav1.GetControllerOf(j)
	if owner == nil || owner.UID != op.UID || owner.Kind != "HomeAssistantMigration" || owner.Name != op.Name ||
		j.Namespace != op.Namespace || j.Spec.Template.Spec.ServiceAccountName != o.options.ServiceAccount ||
		len(j.Spec.Template.Spec.Containers) != 1 || r.ArchiveStorage == nil {
		return false
	}
	args := []string{"--namespace", op.Namespace, "--migration", op.Name, "--stage", r.Stage, "--request-id", r.RequestID}
	if j.Name != migrationWorkerJobName(r.RequestID) {
		validName := false
		for i := 1; i <= 3; i++ {
			name := fmt.Sprintf("migration-observe-%s-%d", strings.TrimPrefix(r.RequestID, "sha256:")[:32], i)
			if j.Name == name {
				validName = true
			}
		}
		if strings.HasPrefix(j.Name, "migration-resolve-") && len(j.Name) == len("migration-resolve-")+40 {
			validName = true
		}
		if !validName {
			return false
		}
		args = append(args, "--mode", "observe-backup")
	}
	c := j.Spec.Template.Spec.Containers[0]
	if c.Image != o.options.Image || !reflect.DeepEqual(c.Command, []string{"/migrate-worker"}) ||
		!reflect.DeepEqual(c.Args, args) || len(j.Spec.Template.Spec.Volumes) != 1 {
		return false
	}
	v := j.Spec.Template.Spec.Volumes[0]
	return v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == r.ArchiveStorage.PVCName
}
