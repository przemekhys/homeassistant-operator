package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fakeWithStatus(scheme *runtime.Scheme, objects ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&ha.HomeAssistantMigration{}).WithObjects(objects...).Build()
}

type onboardingStageFixture struct {
	kube    client.Client
	op      *ha.HomeAssistantMigration
	upload  ha.MigrationExecutionRequest
	restore ha.MigrationExecutionRequest
	target  *hav1.HomeAssistant
}

// newOnboardingStageFixture accepts upload and restore the way the operation
// does, then creates and binds proxy credentials for the upload request.
func newOnboardingStageFixture(t *testing.T) *onboardingStageFixture {
	t.Helper()
	return newOnboardingStageFixtureWith(t, true)
}

func newOnboardingStageFixtureWith(t *testing.T, bind bool) *onboardingStageFixture {
	t.Helper()
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	for _, add := range []func(*runtime.Scheme) error{hav1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme, batchv1.AddToScheme, rbacv1.AddToScheme} {
		if err := add(kube.Scheme()); err != nil {
			t.Fatal(err)
		}
	}
	obs := migrationRequestObservation{ArchiveRef: &ha.MigrationObjectReference{Name: "descriptor", UID: "descriptor-uid"}, ArchiveDigest: "sha256:" + strings.Repeat("c", 64), ArchivePVCUID: "pvc-uid"}
	op.Spec.Archive = &ha.MigrationArchive{BackupID: "backup", AgentID: "backup.local", Digest: obs.ArchiveDigest}
	op.Spec.RestoreScopeDigest = "sha256:" + strings.Repeat("d", 64)
	vd, err := migrationprotocol.VerificationDigest(op.Spec.Verification)
	if err != nil {
		t.Fatal(err)
	}
	op.Spec.Approvals = append(op.Spec.Approvals,
		ha.MigrationApproval{ID: "restore", Approved: true, Digest: op.Spec.ReportID, TargetUID: op.Spec.Target.UID, ScopeDigest: op.Spec.RestoreScopeDigest, ArchiveDigest: obs.ArchiveDigest, VerificationDigest: vd},
		ha.MigrationApproval{ID: "source-stop", Approved: true, Digest: op.Spec.ReportID, TargetUID: op.Spec.Target.UID})
	if err := kube.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	upload, err := acceptMigrationRequest(ctx, kube, kube, op, "upload", obs)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	restore, err := acceptMigrationRequest(ctx, kube, kube, op, "restore", obs)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if bind {
		now := time.Now()
		ref, err := createProxyCredentialBundle(ctx, proxySecretWriter{Client: kube}, kube, kube.Scheme(), op, *upload, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := bindProxyCredentialBundle(ctx, kube, kube, op, *ref, now); err != nil {
			t.Fatal(err)
		}
		if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
			t.Fatal(err)
		}
	}
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: op.Spec.Target.Name, Namespace: op.Namespace, UID: types.UID(op.Spec.Target.UID)}, Spec: hav1.HomeAssistantSpec{Alpha: &hav1.AlphaSpec{MigrationRestore: &hav1.MigrationRestoreAlphaSpec{Enabled: true}}}}
	return &onboardingStageFixture{kube: kube, op: op, upload: *upload, restore: *restore, target: target}
}

