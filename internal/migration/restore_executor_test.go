package migration

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type onboardingFake struct {
	uploads, restores     int
	uploadErr, restoreErr error
	mismatch              bool
}

func (f *onboardingFake) UploadMigrationBackup(_ context.Context, _ io.Reader, _ string, _ int64, _ string) error {
	f.uploads++
	return f.uploadErr
}
func (f *onboardingFake) MigrationOnboardingBackupInfo(_ context.Context, e haclient.MigrationBackupExpectation) (*haclient.MigrationOnboardingBackupEvidence, error) {
	return &haclient.MigrationOnboardingBackupEvidence{BackupID: e.BackupID, AgentID: e.AgentID, Size: e.Size, Protected: !f.mismatch, DatabaseIncluded: e.RestoreDatabase, HomeAssistantIncluded: true, HomeAssistantVersion: "2026.11.0"}, nil
}
func (f *onboardingFake) RestoreMigrationOnboardingBackup(_ context.Context, _, _, _ string, _ bool) error {
	f.restores++
	return f.restoreErr
}

type restoreFixture struct {
	executor *RestoreExecutor
	fake     *onboardingFake
	upload   ha.MigrationExecutionRequest
	restore  ha.MigrationExecutionRequest
	archive  ArchiveDescriptor
}

// newRestoreFixture reproduces the real binding shape: the descriptor carries
// the backup request ID and backup scope; upload/restore carry restore scope.
// The attempt markers mirror what the controller must persist before it creates
// a worker, because a dispatch permit without one is rejected by the CRD.
func newRestoreFixture(t *testing.T, f *onboardingFake) *restoreFixture {
	t.Helper()
	return newRestoreFixtureWith(t, f, interceptor.Funcs{})
}

func newRestoreFixtureWith(t *testing.T, f *onboardingFake, funcs interceptor.Funcs) *restoreFixture {
	t.Helper()
	s := runtime.NewScheme()
	_ = ha.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	backupID := "sha256:" + strings.Repeat("a", 64)
	backupScope := "sha256:" + strings.Repeat("b", 64)
	upload := requestForRestoreTest("upload", "")
	restore := requestForRestoreTest("restore", upload.RequestID)
	op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "op", Namespace: "ns", UID: "op-uid"}}
	op.Spec.ExecutionProtocol = "v1"
	op.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{{Stage: "backup", RequestID: backupID, ScopeDigest: backupScope}, upload, restore}
	op.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{
		{Stage: "upload", RequestID: upload.RequestID}, {Stage: "restore", RequestID: restore.RequestID},
	}
	op.Spec.Archive = &ha.MigrationArchive{BackupID: "backup", AgentID: "agent", Digest: upload.ArchiveDigest}
	k := fake.NewClientBuilder().WithScheme(s).WithObjects(op).WithInterceptorFuncs(funcs).Build()
	archive := ArchiveDescriptor{
		Version: "v1", OperationUID: "op-uid", RequestID: backupID, SourceID: "source", BackupID: "backup", AgentID: "agent",
		PVCUID: "pvc", Filename: "backup.tar", Size: 7, Digest: upload.ArchiveDigest, ScopeDigest: backupScope,
		DatabaseIncluded: true, Protected: true,
		Validation: &ArchiveValidation{Profile: ArchiveValidationProfile, ValidatorVersion: "v1", CiphertextDigest: upload.ArchiveDigest, SourceEvidenceDigest: "sha256:" + strings.Repeat("1", 64), KeyRef: *restore.RecoveryKey, PayloadAuthenticated: true, StructureValid: true},
	}
	return &restoreFixture{executor: &RestoreExecutor{Client: k, Scheme: s, Operation: op, Onboarding: f}, fake: f, upload: upload, restore: restore, archive: archive}
}

func TestRestoreExecutorNeverRetriesOnboardingMutation(t *testing.T) {
	f := &onboardingFake{}
	x := newRestoreFixture(t, f)
	first, err := x.executor.Upload(context.Background(), x.upload, x.archive, bytes.NewBufferString("archive"), true)
	if err != nil || first.Phase != "Verified" || f.uploads != 1 {
		t.Fatalf("upload=%#v err=%v calls=%d", first, err, f.uploads)
	}
	second, err := x.executor.Upload(context.Background(), x.upload, x.archive, bytes.NewBufferString("archive"), true)
	if err != nil || second.Phase != "Verified" || f.uploads != 1 {
		t.Fatal("existing receipt retried upload")
	}
	accepted, err := x.executor.Restore(context.Background(), x.restore, x.archive, "key", true)
	if err != nil || accepted.Phase != "Accepted" || f.restores != 1 {
		t.Fatal("restore was not accepted once")
	}
	if _, err = x.executor.Restore(context.Background(), x.restore, x.archive, "key", true); err != nil || f.restores != 1 {
		t.Fatal("existing receipt retried restore")
	}
	read, err := ReadOnboardingReceipt(context.Background(), x.executor.Client, x.executor.Operation, x.restore)
	if err != nil || read.Phase != "Accepted" || read.Stage != "restore" {
		t.Fatalf("restart did not reconstruct the restore receipt: %#v %v", read, err)
	}
}

