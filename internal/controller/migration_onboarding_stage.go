package controller

import (
	"context"
	"errors"
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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// BuildOnboardingWorkerRBAC constructs the namespaced permissions of an upload
// or restore worker. It does not create anything or prove isolation. ConfigMap
// create cannot be limited by resourceNames and must be constrained by admission.
func BuildOnboardingWorkerRBAC(scheme *runtime.Scheme, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest, name string) (*corev1.ServiceAccount, *rbacv1.Role, *rbacv1.RoleBinding, error) {
	if scheme == nil || op == nil || op.Namespace == "" || op.UID == "" || op.Spec.CancelRequested ||
		!op.DeletionTimestamp.IsZero() || migrationprotocol.AbortRequested(op) ||
		len(validation.IsDNS1123Subdomain(name)) != 0 || (r.Stage != "upload" && r.Stage != "restore") ||
		r.ArchiveRef == nil || op.Spec.ArchiveStorage == nil || migrationprotocol.ValidateAccepted(op) != nil {
		return nil, nil, nil, fmt.Errorf("onboarding worker RBAC inputs invalid")
	}
	accepted := false
	for _, request := range op.Spec.ExecutionRequests {
		if reflect.DeepEqual(request, r) {
			accepted = true
		}
	}
	if !accepted {
		return nil, nil, nil, fmt.Errorf("onboarding request not accepted")
	}
	receipts := []string{migration.OnboardingReceiptName(r.RequestID)}
	if r.Stage == "restore" {
		receipts = append(receipts, migration.OnboardingReceiptName(r.UploadRequestID))
	}
	meta := metav1.ObjectMeta{Name: name, Namespace: op.Namespace}
	rules := []rbacv1.PolicyRule{
		// The worker claims its own one-shot POST permit in the append-only
		// spec.executionDispatches list before it may send anything, so it needs
		// update on exactly this migration; CRD transition rules keep that
		// permission append-only and cannot rewrite history or approvals.
		{APIGroups: []string{ha.GroupVersion.Group}, Resources: []string{"homeassistantmigrations"}, ResourceNames: []string{op.Name}, Verbs: []string{"get", "update"}},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}, ResourceNames: []string{op.Spec.ArchiveStorage.PVCName}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: append([]string{r.ArchiveRef.Name}, receipts...), Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: receipts[:1], Verbs: []string{"update"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"create"}},
	}
	// Only restore may read a credential, and only the pinned recovery key.
	// An empty resourceNames list would grant every Secret, so it is omitted.
	if r.Stage == "restore" && r.RecoveryKey != nil {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{r.RecoveryKey.Name}, Verbs: []string{"get"}})
	}
	sa := &corev1.ServiceAccount{ObjectMeta: *meta.DeepCopy()}
	role := &rbacv1.Role{ObjectMeta: *meta.DeepCopy(), Rules: rules}
	binding := &rbacv1.RoleBinding{ObjectMeta: *meta.DeepCopy(), RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: op.Namespace}}}
	for _, object := range []metav1.Object{sa, role, binding} {
		if err := controllerutil.SetControllerReference(op, object, scheme); err != nil {
			return nil, nil, nil, fmt.Errorf("onboarding worker RBAC owner unavailable")
		}
	}
	return sa, role, binding, nil
}

// errMigrationStartPrecondition marks a refusal that happens strictly before an
// attempt marker is written. It is safe to re-evaluate and is not an unknown
// outcome, unlike a failure after the marker or the Job create.
var errMigrationStartPrecondition = errors.New("migration start precondition not met")

func notReady(message string) error {
	return fmt.Errorf("%w: %s", errMigrationStartPrecondition, message)
}