func TestOnboardingWorkerJobKeepsNetworkIdentityAndIsReadOnly(t *testing.T) {
	x := newOnboardingStageFixture(t)
	uploadLabel := strings.TrimPrefix(x.upload.RequestID, "sha256:")[:40]
	for _, request := range []ha.MigrationExecutionRequest{x.upload, x.restore} {
		job, err := buildMigrationWorkerJob(x.kube.Scheme(), x.op, request, "operator:test", "worker", "archive")
		if err != nil {
			t.Fatalf("%s: %v", request.Stage, err)
		}
		pod := job.Spec.Template
		if pod.Labels[migrationRequestLabel] != uploadLabel {
			t.Fatalf("%s worker is not admitted by the target policy label", request.Stage)
		}
		if pod.Labels[migrationOnboardingRequestLabel] != strings.TrimPrefix(request.RequestID, "sha256:")[:40] {
			t.Fatalf("%s worker is not listed by its own request", request.Stage)
		}
		container := pod.Spec.Containers[0]
		if !container.VolumeMounts[0].ReadOnly || !pod.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly {
			t.Fatalf("%s worker can modify the archive", request.Stage)
		}
		worker := pod.Spec.Volumes[1].Secret
		if worker == nil || worker.SecretName != proxyWorkerCredentialName(x.op.Spec.ProxyCredentialBinding.Bundle.Name) {
			t.Fatalf("%s worker mounts something other than the worker projection", request.Stage)
		}
		encoded, _ := json.Marshal(job)
		for _, forbidden := range []string{x.op.Spec.ProxyCredentialBinding.Bundle.Name + `"`, "-server", "sensitive-key", "sensitive-token"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("%s worker references %q", request.Stage, forbidden)
			}
		}
		if got := container.Args[len(container.Args)-2:]; got[0] != "--mode" || got[1] != "dispatch-onboarding" {
			t.Fatalf("%s worker mode = %v", request.Stage, got)
		}
	}
	if job, _ := buildMigrationWorkerJob(x.kube.Scheme(), x.op, x.restore, "operator:test", "worker", "archive"); job.Name == migrationWorkerJobName(x.upload.RequestID) {
		t.Fatal("restore reused the upload job identity")
	}
	unbound := x.op.DeepCopy()
	unbound.Spec.ProxyCredentialBinding = nil
	if _, err := buildMigrationWorkerJob(x.kube.Scheme(), unbound, x.upload, "operator:test", "worker", "archive"); err == nil {
		t.Fatal("worker built without bound proxy credentials")
	}
	foreign := x.op.DeepCopy()
	foreign.Spec.ProxyCredentialBinding.RequestID = "sha256:" + strings.Repeat("9", 64)
	if _, err := buildMigrationWorkerJob(x.kube.Scheme(), foreign, x.upload, "operator:test", "worker", "archive"); err == nil {
		t.Fatal("worker built with credentials bound to another request")
	}
}

func TestOnboardingWorkerRBACIsMinimal(t *testing.T) {
	x := newOnboardingStageFixture(t)
	for _, request := range []ha.MigrationExecutionRequest{x.upload, x.restore} {
		_, role, binding, err := BuildOnboardingWorkerRBAC(x.kube.Scheme(), x.op, request, "worker")
		if err != nil {
			t.Fatalf("%s: %v", request.Stage, err)
		}
		if binding.RoleRef.Name != role.Name {
			t.Fatal("binding does not reference its role")
		}
		secrets := 0
		for _, rule := range role.Rules {
			for _, resource := range rule.Resources {
				if resource == "secrets" {
					secrets++
					if request.Stage != "restore" || len(rule.ResourceNames) != 1 || rule.ResourceNames[0] != request.RecoveryKey.Name || len(rule.Verbs) != 1 || rule.Verbs[0] != "get" {
						t.Fatalf("%s may read secrets beyond the pinned recovery key: %#v", request.Stage, rule)
					}
				}
				// The only write allowed is the worker's own append-only POST
				// permit on exactly this operation; nothing may delete history.
				if resource == "homeassistantmigrations" && (len(rule.ResourceNames) != 1 || rule.ResourceNames[0] != x.op.Name ||
					len(rule.Verbs) != 2 || rule.Verbs[0] != "get" || rule.Verbs[1] != "update") {
					t.Fatalf("%s migration permission is not limited to its permit: %#v", request.Stage, rule)
				}
			}
			if len(rule.ResourceNames) == 0 && !(len(rule.Verbs) == 1 && rule.Verbs[0] == "create" && rule.Resources[0] == "configmaps") {
				t.Fatalf("%s rule is not limited to named resources: %#v", request.Stage, rule)
			}
		}
		if (request.Stage == "restore") != (secrets == 1) {
			t.Fatalf("%s secret rule count = %d", request.Stage, secrets)
		}
	}
	unaccepted := x.restore
	unaccepted.RequestID = "sha256:" + strings.Repeat("8", 64)
	if _, _, _, err := BuildOnboardingWorkerRBAC(x.kube.Scheme(), x.op, unaccepted, "worker"); err == nil {
		t.Fatal("RBAC built for an unaccepted request")
	}
	backup := x.upload
	backup.Stage = "backup"
	if _, _, _, err := BuildOnboardingWorkerRBAC(x.kube.Scheme(), x.op, backup, "worker"); err == nil {
		t.Fatal("onboarding RBAC built for the backup stage")
	}
}