func TestRestoreExecutorPersistsUnknownWithoutRetry(t *testing.T) {
	f := &onboardingFake{uploadErr: &haclient.Error{Type: haclient.ErrorTypeOutcomeUnknown, Err: errors.New("lost")}}
	x := newRestoreFixture(t, f)
	got, err := x.executor.Upload(context.Background(), x.upload, x.archive, bytes.NewBufferString("x"), true)
	if err == nil || got.Phase != "OutcomeUnknown" || f.uploads != 1 {
		t.Fatal("unknown outcome was not durable")
	}
	got, err = x.executor.Upload(context.Background(), x.upload, x.archive, bytes.NewBufferString("x"), true)
	if err != nil || got.Phase != "OutcomeUnknown" || f.uploads != 1 {
		t.Fatal("unknown outcome retried")
	}
	if _, err := x.executor.Restore(context.Background(), x.restore, x.archive, "key", true); err == nil || f.restores != 0 {
		t.Fatal("restore ran after an unverified upload")
	}
}

func TestRestoreExecutorClaimsDurableDispatchPermitPerStage(t *testing.T) {
	f := &onboardingFake{}
	x := newRestoreFixture(t, f)
	if _, err := x.executor.Upload(context.Background(), x.upload, x.archive, bytes.NewBufferString("archive"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := x.executor.Restore(context.Background(), x.restore, x.archive, "key", true); err != nil {
		t.Fatal(err)
	}
	// Each destructive stage holds its own permit, so a lost upload receipt
	// cannot re-open the upload POST while the restore is already permitted.
	current := &ha.HomeAssistantMigration{}
	if err := x.executor.Client.Get(context.Background(), client.ObjectKeyFromObject(x.executor.Operation), current); err != nil {
		t.Fatal(err)
	}
	permits := map[string]string{}
	for _, marker := range current.Spec.ExecutionDispatches {
		permits[marker.Stage] = marker.RequestID
	}
	if len(permits) != 2 || permits["upload"] != x.upload.RequestID || permits["restore"] != x.restore.RequestID {
		t.Fatalf("dispatch permits=%#v", permits)
	}

	// Deleting every receipt is the lost-write case: the durable permit must
	// still forbid a second POST for either stage.
	for _, id := range []string{x.upload.RequestID, x.restore.RequestID} {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Namespace: x.executor.Operation.Namespace, Name: OnboardingReceiptName(id),
		}}
		if err := x.executor.Client.Delete(context.Background(), cm); err != nil {
			t.Fatal(err)
		}
	}
	fresh := &RestoreExecutor{Client: x.executor.Client, Scheme: x.executor.Scheme, Operation: x.executor.Operation, Onboarding: f}
	if _, err := fresh.Upload(context.Background(), x.upload, x.archive, bytes.NewBufferString("archive"), true); err == nil || f.uploads != 1 {
		t.Fatalf("lost receipt allowed a second upload POST: calls=%d err=%v", f.uploads, err)
	}
	if _, err := fresh.Restore(context.Background(), x.restore, x.archive, "key", true); err == nil || f.restores != 1 {
		t.Fatalf("lost receipt allowed a second restore POST: calls=%d err=%v", f.restores, err)
	}
}

func TestRestoreExecutorNeverPostsAfterDispatchClaimOutcomeUnknown(t *testing.T) {
	f := &onboardingFake{}
	x := newRestoreFixtureWith(t, f, interceptor.Funcs{
		Update: func(ctx context.Context, cw client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, isOperation := obj.(*ha.HomeAssistantMigration); isOperation {
				return errors.New("connection reset")
			}
			return cw.Update(ctx, obj, opts...)
		},
	})
	if _, err := x.executor.Upload(context.Background(), x.upload, x.archive, bytes.NewBufferString("archive"), true); err == nil || f.uploads != 0 {
		t.Fatalf("uncertain dispatch claim sent a POST: calls=%d err=%v", f.uploads, err)
	}
}