// Prepare supplies archive identity and the stop evidence for an upload or
// restore request. It reads only: no HA mutation and no worker is started.
func (o *migrationBackupQualification) Prepare(ctx context.Context, op *ha.HomeAssistantMigration, stage string) (migrationRequestObservation, bool, error) {
	var none migrationRequestObservation
	if op == nil || (stage != "upload" && stage != "restore") {
		return none, false, fmt.Errorf("onboarding preparation stage invalid")
	}
	var backup *ha.MigrationExecutionRequest
	for i := range op.Spec.ExecutionRequests {
		if op.Spec.ExecutionRequests[i].Stage == "backup" {
			backup = &op.Spec.ExecutionRequests[i]
		}
	}
	if backup == nil {
		return none, false, fmt.Errorf("verified backup unavailable")
	}
	observed, err := o.Observe(ctx, op, *backup)
	if err != nil || !observed.Verified || observed.ArchiveRef == nil || observed.ArchiveDigest == "" || observed.ArchivePVCUID == "" {
		return none, false, fmt.Errorf("verified backup unavailable")
	}
	stopped, err := sourceStopVerified(ctx, o.reader, op)
	if err != nil {
		return none, false, err
	}
	return migrationRequestObservation{ArchiveRef: observed.ArchiveRef, ArchiveDigest: observed.ArchiveDigest, ArchivePVCUID: observed.ArchivePVCUID}, stopped, nil
}

// PrepareIsolation creates, never adopts, the stopped-target policy and the
// immutable proxy credentials for the accepted upload request. The durable
// binding on the operation is the boundary: once it exists the target may run,
// so only the already-bound policy is re-verified from then on.
func (o *migrationBackupQualification) PrepareIsolation(ctx context.Context, op *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest) (bool, error) {
	if o.kube == nil || o.reader == nil || op == nil || request.Stage != "upload" {
		return false, fmt.Errorf("onboarding isolation preparation unavailable")
	}
	target := &hav1.HomeAssistant{}
	if err := o.reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: op.Spec.Target.Name}, target); err != nil {
		return false, fmt.Errorf("onboarding target unavailable")
	}
	if op.Spec.ProxyCredentialBinding != nil {
		return false, observeTargetPolicySnapshot(ctx, o.reader, o.scheme, op, target, request)
	}
	if err := ensureStoppedTargetPolicy(ctx, o.kube, o.reader, o.scheme, op, target, request); err != nil {
		return false, err
	}
	if err := observeTargetPolicySnapshot(ctx, o.reader, o.scheme, op, target, request); err != nil {
		return false, err
	}
	binding, err := createProxyCredentialBundle(ctx, o.kube, o.reader, o.scheme, op, request, time.Now())
	if err != nil {
		return false, err
	}
	if err := bindProxyCredentialBundle(ctx, o.kube, o.reader, op, *binding, time.Now()); err != nil {
		return false, err
	}
	return true, nil
}

