package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	migrationpkg "github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
)

func bindTestVerification(t *testing.T, operation *hav1alpha1.HomeAssistantMigration) {
	t.Helper()
	operation.Spec.Verification = []hav1alpha1.MigrationVerificationItem{{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: "sha256:" + strings.Repeat("a", 64)}}
	digest, err := migrationprotocol.VerificationDigest(operation.Spec.Verification)
	if err != nil {
		t.Fatal(err)
	}
	for i := range operation.Spec.Approvals {
		operation.Spec.Approvals[i].VerificationDigest = digest
	}
}

func TestMigrationObservesBackupDispatchReceiptAfterRestart(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target"}}
	operation := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: "default", UID: "operation", Finalizers: []string{migrationOperationFinalizer}},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "target"}}}
	claim := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: migrationpkg.BackupDispatchReceiptName("operation"), Namespace: "default"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(operation).WithObjects(operation, target, claim).Build()
	r := &HomeAssistantMigrationReconciler{Client: kube, Scheme: scheme}
	key := client.ObjectKeyFromObject(operation)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), key, stored); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(stored.Status.Conditions, "Progressing")
	if stored.Status.Phase != "Unknown" || condition == nil || condition.Reason != "LegacyExecutionEvidence" {
		t.Fatalf("phase=%s condition=%#v", stored.Status.Phase, condition)
	}
}

func TestMigrationLegacyProtocolRemainsBlockedAfterEvidenceRemoval(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	op := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default", UID: "op", Finalizers: []string{migrationOperationFinalizer}, Annotations: map[string]string{migrationRequestBackupAnnotation: "old-evidence"}}, Spec: hav1alpha1.HomeAssistantMigrationSpec{SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(op).WithObjects(op).Build()
	r := &HomeAssistantMigrationReconciler{Client: kube, Scheme: scheme}
	key := client.ObjectKeyFromObject(op)
	for _, reason := range []string{"LegacyExecutionEvidence", "LegacyExecutionProtocol"} {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		stored := &hav1alpha1.HomeAssistantMigration{}
		if err := kube.Get(context.Background(), key, stored); err != nil {
			t.Fatal(err)
		}
		condition := meta.FindStatusCondition(stored.Status.Conditions, "Progressing")
		if condition == nil || condition.Reason != reason {
			t.Fatalf("condition=%#v, want %s", condition, reason)
		}
		stored.Annotations = nil
		if err := kube.Update(context.Background(), stored); err != nil {
			t.Fatal(err)
		}
	}
}

type failingSecretReader struct{ client.Reader }

func (r failingSecretReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Secret); ok {
		return errors.New("private-token in upstream error")
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestMigrationWaitingPhaseIsFailClosed(t *testing.T) {
	migration := &hav1alpha1.HomeAssistantMigration{}
	for _, test := range []struct {
		name        string
		approvals   []hav1alpha1.MigrationApproval
		annotations map[string]string
		want        string
	}{
		{"backup approval", nil, nil, "AwaitingBackupApproval"},
		{"backup executor unavailable", []hav1alpha1.MigrationApproval{{ID: "backup", Approved: true}}, nil, "Blocked"},
		{"unbound restore", []hav1alpha1.MigrationApproval{{ID: "backup", Approved: true}, {ID: "restore", Approved: true}}, nil, "Blocked"},
		{"execution without bound restore", []hav1alpha1.MigrationApproval{{ID: "backup", Approved: true}, {ID: "restore", Approved: true}, {ID: "source-stop", Approved: true}}, nil, "Blocked"},
		{"unknown is never retried", nil, map[string]string{"ha.homeassistant.io/migration-outcome": "unknown"}, "Unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			migration.Spec.Approvals = test.approvals
			migration.Annotations = test.annotations
			got, _ := migrationWaitingPhase(migration)
			if got != test.want {
				t.Fatalf("phase = %s, want %s", got, test.want)
			}
		})
	}
}

func TestCancellationPreventsPreparingTargetStorage(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	op := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "cancelled", Namespace: "default", UID: "operation", Finalizers: []string{migrationOperationFinalizer}}, Spec: hav1alpha1.HomeAssistantMigrationSpec{CancelRequested: true, SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "missing", UID: "target"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(op).WithObjects(op).Build()
	r := &HomeAssistantMigrationReconciler{Client: kube, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(op)}); err != nil {
		t.Fatal(err)
	}
	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(op), stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != "Blocked" {
		t.Fatalf("phase = %q", stored.Status.Phase)
	}
}