func newOnboardingQualification(x *onboardingStageFixture) *migrationBackupQualification {
	return &migrationBackupQualification{
		migrationWorkerObserver: migrationWorkerObserver{reader: x.kube}, kube: x.kube, scheme: x.kube.Scheme(),
		options: BackupQualificationOptions{Image: "operator:test", ServiceAccount: "worker", VerifyIsolation: func(context.Context, *ha.HomeAssistantMigration) error { return nil }},
	}
}

func writeOnboardingReceipt(t *testing.T, x *onboardingStageFixture, request ha.MigrationExecutionRequest, phase string) {
	t.Helper()
	receipt := migration.OnboardingReceipt{Version: "v1", OperationUID: string(x.op.UID), RequestID: request.RequestID, Stage: request.Stage, UploadRequestID: request.UploadRequestID, ArchiveDigest: request.ArchiveDigest, BackupID: "backup", AgentID: "backup.local", ScopeDigest: request.ScopeDigest, Phase: phase}
	data, _ := json.Marshal(receipt)
	yes := true
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: migration.OnboardingReceiptName(request.RequestID), Namespace: x.op.Namespace, OwnerReferences: []metav1.OwnerReference{{APIVersion: ha.GroupVersion.String(), Kind: "HomeAssistantMigration", Name: x.op.Name, UID: x.op.UID, Controller: &yes}}}, Data: map[string]string{"receipt.json": string(data)}}
	if err := x.kube.Create(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
}

func TestObserveOnboardingNeverInfersSuccessFromAcceptance(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, phase, stage string
		wantPhase, want    string
		verified           bool
	}{
		{"verified upload", "Verified", "upload", "RestoreInProgress", "UploadVerified", true},
		{"accepted restore stays unverified", "Accepted", "restore", "RestoreInProgress", "ExternalJobAccepted", false},
		{"abandoned dispatch", "Dispatching", "upload", "Unknown", "DispatchOutcomeUnknown", false},
		{"lost response", "OutcomeUnknown", "restore", "Unknown", "ReceiptPhaseInvalid", false},
		{"rejected", "Rejected", "upload", "Unknown", "ReceiptPhaseInvalid", false},
		{"mismatched evidence", "EvidenceMismatch", "upload", "Unknown", "ReceiptPhaseInvalid", false},
		{"restore cannot be verified by an upload receipt", "Verified", "restore", "Unknown", "ReceiptPhaseInvalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newOnboardingStageFixture(t)
			request := x.upload
			if tc.stage == "restore" {
				request = x.restore
			}
			writeOnboardingReceipt(t, x, request, tc.phase)
			observed, err := newOnboardingQualification(x).Observe(ctx, x.op, request)
			if err != nil {
				t.Fatal(err)
			}
			phase, reason := migrationObservedStage(request, observed)
			if phase != tc.wantPhase || reason != tc.want || observed.Verified != tc.verified {
				t.Fatalf("phase=%s reason=%s verified=%v", phase, reason, observed.Verified)
			}
			if tc.stage == "restore" && (observed.AccessVerified || observed.ResultDigest != "") {
				t.Fatal("restore acceptance was reported as verified access or data")
			}
			if tc.verified && (observed.ArchiveDigest != request.ArchiveDigest || observed.ScopeDigest != request.ScopeDigest || observed.VerificationDigest != request.VerificationDigest) {
				t.Fatal("verified upload did not carry the accepted request bindings")
			}
		})
	}
	x := newOnboardingStageFixture(t)
	observed, err := newOnboardingQualification(x).Observe(ctx, x.op, x.upload)
	if err != nil {
		t.Fatal(err)
	}
	if phase, reason := migrationObservedStage(x.upload, observed); phase != "Unknown" || reason != "DispatchOutcomeUnknown" {
		t.Fatalf("missing receipt = %s/%s, want Unknown", phase, reason)
	}
}