func TestRestoreExecutorDispatchRequiresItsAttemptMarker(t *testing.T) {
	x := newRestoreFixture(t, &onboardingFake{})
	op := &ha.HomeAssistantMigration{}
	if err := x.executor.Client.Get(context.Background(), client.ObjectKeyFromObject(x.executor.Operation), op); err != nil {
		t.Fatal(err)
	}
	// A worker that never received the controller's attempt marker has no
	// permission to claim a POST for the upload stage.
	op.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{}
	if err := x.executor.Client.Update(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	fresh := &RestoreExecutor{Client: x.executor.Client, Scheme: x.executor.Scheme, Operation: x.executor.Operation, Onboarding: x.fake}
	if _, err := fresh.Upload(context.Background(), x.upload, x.archive, bytes.NewBufferString("archive"), true); err == nil || x.fake.uploads != 0 {
		t.Fatalf("dispatch claim without an attempt marker sent a POST: calls=%d err=%v", x.fake.uploads, err)
	}
}

func TestRestoreExecutorRequiresVerifiedEvidenceAndBinding(t *testing.T) {
	f := &onboardingFake{mismatch: true}
	x := newRestoreFixture(t, f)
	got, err := x.executor.Upload(context.Background(), x.upload, x.archive, bytes.NewBufferString("x"), true)
	if err != nil || got.Phase != "EvidenceMismatch" {
		t.Fatalf("mismatched onboarding evidence was verified: %#v %v", got, err)
	}
	if _, err := x.executor.Restore(context.Background(), x.restore, x.archive, "key", true); err == nil || f.restores != 0 {
		t.Fatal("restore ran without a verified upload receipt")
	}

	for name, mutate := range map[string]func(*ha.MigrationExecutionRequest, *ArchiveDescriptor){
		"wrong database scope": func(_ *ha.MigrationExecutionRequest, a *ArchiveDescriptor) { a.DatabaseIncluded = false },
		"archive digest": func(_ *ha.MigrationExecutionRequest, a *ArchiveDescriptor) {
			a.Digest = "sha256:" + strings.Repeat("9", 64)
		},
		"foreign pvc": func(_ *ha.MigrationExecutionRequest, a *ArchiveDescriptor) { a.PVCUID = "other" },
		"unaccepted backup request": func(_ *ha.MigrationExecutionRequest, a *ArchiveDescriptor) {
			a.RequestID = "sha256:" + strings.Repeat("7", 64)
		},
		"backup scope": func(_ *ha.MigrationExecutionRequest, a *ArchiveDescriptor) {
			a.ScopeDigest = "sha256:" + strings.Repeat("6", 64)
		},
		"missing validation": func(_ *ha.MigrationExecutionRequest, a *ArchiveDescriptor) { a.Validation = nil },
		"unapproved backup":  func(_ *ha.MigrationExecutionRequest, a *ArchiveDescriptor) { a.BackupID = "another-backup" },
		"unapproved agent":   func(_ *ha.MigrationExecutionRequest, a *ArchiveDescriptor) { a.AgentID = "other.agent" },
	} {
		t.Run(name, func(t *testing.T) {
			fresh := newRestoreFixture(t, &onboardingFake{})
			archive := fresh.archive
			mutate(&fresh.upload, &archive)
			if _, err := fresh.executor.Upload(context.Background(), fresh.upload, archive, bytes.NewBufferString("x"), true); err == nil || fresh.fake.uploads != 0 {
				t.Fatalf("%s accepted before upload POST", name)
			}
		})
	}

	fresh := newRestoreFixture(t, &onboardingFake{})
	if _, err := fresh.executor.Upload(context.Background(), fresh.upload, fresh.archive, bytes.NewBufferString("x"), true); err != nil {
		t.Fatal(err)
	}
	rotated := fresh.archive
	key := rotated.Validation.KeyRef
	key.ResourceVersion = "2"
	rotated.Validation = &ArchiveValidation{Profile: rotated.Validation.Profile, ValidatorVersion: "v1", CiphertextDigest: rotated.Digest, SourceEvidenceDigest: rotated.Validation.SourceEvidenceDigest, KeyRef: key, PayloadAuthenticated: true, StructureValid: true}
	if _, err := fresh.executor.Restore(context.Background(), fresh.restore, rotated, "key", true); err == nil || fresh.fake.restores != 0 {
		t.Fatal("restore accepted a recovery key that is not the archive's pinned key")
	}
	other := fresh.restore
	other.RequestID = "sha256:" + strings.Repeat("5", 64)
	if _, err := fresh.executor.Restore(context.Background(), other, fresh.archive, "key", true); err == nil || fresh.fake.restores != 0 {
		t.Fatal("unaccepted restore request was sent")
	}
}

func requestForRestoreTest(stage, upload string) ha.MigrationExecutionRequest {
	r := ha.MigrationExecutionRequest{Version: "v1", Stage: stage, OperationUID: "op-uid", SourceID: "source", ReportID: "report", Target: ha.MigrationTarget{Name: "target", UID: "target-uid"}, ScopeDigest: "sha256:" + strings.Repeat("d", 64), VerificationDigest: "sha256:" + strings.Repeat("e", 64), ArchiveRef: &ha.MigrationObjectReference{Name: "archive", UID: "archive-uid"}, ArchiveDigest: "sha256:" + strings.Repeat("f", 64), ArchivePVCUID: "pvc", UploadRequestID: upload}
	if stage == "restore" {
		r.RecoveryKey = &ha.MigrationSecretIdentity{Name: "key", Key: "value", UID: "key-uid", ResourceVersion: "1"}
	}
	id, err := migrationprotocol.RequestID(r)
	if err != nil {
		panic(err)
	}
	r.RequestID = id
	return r
}