func TestMigrationReportsMissingBackupExecutorBeforeRestoreApproval(t *testing.T) {
	migration := &hav1alpha1.HomeAssistantMigration{
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			ReportID: "report", BackupScopeDigest: "scope", Target: hav1alpha1.MigrationTarget{UID: "uid"},
			Approvals:            []hav1alpha1.MigrationApproval{{ID: "backup", Digest: "report", ScopeDigest: "scope", TargetUID: "uid", Approved: true}},
			SourceTokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "source-token"}, Key: "token"},
		},
	}
	phase, reason := migrationWaitingPhase(migration)
	if phase != "Blocked" || reason != "BackupExecutorUnavailable" {
		t.Fatalf("phase=%s reason=%s", phase, reason)
	}
}

func TestMigrationReportsMissingSourceTokenReference(t *testing.T) {
	migration := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{
		ReportID: "report", BackupScopeDigest: "scope", Target: hav1alpha1.MigrationTarget{UID: "uid"},
		Approvals: []hav1alpha1.MigrationApproval{{ID: "backup", Digest: "report", ScopeDigest: "scope", TargetUID: "uid", Approved: true}},
	}}
	phase, reason := migrationWaitingPhase(migration)
	if phase != "Blocked" || reason != "SourceTokenReferenceMissing" {
		t.Fatalf("phase=%s reason=%s", phase, reason)
	}
}

func TestMigrationBackupApprovalRequiresExactReportAndTarget(t *testing.T) {
	migration := &hav1alpha1.HomeAssistantMigration{Spec: hav1alpha1.HomeAssistantMigrationSpec{
		ReportID: "sha256:report", BackupScopeDigest: "scope", Target: hav1alpha1.MigrationTarget{UID: "target"},
	}}
	approval := hav1alpha1.MigrationApproval{
		ID: "backup", Approved: true, Digest: "sha256:report", ScopeDigest: "scope", TargetUID: "target",
	}
	if !backupApprovalBound(migration, &approval) {
		t.Fatal("exact backup approval rejected")
	}
	for _, mutate := range []func(*hav1alpha1.MigrationApproval){
		func(a *hav1alpha1.MigrationApproval) { a.Approved = false },
		func(a *hav1alpha1.MigrationApproval) { a.Digest = "other-report" },
		func(a *hav1alpha1.MigrationApproval) { a.TargetUID = "other-target" },
		func(a *hav1alpha1.MigrationApproval) { a.ScopeDigest = "other-scope" },
	} {
		changed := approval
		mutate(&changed)
		if backupApprovalBound(migration, &changed) {
			t.Fatal("incorrectly bound backup approval accepted")
		}
	}
	migration.Spec.ReportID = ""
	if backupApprovalBound(migration, &approval) {
		t.Fatal("backup approval accepted without a report")
	}
}

func TestMigrationReportsUnavailableSourceTokenWithoutLeakingValues(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "uid"}}
	operation := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "operation", Namespace: "default", Finalizers: []string{migrationOperationFinalizer}}, Spec: hav1alpha1.HomeAssistantMigrationSpec{
		ExecutionProtocol: "v1", SourceID: "source", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"}, ReportID: "report", BackupScopeDigest: "scope",
		SourceTokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "source-token"}, Key: "token"},
		Approvals:            []hav1alpha1.MigrationApproval{{ID: "backup", Digest: "report", ScopeDigest: "scope", TargetUID: "uid", Approved: true}},
	}}
	bindTestVerification(t, operation)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(operation).WithObjects(operation, target).Build()
	r := &HomeAssistantMigrationReconciler{Client: kube, Scheme: scheme}
	key := client.ObjectKeyFromObject(operation)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), key, stored); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(stored.Status.Conditions, "Progressing")
	if condition == nil || condition.Reason != "SourceTokenUnavailable" {
		t.Fatalf("condition=%#v", condition)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "source-token", Namespace: "default"}, Data: map[string][]byte{"token": []byte("private-token")}}
	if err := kube.Create(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), key, stored); err != nil {
		t.Fatal(err)
	}
	condition = meta.FindStatusCondition(stored.Status.Conditions, "Progressing")
	if condition == nil || condition.Reason != "BackupExecutorUnavailable" || strings.Contains(condition.Message, "private-token") {
		t.Fatalf("condition=%#v", condition)
	}
	r.APIReader = failingSecretReader{Reader: kube}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("Secret read failure must be retried without exposing its details: %v", err)
	}
}