func TestStartOnboardingRefusesBeforeAnyAttemptMarker(t *testing.T) {
	ctx := context.Background()
	x := newOnboardingStageFixture(t)
	q := newOnboardingQualification(x)
	assertNothingStarted := func(t *testing.T) {
		t.Helper()
		current := &ha.HomeAssistantMigration{}
		if err := x.kube.Get(ctx, client.ObjectKeyFromObject(x.op), current); err != nil || len(current.Spec.ExecutionAttempts) != 0 {
			t.Fatalf("attempt marker written despite refusal: %v %v", current.Spec.ExecutionAttempts, err)
		}
		jobs := &batchv1.JobList{}
		if err := x.kube.List(ctx, jobs); err != nil || len(jobs.Items) != 0 {
			t.Fatalf("worker created despite refusal: %d %v", len(jobs.Items), err)
		}
	}
	// The target and its policy do not exist yet.
	if err := q.Start(ctx, x.op, x.upload); !errors.Is(err, errMigrationStartPrecondition) {
		t.Fatalf("missing target = %v", err)
	}
	assertNothingStarted(t)

	q.options.VerifyIsolation = func(context.Context, *ha.HomeAssistantMigration) error { return errors.New("not enforced") }
	if err := q.Start(ctx, x.op, x.upload); !errors.Is(err, errMigrationStartPrecondition) {
		t.Fatalf("unverified isolation = %v", err)
	}
	assertNothingStarted(t)
	q.options.VerifyIsolation = func(context.Context, *ha.HomeAssistantMigration) error { return nil }

	stopped := x.op.DeepCopy()
	for i := range stopped.Spec.Approvals {
		if stopped.Spec.Approvals[i].ID == "source-stop" {
			stopped.Spec.Approvals[i].Approved = false
		}
	}
	if err := x.kube.Update(ctx, stopped); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(ctx, x.op, x.upload); !errors.Is(err, errMigrationStartPrecondition) {
		t.Fatalf("revoked source stop = %v", err)
	}
	assertNothingStarted(t)
}

func TestStartOnboardingStartsEachRequestOnce(t *testing.T) {
	ctx := context.Background()
	x := newOnboardingStageFixture(t)
	if err := x.kube.Create(ctx, x.target); err != nil {
		t.Fatal(err)
	}
	yes, replicas := true, int32(1)
	selector := migrationTargetSelector(string(x.target.UID))
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: x.target.Name, Namespace: x.target.Namespace, OwnerReferences: []metav1.OwnerReference{{APIVersion: hav1.GroupVersion.String(), Kind: "HomeAssistant", Name: x.target.Name, UID: x.target.UID, Controller: &yes}}}, Spec: appsv1.StatefulSetSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "target"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "target", migrationTargetLabel: selector}}}}}
	policy, err := buildBoundMigrationTargetPolicy(x.kube.Scheme(), x.op, x.target, x.upload)
	if err != nil {
		t.Fatal(err)
	}
	policy.UID = "policy-uid" // the fake client does not allocate UIDs.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: x.target.Name + "-0", Namespace: x.target.Namespace, Labels: map[string]string{"app": "target", migrationTargetLabel: selector}}, Status: corev1.PodStatus{
		Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		ContainerStatuses: []corev1.ContainerStatus{{Name: "migration-onboarding-proxy", Ready: true}}}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "archive", Namespace: x.op.Namespace, UID: "pvc-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: ha.GroupVersion.String(), Kind: "HomeAssistantMigration", Name: x.op.Name, UID: x.op.UID, Controller: &yes}}}}
	for _, object := range []client.Object{sts, policy, pod, pvc} {
		if err := x.kube.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	q := newOnboardingQualification(x)

	if err := q.Start(ctx, x.op, x.restore); !errors.Is(err, errMigrationStartPrecondition) {
		t.Fatalf("restore started without a verified upload receipt: %v", err)
	}
	if err := q.Start(ctx, x.op, x.upload); err != nil {
		t.Fatalf("upload start: %v", err)
	}
	job := &batchv1.Job{}
	if err := x.kube.Get(ctx, client.ObjectKey{Namespace: x.op.Namespace, Name: migrationWorkerJobName(x.upload.RequestID)}, job); err != nil {
		t.Fatal(err)
	}
	if !q.boundOnboardingJob(job, x.op, x.upload) {
		t.Fatal("started upload worker does not match the observer's binding")
	}
	if err := q.Start(ctx, x.op, x.upload); err == nil {
		t.Fatal("upload attempt was started twice")
	}
	jobs := &batchv1.JobList{}
	if err := x.kube.List(ctx, jobs); err != nil || len(jobs.Items) != 1 {
		t.Fatalf("jobs after repeated start = %d %v", len(jobs.Items), err)
	}

	writeOnboardingReceipt(t, x, x.upload, "Verified")
	if err := x.kube.Get(ctx, client.ObjectKeyFromObject(x.op), x.op); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(ctx, x.op, x.restore); err != nil {
		t.Fatalf("restore start after verified upload: %v", err)
	}
	if err := x.kube.Get(ctx, client.ObjectKeyFromObject(x.op), x.op); err != nil || len(x.op.Spec.ExecutionAttempts) != 2 {
		t.Fatalf("attempt markers = %v %v", x.op.Spec.ExecutionAttempts, err)
	}
	observed, err := q.Observe(ctx, x.op, x.restore)
	if err != nil || !observed.Active || observed.Phase != "Dispatching" {
		t.Fatalf("running restore worker = %#v %v", observed, err)
	}
	if phase, reason := migrationObservedStage(x.restore, observed); phase != "RestoreInProgress" || reason != "DispatchInProgress" {
		t.Fatalf("running restore = %s/%s", phase, reason)
	}
}