func (o *migrationBackupQualification) startOnboarding(ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) error {
	if o.options.VerifyIsolation(ctx, op) != nil {
		return notReady("onboarding isolation unavailable")
	}
	// Everything is re-read directly: an earlier approval, stop proof, or Pod
	// readiness observation is not authority for a later destructive POST.
	fresh := &ha.HomeAssistantMigration{}
	if err := o.reader.Get(ctx, client.ObjectKeyFromObject(op), fresh); err != nil || fresh.UID != op.UID ||
		fresh.Spec.CancelRequested || !fresh.DeletionTimestamp.IsZero() || migrationprotocol.AbortRequested(fresh) ||
		migrationprotocol.ValidateAccepted(fresh) != nil || fresh.Spec.ArchiveStorage == nil {
		return notReady("onboarding operation unavailable")
	}
	var restore *ha.MigrationApproval
	for i := range fresh.Spec.Approvals {
		if fresh.Spec.Approvals[i].ID == "restore" {
			if restore != nil {
				return notReady("restore approval is ambiguous")
			}
			restore = &fresh.Spec.Approvals[i]
		}
	}
	if !restoreApprovalBound(fresh, restore) {
		return notReady("restore approval is not bound")
	}
	if stopped, err := sourceStopVerified(ctx, o.reader, fresh); err != nil || !stopped {
		return notReady("source stop is not verified")
	}
	binding := fresh.Spec.ProxyCredentialBinding
	if binding == nil || bindProxyCredentialBundle(ctx, o.kube, o.reader, fresh, *binding, time.Now()) != nil {
		return notReady("proxy credentials are not bound")
	}
	var upload *ha.MigrationExecutionRequest
	for i := range fresh.Spec.ExecutionRequests {
		if fresh.Spec.ExecutionRequests[i].Stage == "upload" {
			upload = &fresh.Spec.ExecutionRequests[i]
		}
	}
	if upload == nil {
		return notReady("upload request unavailable")
	}
	target := &hav1.HomeAssistant{}
	if err := o.reader.Get(ctx, client.ObjectKey{Namespace: fresh.Namespace, Name: fresh.Spec.Target.Name}, target); err != nil || string(target.UID) != fresh.Spec.Target.UID ||
		observeTargetPolicySnapshot(ctx, o.reader, o.scheme, fresh, target, *upload) != nil || !targetProxyReady(ctx, o.reader, target) {
		return notReady("target onboarding proxy is not ready")
	}
	if r.Stage == "restore" {
		receipt, err := migration.ReadOnboardingReceipt(ctx, o.reader, fresh, *upload)
		if err != nil || receipt.Phase != "Verified" {
			return notReady("verified upload is required before restore")
		}
	}
	return startMigrationWorker(ctx, o.kube, o.scheme, fresh, r, o.options.Image, o.options.ServiceAccount, fresh.Spec.ArchiveStorage.PVCName, true)
}

// targetProxyReady requires the exact target Pod and its server-only proxy
// sidecar to be ready. It is a precondition, not evidence of a restore.
func targetProxyReady(ctx context.Context, reader client.Reader, target *hav1.HomeAssistant) bool {
	pod := &corev1.Pod{}
	if reader.Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: target.Name + "-0"}, pod) != nil || !pod.DeletionTimestamp.IsZero() ||
		pod.Labels[migrationTargetLabel] != migrationTargetSelector(string(target.UID)) {
		return false
	}
	ready := false
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	proxy := false
	for _, s := range pod.Status.ContainerStatuses {
		if s.Name == "migration-onboarding-proxy" && s.Ready {
			proxy = true
		}
	}
	return ready && proxy
}

func (o *migrationBackupQualification) observeOnboarding(ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) (migrationStageObservation, error) {
	observed := migrationStageObservation{
		OperationUID: string(op.UID), RequestID: r.RequestID, Stage: r.Stage,
		SourceID: r.SourceID, TargetUID: r.Target.UID, Phase: "Dispatching",
	}
	jobs, pods, err := o.localOnboardingWorkers(ctx, op, r)
	if err != nil {
		return observed, err
	}
	active := localBackupWorkersActive(jobs, pods)
	receipt, err := migration.ReadOnboardingReceipt(ctx, o.reader, op, r)
	if err != nil {
		// A missing receipt after the attempt marker cannot prove the POST did
		// not happen; it is only progress while a bound worker is still running.
		observed.Active = active
		return observed, nil
	}
	switch receipt.Phase {
	case "Dispatching":
		observed.Active = active
	case "Verified":
		if r.Stage != "upload" || active {
			observed.Phase = "Unknown"
			return observed, nil
		}
		observed.Phase, observed.Verified = "Verified", true
		observed.ArchiveDigest, observed.ArchiveRef, observed.ArchivePVCUID = r.ArchiveDigest, r.ArchiveRef, r.ArchivePVCUID
		observed.ScopeDigest, observed.VerificationDigest = r.ScopeDigest, r.VerificationDigest
	case "Accepted":
		// HTTP acceptance of a restore is never a verified restore: target access
		// and data consistency must be verified before this stage can progress.
		if r.Stage != "restore" || active {
			observed.Phase = "Unknown"
			return observed, nil
		}
		observed.Phase, observed.JobID = "Accepted", receipt.BackupID
		// Try to verify access and data consistency after restore acceptance
		if verified, resultDigest, err := o.verifyRestoreAccessAndData(ctx, op, r); err == nil && verified {
			observed.Phase = "Verified"
			observed.Verified = true
			observed.AccessVerified = true
			observed.ResultDigest = resultDigest
			observed.ArchiveDigest = r.ArchiveDigest
			observed.ArchiveRef = r.ArchiveRef
			observed.ArchivePVCUID = r.ArchivePVCUID
			observed.ScopeDigest = r.ScopeDigest
			observed.VerificationDigest = r.VerificationDigest
		}
	default:
		observed.Phase = receipt.Phase
	}
	return observed, nil
}