func TestMigrationReconstructsConditionsWhenPhaseIsUnchanged(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "uid"},
	}
	migration := &hav1alpha1.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "migration",
			Namespace:  "default",
			Generation: 3,
			Finalizers: []string{migrationOperationFinalizer},
		},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			ExecutionProtocol: "v1", SourceID: "source", ReportID: "report",
			Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"},
		},
		Status: hav1alpha1.HomeAssistantMigrationStatus{Phase: "AwaitingBackupApproval"},
	}
	bindTestVerification(t, migration)
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(migration).
		WithObjects(target, migration).
		Build()
	reconciler := &HomeAssistantMigrationReconciler{Client: kube, Scheme: scheme}
	key := client.ObjectKeyFromObject(migration)
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), key, stored); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(stored.Status.Conditions, "Progressing")
	if condition == nil || condition.Reason != "AwaitingBackupApproval" ||
		condition.ObservedGeneration != 3 {
		t.Fatalf("condition=%#v", condition)
	}
}

func TestMigrationReceiptBindsGenerationAndTarget(t *testing.T) {
	encoded, err := json.Marshal(
		migrationReceipt{
			Generation: 4,
			SourceID:   "source",
			TargetUID:  "target-uid",
			Stage:      "approved",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var receipt migrationReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Generation != 4 || receipt.SourceID != "source" ||
		receipt.TargetUID != "target-uid" ||
		receipt.Stage != "approved" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
}

func TestMigrationReceiptRejectsRestartWithChangedIntentOrMalformedData(t *testing.T) {
	migration := &hav1alpha1.HomeAssistantMigration{}
	migration.Generation = 4
	migration.Spec.SourceID = "source"
	migration.Spec.Target.UID = "target-uid"
	migration.Annotations = map[string]string{
		migrationReceiptAnnotation: `{"generation":4,"sourceID":"source","targetUID":"target-uid","stage":"approved"}`,
	}
	if err := validateMigrationExecutionReceipt(migration); err != nil {
		t.Fatal(err)
	}
	// A new generation after persisted intent must not reuse the old receipt.
	migration.Generation = 5
	if err := validateMigrationExecutionReceipt(migration); err == nil {
		t.Fatal("stale receipt was accepted after restart")
	}
	migration.Generation = 4
	migration.Spec.SourceID = "another-source"
	if err := validateMigrationExecutionReceipt(migration); err == nil {
		t.Fatal("receipt was accepted for a changed source")
	}
	migration.Annotations[migrationReceiptAnnotation] = "not-json"
	if err := validateMigrationExecutionReceipt(migration); err == nil {
		t.Fatal("malformed receipt was accepted")
	}
}

// reloadVerificationOperation refreshes the fixture's operation from the fake
// API server so approvals and status updates use the latest resourceVersion.
func reloadVerificationOperation(t *testing.T, fixture *onboardingStageFixture) *hav1alpha1.HomeAssistantMigration {
	t.Helper()
	current := &hav1alpha1.HomeAssistantMigration{}
	if err := fixture.kube.Get(context.Background(), client.ObjectKeyFromObject(fixture.op), current); err != nil {
		t.Fatal(err)
	}
	fixture.op = current
	return current
}

func verificationRequestForStage(t *testing.T, op *hav1alpha1.HomeAssistantMigration, stage string) hav1alpha1.MigrationExecutionRequest {
	t.Helper()
	for _, request := range op.Spec.ExecutionRequests {
		if request.Stage == stage {
			return request
		}
	}
	t.Fatalf("%s request missing", stage)
	return hav1alpha1.MigrationExecutionRequest{}
}

func reconcileVerification(t *testing.T, fixture *onboardingStageFixture, r *HomeAssistantMigrationReconciler) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fixture.op)}); err != nil {
		t.Fatal(err)
	}
}

func verificationResultDigest(t *testing.T, qualification *migrationBackupQualification, op *hav1alpha1.HomeAssistantMigration) string {
	t.Helper()
	restore := verificationRequestForStage(t, op, "restore")
	observed, err := qualification.Observe(context.Background(), op, restore)
	if err != nil {
		t.Fatal(err)
	}
	if !observed.AccessVerified || !migrationprotocol.ValidDigest(observed.ResultDigest) {
		t.Fatalf("restore access not verified: access=%v digest=%q", observed.AccessVerified, observed.ResultDigest)
	}
	return observed.ResultDigest
}