type autoUIDClient struct{ client.Client }

// The fake API server does not allocate UIDs, which the isolation code requires.
func (c autoUIDClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if obj.GetUID() == "" {
		obj.SetUID(types.UID("uid-" + obj.GetName()))
	}
	return c.Client.Create(ctx, obj, opts...)
}

type verifiedBackupObserver struct {
	*migrationBackupQualification
	backup migrationStageObservation
}

func (o verifiedBackupObserver) Observe(ctx context.Context, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) (migrationStageObservation, error) {
	if r.Stage == "backup" {
		return o.backup, nil
	}
	return o.migrationBackupQualification.Observe(ctx, op, r)
}

func TestReconcilerPreparesIsolationThenStartsOnlyWhenTargetIsReady(t *testing.T) {
	ctx := context.Background()
	x := newOnboardingStageFixtureWith(t, false)
	backup, err := acceptBackupRequest(ctx, x.kube, x.kube, x.op)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.kube.Get(ctx, client.ObjectKeyFromObject(x.op), x.op); err != nil {
		t.Fatal(err)
	}
	x.op.Spec.ExecutionAttempts = append(x.op.Spec.ExecutionAttempts, ha.MigrationExecutionAttempt{Stage: "backup", RequestID: backup.RequestID})
	x.op.Finalizers = []string{migrationOperationFinalizer}
	if err := x.kube.Update(ctx, x.op); err != nil {
		t.Fatal(err)
	}
	yes, zero := true, int32(0)
	selector := migrationTargetSelector(string(x.target.UID))
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: x.target.Name, Namespace: x.target.Namespace, OwnerReferences: []metav1.OwnerReference{{APIVersion: hav1.GroupVersion.String(), Kind: "HomeAssistant", Name: x.target.Name, UID: x.target.UID, Controller: &yes}}}, Spec: appsv1.StatefulSetSpec{Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "target"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "target", migrationTargetLabel: selector}}}}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "archive", Namespace: x.op.Namespace, UID: "pvc-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: ha.GroupVersion.String(), Kind: "HomeAssistantMigration", Name: x.op.Name, UID: x.op.UID, Controller: &yes}}}}

	// A client with a status subresource, populated from the prepared state.
	secrets, configMaps := &corev1.SecretList{}, &corev1.ConfigMapList{}
	if err := x.kube.List(ctx, secrets); err != nil {
		t.Fatal(err)
	}
	if err := x.kube.List(ctx, configMaps); err != nil {
		t.Fatal(err)
	}
	objects := []client.Object{x.op.DeepCopy(), x.target, sts, pvc}
	for i := range secrets.Items {
		objects = append(objects, &secrets.Items[i])
	}
	for i := range configMaps.Items {
		objects = append(objects, &configMaps.Items[i])
	}
	kube := fakeWithStatus(x.kube.Scheme(), objects...)
	x.kube = kube
	q := newOnboardingQualification(x)
	q.kube = autoUIDClient{kube}
	observer := verifiedBackupObserver{migrationBackupQualification: q, backup: migrationStageObservation{
		OperationUID: string(x.op.UID), RequestID: backup.RequestID, Stage: "backup", SourceID: x.op.Spec.SourceID, TargetUID: x.op.Spec.Target.UID,
		Phase: "Verified", Verified: true, JobID: "job", ArchiveDigest: x.upload.ArchiveDigest, ArchiveRef: x.upload.ArchiveRef, ArchivePVCUID: x.upload.ArchivePVCUID,
		ScopeDigest: backup.ScopeDigest, VerificationDigest: backup.VerificationDigest,
	}}
	reconciler := &HomeAssistantMigrationReconciler{Client: autoUIDClient{kube}, APIReader: kube, Scheme: kube.Scheme(), stageObserver: observer}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(x.op)}
	reload := func() *ha.HomeAssistantMigration {
		current := &ha.HomeAssistantMigration{}
		if err := kube.Get(ctx, client.ObjectKeyFromObject(x.op), current); err != nil {
			t.Fatal(err)
		}
		return current
	}
	hasUploadAttempt := func(op *ha.HomeAssistantMigration) bool {
		for _, a := range op.Spec.ExecutionAttempts {
			if a.Stage == "upload" {
				return true
			}
		}
		return false
	}

	// 1: policy and credentials are created and bound; nothing is started.
	result, err := reconciler.Reconcile(ctx, request)
	if err != nil || result.RequeueAfter != time.Second {
		t.Fatalf("isolation preparation = %v %v", result, err)
	}
	current := reload()
	if current.Spec.ProxyCredentialBinding == nil || hasUploadAttempt(current) {
		t.Fatalf("binding=%v attempts=%v", current.Spec.ProxyCredentialBinding, current.Spec.ExecutionAttempts)
	}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: x.op.Namespace, Name: "migration-target-" + selector}, &networkingv1.NetworkPolicy{}); err != nil {
		t.Fatal("target policy was not created", err)
	}

	// 2: the target is not running, which is a refusal rather than an unknown outcome.
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	current = reload()
	if current.Status.Phase != "Blocked" || hasUploadAttempt(current) {
		t.Fatalf("phase=%s attempts=%v", current.Status.Phase, current.Spec.ExecutionAttempts)
	}
	condition := current.Status.Conditions[len(current.Status.Conditions)-1]
	if condition.Reason != "OnboardingStartPreconditionUnmet" {
		t.Fatalf("reason = %s", condition.Reason)
	}
	jobs := &batchv1.JobList{}
	if err := kube.List(ctx, jobs); err != nil || len(jobs.Items) != 0 {
		t.Fatalf("worker created before the target was ready: %d %v", len(jobs.Items), err)
	}

	// 3: with the proxy ready exactly one upload worker is started.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: x.target.Name + "-0", Namespace: x.target.Namespace, Labels: map[string]string{"app": "target", migrationTargetLabel: selector}}, Status: corev1.PodStatus{
		Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		ContainerStatuses: []corev1.ContainerStatus{{Name: "migration-onboarding-proxy", Ready: true}}}}
	if err := kube.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if result, err = reconciler.Reconcile(ctx, request); err != nil || result.RequeueAfter != time.Second {
		t.Fatalf("start = %v %v", result, err)
	}
	if current = reload(); !hasUploadAttempt(current) {
		t.Fatal("attempt marker was not persisted before the worker")
	}
	if err := kube.List(ctx, jobs); err != nil || len(jobs.Items) != 1 {
		t.Fatalf("workers = %d %v", len(jobs.Items), err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := kube.List(ctx, jobs); err != nil || len(jobs.Items) != 1 {
		t.Fatalf("a second reconcile started another worker: %d %v", len(jobs.Items), err)
	}
}