func (o *migrationBackupQualification) localOnboardingWorkers(ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) ([]batchv1.Job, []corev1.Pod, error) {
	selector := client.MatchingLabels{migrationOnboardingRequestLabel: strings.TrimPrefix(r.RequestID, "sha256:")[:40]}
	jobs, pods := &batchv1.JobList{}, &corev1.PodList{}
	if err := o.reader.List(ctx, jobs, client.InNamespace(op.Namespace), selector); err != nil {
		return nil, nil, fmt.Errorf("onboarding worker history unavailable")
	}
	if err := o.reader.List(ctx, pods, client.InNamespace(op.Namespace), selector); err != nil {
		return nil, nil, fmt.Errorf("onboarding worker pods unavailable")
	}
	for i := range jobs.Items {
		if !o.boundOnboardingJob(&jobs.Items[i], op, r) {
			return nil, nil, fmt.Errorf("onboarding worker history binding invalid")
		}
	}
	return jobs.Items, pods.Items, nil
}

func (o *migrationBackupQualification) boundOnboardingJob(j *batchv1.Job, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) bool {
	owner := metav1.GetControllerOf(j)
	if owner == nil || owner.UID != op.UID || owner.Kind != "HomeAssistantMigration" || owner.Name != op.Name ||
		j.Namespace != op.Namespace || j.Name != migrationWorkerJobName(r.RequestID) ||
		j.Spec.Template.Spec.ServiceAccountName != o.options.ServiceAccount || len(j.Spec.Template.Spec.Containers) != 1 {
		return false
	}
	c := j.Spec.Template.Spec.Containers[0]
	args := []string{"--namespace", op.Namespace, "--migration", op.Name, "--stage", r.Stage, "--request-id", r.RequestID, "--mode", "dispatch-onboarding"}
	return c.Image == o.options.Image && reflect.DeepEqual(c.Command, []string{"/migrate-worker"}) && reflect.DeepEqual(c.Args, args) && len(j.Spec.Template.Spec.Volumes) == 2
}