// addVerificationApproval reloads the operation, replaces or appends a consent
// bound to the restore request.
func addVerificationApproval(t *testing.T, fixture *onboardingStageFixture, id, digest string) {
	t.Helper()
	op := reloadVerificationOperation(t, fixture)
	restore := verificationRequestForStage(t, op, "restore")
	approval := hav1alpha1.MigrationApproval{
		ID: id, Approved: true, Digest: digest, TargetUID: op.Spec.Target.UID,
		ArchiveDigest: restore.ArchiveDigest, VerificationDigest: restore.VerificationDigest,
	}
	replaced := false
	for i := range op.Spec.Approvals {
		if op.Spec.Approvals[i].ID == id {
			op.Spec.Approvals[i] = approval
			replaced = true
			break
		}
	}
	if !replaced {
		op.Spec.Approvals = append(op.Spec.Approvals, approval)
	}
	if err := fixture.kube.Update(context.Background(), op); err != nil {
		t.Fatal(err)
	}
}

// TestMigrationVerificationRequiresResumeAndManualCutoverApprovals proves that
// HTTP acceptance (2xx) and Pod readiness are not completion evidence. The
// phase stays AwaitingResumeApproval until a resume consent binds the verified
// result digest, then AwaitingManualCutover until a separate cutover consent.
func TestMigrationVerificationRequiresResumeAndManualCutoverApprovals(t *testing.T) {
	ctx := context.Background()
	haServer := newFakeVerificationHomeAssistant(t)
	fixture, qualification := newVerificationFixture(t, haServer)
	backup := verificationRequestForStage(t, fixture.op, "backup")
	upload := verificationRequestForStage(t, fixture.op, "upload")
	observer := verificationObserver(qualification, fixture.op, &backup, &upload)
	r := &HomeAssistantMigrationReconciler{Client: fixture.kube, APIReader: fixture.kube, Scheme: fixture.kube.Scheme(), stageObserver: observer}

	// A ready target Pod next to an accepted restore is still not evidence.
	ready := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: fixture.target.Name + "-0", Namespace: fixture.target.Namespace, Labels: map[string]string{migrationTargetLabel: migrationTargetSelector(string(fixture.target.UID))}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	if err := fixture.kube.Create(ctx, ready); err != nil {
		t.Fatal(err)
	}

	reconcileVerification(t, fixture, r)
	op := reloadVerificationOperation(t, fixture)
	if op.Status.Phase != "AwaitingResumeApproval" {
		t.Fatalf("phase after accepted restore and ready Pod = %q, want AwaitingResumeApproval", op.Status.Phase)
	}
	progressing := meta.FindStatusCondition(op.Status.Conditions, "Progressing")
	if progressing == nil || progressing.Reason != "ResumeApprovalRequired" {
		t.Fatalf("resume condition = %#v", progressing)
	}

	digest := verificationResultDigest(t, qualification, op)
	if !strings.Contains(progressing.Message, digest) {
		t.Fatalf("condition message %q does not expose result digest %q", progressing.Message, digest)
	}

	// A resume consent bound to a different digest must not release the gate.
	addVerificationApproval(t, fixture, "resume", "sha256:"+strings.Repeat("0", 64))
	reconcileVerification(t, fixture, r)
	op = reloadVerificationOperation(t, fixture)
	if op.Status.Phase != "AwaitingResumeApproval" {
		t.Fatalf("phase with mismatched resume digest = %q, want AwaitingResumeApproval", op.Status.Phase)
	}

	// The correctly bound resume consent advances to manual cutover only.
	addVerificationApproval(t, fixture, "resume", digest)
	reconcileVerification(t, fixture, r)
	op = reloadVerificationOperation(t, fixture)
	if op.Status.Phase != "AwaitingManualCutover" {
		t.Fatalf("phase with bound resume consent = %q, want AwaitingManualCutover", op.Status.Phase)
	}

	// A separate manual cutover consent is required for completion.
	addVerificationApproval(t, fixture, "manual-cutover", "sha256:"+strings.Repeat("1", 64))
	reconcileVerification(t, fixture, r)
	op = reloadVerificationOperation(t, fixture)
	if op.Status.Phase != "Completed" {
		t.Fatalf("phase with resume and cutover consent = %q, want Completed", op.Status.Phase)
	}
	progressing = meta.FindStatusCondition(op.Status.Conditions, "Progressing")
	if progressing == nil || progressing.Reason != "MigrationVerified" {
		t.Fatalf("completed condition = %#v", progressing)
	}
}

// TestMigrationVerificationRestartStatusLossReproducesPhase proves that losing
// the status (as after a controller restart) recomputes the same phase from the
// durable operation, requests, receipts and consents instead of stale state.
func TestMigrationVerificationRestartStatusLossReproducesPhase(t *testing.T) {
	ctx := context.Background()
	haServer := newFakeVerificationHomeAssistant(t)
	fixture, qualification := newVerificationFixture(t, haServer)
	backup := verificationRequestForStage(t, fixture.op, "backup")
	upload := verificationRequestForStage(t, fixture.op, "upload")
	observer := verificationObserver(qualification, fixture.op, &backup, &upload)
	r := &HomeAssistantMigrationReconciler{Client: fixture.kube, APIReader: fixture.kube, Scheme: fixture.kube.Scheme(), stageObserver: observer}

	reconcileVerification(t, fixture, r)
	op := reloadVerificationOperation(t, fixture)
	if op.Status.Phase != "AwaitingResumeApproval" {
		t.Fatalf("initial phase = %q", op.Status.Phase)
	}
	digest := verificationResultDigest(t, qualification, op)

	// Simulate a restart that loses the persisted status.
	op.Status = hav1alpha1.HomeAssistantMigrationStatus{}
	if err := fixture.kube.Status().Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	reconcileVerification(t, fixture, r)
	op = reloadVerificationOperation(t, fixture)
	if op.Status.Phase != "AwaitingResumeApproval" {
		t.Fatalf("phase after status loss = %q, want AwaitingResumeApproval", op.Status.Phase)
	}

	addVerificationApproval(t, fixture, "resume", digest)
	addVerificationApproval(t, fixture, "manual-cutover", "sha256:"+strings.Repeat("1", 64))
	reconcileVerification(t, fixture, r)
	op = reloadVerificationOperation(t, fixture)
	if op.Status.Phase != "Completed" {
		t.Fatalf("phase with consents = %q, want Completed", op.Status.Phase)
	}

	// Losing status again must reproduce Completed, not regress to a gate.
	op.Status = hav1alpha1.HomeAssistantMigrationStatus{}
	if err := fixture.kube.Status().Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	reconcileVerification(t, fixture, r)
	op = reloadVerificationOperation(t, fixture)
	if op.Status.Phase != "Completed" {
		t.Fatalf("phase after second status loss = %q, want Completed", op.Status.Phase)
	}
}

// TestMigrationVerificationBlocksOnMissingTokenOrDataMismatch proves that a
// missing operator token or a projection mismatch leaves the operation before
// verification and never releases the resume gate.
func TestMigrationVerificationBlocksOnMissingTokenOrDataMismatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, fixture *onboardingStageFixture, fake *fakeVerificationHomeAssistant)
	}{
		{
			name: "missing source token",
			mutate: func(t *testing.T, fixture *onboardingStageFixture, _ *fakeVerificationHomeAssistant) {
				secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default"}}
				if err := fixture.kube.Delete(context.Background(), secret); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "projection mismatch",
			mutate: func(t *testing.T, _ *onboardingStageFixture, fake *fakeVerificationHomeAssistant) {
				fake.projection.Items[0].Summary = "tampered"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fake := newFakeVerificationHomeAssistant(t)
			fixture, qualification := newVerificationFixture(t, fake)
			backup := verificationRequestForStage(t, fixture.op, "backup")
			upload := verificationRequestForStage(t, fixture.op, "upload")
			observer := verificationObserver(qualification, fixture.op, &backup, &upload)
			r := &HomeAssistantMigrationReconciler{Client: fixture.kube, APIReader: fixture.kube, Scheme: fixture.kube.Scheme(), stageObserver: observer}
			tc.mutate(t, fixture, fake)
			reconcileVerification(t, fixture, r)
			op := reloadVerificationOperation(t, fixture)
			if op.Status.Phase == "AwaitingResumeApproval" || op.Status.Phase == "AwaitingManualCutover" || op.Status.Phase == "Completed" {
				t.Fatalf("verification failure released phase %q", op.Status.Phase)
			}
			passed, err := migrationVerificationAndResumePassed(ctx, fixture.kube, fixture.target)
			if err != nil {
				t.Fatal(err)
			}
			if passed {
				t.Fatal("verification failure released the resume gate")
			}
		})
	}
}