// verifyRestoreAccessAndData proves, by reading the restored target, that the
// operator's pinned identity still has access and that every approved
// verification item matches its expected projection. It is read-only: it never
// bootstraps, mutates Home Assistant or releases a gate. A missing token, a
// changed credential version, an unreachable target or any mismatch leaves the
// caller in its pre-verification phase.
func (o *migrationBackupQualification) verifyRestoreAccessAndData(ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) (bool, string, error) {
	if o == nil || o.kube == nil || op == nil || r.Stage != "restore" {
		return false, "", fmt.Errorf("restore verification inputs unavailable")
	}
	// The verification set must be the approved, frozen set bound to the request.
	approvedDigest, err := migrationprotocol.VerificationDigest(op.Spec.Verification)
	if err != nil || approvedDigest != r.VerificationDigest || len(op.Spec.Verification) == 0 {
		return false, "", fmt.Errorf("restore verification set unavailable")
	}
	// Get target HomeAssistant and confirm the exact bound UID.
	target := &hav1.HomeAssistant{}
	if err := o.kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: op.Spec.Target.Name}, target); err != nil {
		return false, "", fmt.Errorf("restore target unavailable")
	}
	if string(target.UID) != op.Spec.Target.UID {
		return false, "", fmt.Errorf("restore target UID mismatch")
	}
	// Find the backup request to recover the pinned operator identity. The
	// restore request intentionally does not carry a credential projection.
	var backupRequest *ha.MigrationExecutionRequest
	for i := range op.Spec.ExecutionRequests {
		if op.Spec.ExecutionRequests[i].Stage == "backup" {
			backupRequest = &op.Spec.ExecutionRequests[i]
			break
		}
	}
	if backupRequest == nil || backupRequest.SourceToken == nil {
		return false, "", fmt.Errorf("restore source token unavailable")
	}
	// The pinned identity must be the operator's declared source token. A request
	// that points at another Secret cannot prove operator access to the target.
	if op.Spec.SourceTokenSecretRef == nil ||
		backupRequest.SourceToken.Name != op.Spec.SourceTokenSecretRef.Name ||
		backupRequest.SourceToken.Key != op.Spec.SourceTokenSecretRef.Key {
		return false, "", fmt.Errorf("restore source token identity mismatch")
	}
	// Validate the pinned credential version before reading its value. A rotated
	// or deleted Secret is not evidence of access and never releases the gate.
	sourceTokenSecret := &corev1.Secret{}
	if err := o.kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: backupRequest.SourceToken.Name}, sourceTokenSecret); err != nil {
		return false, "", fmt.Errorf("restore source token secret unavailable")
	}
	if err := migrationprotocol.ValidateCredentialVersion(backupRequest.SourceToken, sourceTokenSecret); err != nil {
		return false, "", fmt.Errorf("restore source token version changed")
	}
	token := string(sourceTokenSecret.Data[backupRequest.SourceToken.Key])
	if token == "" {
		return false, "", fmt.Errorf("restore source token data unavailable")
	}
	// Use the pinned operator token against the restored target. The in-cluster
	// URL is plain HTTP; verification never trusts Pod readiness or a 2xx alone.
	haClient := o.clientForHomeAssistant
	if haClient == nil {
		haClient = func(target *hav1.HomeAssistant) *haclient.Client {
			return newHAClientForHA(target, nil)
		}
	}
	client := haClient(target).WithTimeout(30 * time.Second)
	if err := client.CheckHealthWithToken(ctx, token); err != nil {
		return false, "", fmt.Errorf("restore target access unavailable with operator token")
	}
	// Read each approved projection and compare it with the frozen expectation.
	// The result digest binds the actual observed projections, not the approved
	// expectations, so resume consent cannot be satisfied by copying the set digest.
	results := make([]migrationprotocol.VerificationResult, 0, len(op.Spec.Verification))
	for _, item := range op.Spec.Verification {
		data, err := client.ReadMigrationVerification(ctx, token, item.Kind, item.ID)
		if err != nil {
			return false, "", fmt.Errorf("restore verification data unavailable for %s/%s", item.Kind, item.ID)
		}
		digest, err := migrationprotocol.ProjectionDigest(item.Kind, item.ID, data)
		if err != nil {
			return false, "", fmt.Errorf("restore verification digest calculation failed for %s/%s", item.Kind, item.ID)
		}
		if digest != item.ExpectedDigest {
			return false, "", fmt.Errorf("restore verification mismatch for %s/%s", item.Kind, item.ID)
		}
		results = append(results, migrationprotocol.VerificationResult{Kind: item.Kind, ID: item.ID, Digest: digest})
	}
	resultDigest, err := migrationprotocol.VerificationResultDigest(results)
	if err != nil {
		return false, "", fmt.Errorf("restore result digest calculation failed")
	}
	return true, resultDigest, nil
}