func TestMigrationReconcileBlocksUnprotectedOnboardingAndKeepsUnknownReceipt(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target-uid"},
		Spec: hav1.HomeAssistantSpec{
			Alpha: &hav1.AlphaSpec{
				MigrationRestore: &hav1.MigrationRestoreAlphaSpec{Enabled: true},
			},
		},
	}
	migration := &hav1alpha1.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "migration",
			Namespace:  "default",
			Finalizers: []string{migrationOperationFinalizer},
			Generation: 1,
		},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			ExecutionProtocol: "v1", SourceID: "source",
			Target:   hav1alpha1.MigrationTarget{Name: "target", UID: "target-uid"},
			ReportID: "report", BackupScopeDigest: "scope",
			Archive: &hav1alpha1.MigrationArchive{
				BackupID: "backup",
				AgentID:  "backup.local",
				Digest:   "sha256:archive",
			},
			RestoreScopeDigest: "sha256:scope",
			Approvals: []hav1alpha1.MigrationApproval{
				{ID: "backup", Digest: "report", ScopeDigest: "scope", TargetUID: "target-uid", Approved: true},
				{
					ID:            "restore",
					Digest:        "decision",
					Approved:      true,
					TargetUID:     "target-uid",
					ArchiveDigest: "sha256:archive",
					ScopeDigest:   "sha256:scope",
				},
				{ID: "source-stop", Digest: "confirmed", Approved: true},
			},
		},
	}
	bindTestVerification(t, migration)
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(migration).
		WithObjects(migration, target).
		Build()
	reconciler := &HomeAssistantMigrationReconciler{Client: kube, Scheme: scheme}
	request := ctrl.Request{
		NamespacedName: client.ObjectKey{Namespace: "default", Name: "migration"},
	}
	first, err := reconciler.Reconcile(context.Background(), request)
	if err != nil || first.RequeueAfter == 0 {
		t.Fatalf("receipt reconcile = %#v, %v", first, err)
	}
	persisted := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), request.NamespacedName, persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Annotations[migrationReceiptAnnotation] != "" ||
		persisted.Status.Phase != "Blocked" {
		t.Fatalf(
			"unprotected operation wrote a receipt or was not blocked: %#v",
			persisted.Status,
		)
	}
	condition := meta.FindStatusCondition(persisted.Status.Conditions, "Progressing")
	if condition == nil || condition.Reason != "OnboardingIsolationUnavailable" {
		t.Fatalf("condition = %#v", condition)
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "target-data"}, pvc); err != nil {
		t.Fatal(err)
	}
	if len(pvc.OwnerReferences) != 1 || pvc.OwnerReferences[0].UID != migration.UID {
		t.Fatal("restore PVC is not owned by migration")
	}
	// A receipt persisted by a previous executor is still ambiguous after a
	// restart; it must never become permission to submit the operation again.
	if persisted.Annotations == nil {
		persisted.Annotations = map[string]string{}
	}
	persisted.Annotations[migrationReceiptAnnotation] = `{"generation":1,"sourceID":"source","targetUID":"target-uid","stage":"approved"}`
	if err := kube.Update(context.Background(), persisted); err != nil {
		t.Fatal(err)
	}
	restarted := &HomeAssistantMigrationReconciler{Client: kube, Scheme: scheme}
	if _, err := restarted.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), request.NamespacedName, persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status.Phase != "Unknown" {
		t.Fatalf("phase after restart = %q, want Unknown", persisted.Status.Phase)
	}
	// Unknown or future stages are not proof of a completed operation either.
	persisted.Annotations[migrationReceiptAnnotation] = `{"generation":1,"sourceID":"source","targetUID":"target-uid","stage":"unexpected"}`
	if err := kube.Update(context.Background(), persisted); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), request.NamespacedName, persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status.Phase != "Unknown" {
		t.Fatalf("phase for unexpected receipt stage = %q, want Unknown", persisted.Status.Phase)
	}
	persisted.Spec.Approvals = nil
	if err := kube.Update(context.Background(), persisted); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), request.NamespacedName, persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status.Phase != "Unknown" {
		t.Fatalf("revoking approval hid the ambiguous outcome: %q", persisted.Status.Phase)
	}
}
