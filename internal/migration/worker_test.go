package migration

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type loseWorkerDispatchResponse struct {
	client.Client
	lost bool
}

type verifyingDispatchSource struct {
	*dispatchSource
	projections     map[string][]byte
	profileFailures int
	profileVersion  string
}

func (s *verifyingDispatchSource) GetConfig(context.Context, string) (*haclient.ConfigResponse, error) {
	if s.profileFailures > 0 {
		s.profileFailures--
		return nil, &haclient.Error{Type: haclient.ErrorTypeNotReady}
	}
	version := s.profileVersion
	if version == "" {
		version = "2026.11.0"
	}
	return &haclient.ConfigResponse{Version: version}, nil
}

func TestWorkerProfileRetriesOnlyReadObservation(t *testing.T) {
	source := &verifyingDispatchSource{dispatchSource: &dispatchSource{}, profileFailures: 1}
	if err := observeWorkerProfile(context.Background(), source, "private-token"); err != nil {
		t.Fatal(err)
	}
	if source.calls != 0 {
		t.Fatal("profile observation sent a mutation")
	}
	source.profileVersion = "2026.10.4"
	if err := observeWorkerProfile(context.Background(), source, "private-token"); err == nil || source.calls != 0 {
		t.Fatal("unqualified profile accepted")
	}
}

func (s *verifyingDispatchSource) ReadMigrationVerification(_ context.Context, _ string, kind, id string) ([]byte, error) {
	value, ok := s.projections[kind+"/"+id]
	if !ok {
		return nil, errors.New("unavailable")
	}
	return value, nil
}

type verifyingConcurrentSource struct {
	*concurrentDispatchSource
	projections map[string][]byte
}

func (s *verifyingConcurrentSource) GetConfig(context.Context, string) (*haclient.ConfigResponse, error) {
	return &haclient.ConfigResponse{Version: "2026.11.0"}, nil
}

func (s *verifyingConcurrentSource) ReadMigrationVerification(_ context.Context, _ string, kind, id string) ([]byte, error) {
	value, ok := s.projections[kind+"/"+id]
	if !ok {
		return nil, errors.New("unavailable")
	}
	return value, nil
}

func (c *loseWorkerDispatchResponse) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := c.Client.Update(ctx, obj, opts...); err != nil {
		return err
	}
	if _, ok := obj.(*ha.HomeAssistantMigration); ok && !c.lost {
		c.lost = true
		return errors.New("lost response")
	}
	return nil
}

func TestWorkerRejectsUnacceptedOrUnattemptedRequest(t *testing.T) {
	s := runtime.NewScheme()
	if err := ha.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "operation", Namespace: "default", UID: "uid"}, Spec: ha.HomeAssistantMigrationSpec{ExecutionProtocol: "v1"}}
	kube := fake.NewClientBuilder().WithScheme(s).WithObjects(op).Build()
	if _, err := ReadWorkerRequest(context.Background(), kube, "default", "operation", "backup", "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("accepted unbound request")
	}
}

func TestBackupWorkerClaimsOnceAndPinsSecrets(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	if err := ha.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	include := true
	password := "recovery-password"
	projection := []byte(`{"entityId":"todo.test","items":[{"uid":"1","summary":"test","status":"needs_action"}]}`)
	source := &verifyingDispatchSource{dispatchSource: &dispatchSource{backupReader: backupReader{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password}}, agents: []haclient.BackupAgent{{ID: "backup.local"}}}, id: "job-id"}, projections: map[string][]byte{"TodoItems/todo.test": projection}}
	scope, err := BackupScopeDigest(observeBackupPreflight(ctx, source, "private-token"))
	if err != nil {
		t.Fatal(err)
	}
	itemDigest, err := migrationprotocol.ProjectionDigest("TodoItems", "todo.test", projection)
	if err != nil {
		t.Fatal(err)
	}
	items := []ha.MigrationVerificationItem{{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: itemDigest}}
	vd, err := migrationprotocol.VerificationDigest(items)
	if err != nil {
		t.Fatal(err)
	}
	token := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default", UID: "token-uid", ResourceVersion: "1"}, Data: map[string][]byte{"token": []byte("private-token")}}
	key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: "default", UID: "key-uid", ResourceVersion: "1"}, Data: map[string][]byte{"key": []byte(password)}}
	r := ha.MigrationExecutionRequest{Version: "v1", Stage: "backup", OperationUID: "uid", SourceID: "https://source.example", SourceEndpoint: "https://source.example", ReportID: "report", Target: ha.MigrationTarget{Name: "target", UID: "target-uid"}, ScopeDigest: scope, VerificationDigest: vd, ArchiveStorage: &ha.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"}, SourceToken: &ha.MigrationSecretIdentity{Name: "token", Key: "token", UID: "token-uid", ResourceVersion: "1"}, RecoveryKey: &ha.MigrationSecretIdentity{Name: "key", Key: "key", UID: "key-uid", ResourceVersion: "1"}}
	r.RequestID, err = migrationprotocol.RequestID(r)
	if err != nil {
		t.Fatal(err)
	}
	op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "operation", Namespace: "default", UID: "uid"}, Spec: ha.HomeAssistantMigrationSpec{ExecutionProtocol: "v1", SourceID: r.SourceID, ReportID: r.ReportID, Target: r.Target, ArchiveStorage: r.ArchiveStorage, Verification: items, Approvals: []ha.MigrationApproval{{ID: "backup-settings-stable", Approved: true, Digest: r.ReportID, ScopeDigest: r.ScopeDigest, TargetUID: r.Target.UID}}, ExecutionRequests: []ha.MigrationExecutionRequest{r}, ExecutionAttempts: []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}}}
	kube := fake.NewClientBuilder().WithScheme(s).WithObjects(op, token, key).Build()
	if err := RunBackupWorker(ctx, kube, s, "default", "operation", r.RequestID, source); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 {
		t.Fatalf("sent %d times", source.calls)
	}
	if err := RunBackupWorker(ctx, kube, s, "default", "operation", r.RequestID, source); err == nil || source.calls != 1 {
		t.Fatal("repeat dispatch accepted")
	}
	current := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), current); err != nil {
		t.Fatal(err)
	}
	aborted := current.DeepCopy()
	aborted.Spec.Resolutions = []ha.MigrationResolution{{Version: "v1", Stage: "backup", RequestID: r.RequestID, Decision: "Abort"}}
	abortKube := fake.NewClientBuilder().WithScheme(s).WithObjects(aborted, token.DeepCopy(), key.DeepCopy()).Build()
	if _, err := ReadWorkerRequest(ctx, abortKube, "default", "operation", "backup", r.RequestID); err == nil {
		t.Fatal("aborted worker request accepted")
	}
	if err := RunBackupWorker(ctx, abortKube, s, "default", "operation", r.RequestID, source); err == nil || source.calls != 1 {
		t.Fatal("abort authorised another dispatch")
	}
	cm := &corev1.ConfigMap{}
	name := "migration-receipt-" + strings.TrimPrefix(r.RequestID, "sha256:")[:40]
	if err := kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, cm); err != nil {
		t.Fatal(err)
	}
	if owner := metav1.GetControllerOf(cm); owner == nil || owner.UID != op.UID {
		t.Fatal("receipt not owned by migration")
	}
	ownerTrue := true
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "archive", Namespace: "default", UID: "pvc-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: ha.GroupVersion.String(), Kind: "HomeAssistantMigration", Name: op.Name, UID: op.UID, Controller: &ownerTrue}}}}
	if err := kube.Create(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{"slug": "job-id", "version": 2, "type": "partial", "compressed": true, "protected": true, "date": "2026-11-15T22:18:00Z", "name": "test", "homeassistant": map[string]any{"version": "2026.11.0", "exclude_database": false}, "extra": map[string]any{"instance_id": "disposable", "with_automatic_settings": true}}
	archiveBytes := string(profileFixture(t, []byte(password), payloadFixture(t, tar.Header{Name: "data", Mode: 0600, Size: 32}), metadata))
	observer := &backupObservationFixture{backupReader: source.backupReader, details: &haclient.BackupDetails{AgentErrors: map[string]any{}, Backup: haclient.BackupArchive{ID: "job-id", Date: "2026-11-15T22:18:00Z", HomeAssistantVersion: "2026.11.0", Automatic: true, DatabaseIncluded: &include, HomeAssistantIncluded: &include, Folders: []string{}, Addons: []json.RawMessage{}, FailedFolders: []string{}, FailedAddons: []json.RawMessage{}, FailedAgentIDs: []string{}, Agents: map[string]haclient.BackupAgentCopy{"backup.local": {Protected: true, Size: int64(len(archiveBytes))}}}}, keyOK: true, archive: archiveBytes}
	t.Run("lost receipt resolves by read only", func(t *testing.T) {
		current := &ha.HomeAssistantMigration{}
		if err := kube.Get(ctx, client.ObjectKeyFromObject(op), current); err != nil {
			t.Fatal(err)
		}
		e := migrationprotocol.Evidence{Version: "v1", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: "backup", SourceID: r.SourceID, TargetUID: r.Target.UID, JobID: "job-id", BackupID: "job-id"}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		yes := true
		hint := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "resolution", Namespace: op.Namespace, UID: "hint-uid"}, Immutable: &yes, Data: map[string]string{"evidence.json": string(b)}}
		current.Spec.Resolutions = []ha.MigrationResolution{{Version: "v1", Stage: "backup", RequestID: r.RequestID, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: hint.Name, UID: string(hint.UID), Key: "evidence.json"}}}
		lost := fake.NewClientBuilder().WithScheme(s).WithObjects(current, token.DeepCopy(), key.DeepCopy(), pvc.DeepCopy(), hint).Build()
		dir := t.TempDir()
		if err := ArchiveDispatchedBackup(ctx, descriptorUIDClient{lost}, observer, "default", "operation", r.RequestID, dir, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadValidatedBackupEvidence(ctx, lost, current, r); err != nil {
			t.Fatal(err)
		}
		if err := RunBackupWorker(ctx, lost, s, "default", "operation", r.RequestID, source); err == nil || source.calls != 1 {
			t.Fatal("resolution reopened dispatch")
		}
		receipt := &corev1.ConfigMap{}
		if err := lost.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: name}, receipt); err == nil {
			t.Fatal("missing dispatch receipt was recreated")
		}
		// The hint's Kubernetes UID is part of the durable binding.
		wrong := current.DeepCopy()
		wrong.Spec.Resolutions[0].EvidenceRef.UID = "different"
		if err := lost.Update(ctx, wrong); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadValidatedBackupEvidence(ctx, lost, wrong, r); err == nil {
			t.Fatal("foreign resolution evidence accepted after restart")
		}
	})
	t.Run("dispatching receipt remains unchanged", func(t *testing.T) {
		current := &ha.HomeAssistantMigration{}
		if err := kube.Get(ctx, client.ObjectKeyFromObject(op), current); err != nil {
			t.Fatal(err)
		}
		e := migrationprotocol.Evidence{Version: "v1", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: r.Stage, SourceID: r.SourceID, TargetUID: r.Target.UID, JobID: "job-id"}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		yes := true
		hint := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "hint", Namespace: op.Namespace, UID: "hint-uid"}, Immutable: &yes, Data: map[string]string{"result": string(b)}}
		current.Spec.Resolutions = []ha.MigrationResolution{{Version: "v1", Stage: r.Stage, RequestID: r.RequestID, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: hint.Name, UID: string(hint.UID), Key: "result"}}}
		dispatch := BackupDispatchReceipt{RequestID: r.RequestID, OperationUID: r.OperationUID, TargetUID: r.Target.UID, ReportID: r.ReportID, ScopeDigest: r.ScopeDigest, Phase: "Dispatching"}
		body, err := json.Marshal(dispatch)
		if err != nil {
			t.Fatal(err)
		}
		receipt := cm.DeepCopy()
		receipt.ResourceVersion = ""
		receipt.Data[backupDispatchReceiptKey] = string(body)
		clone := fake.NewClientBuilder().WithScheme(s).WithObjects(current, token.DeepCopy(), key.DeepCopy(), pvc.DeepCopy(), hint, receipt).Build()
		if err := ArchiveDispatchedBackup(ctx, descriptorUIDClient{clone}, observer, "default", "operation", r.RequestID, t.TempDir(), time.Millisecond); err != nil {
			t.Fatal(err)
		}
		stored := &corev1.ConfigMap{}
		if err := clone.Get(ctx, client.ObjectKeyFromObject(receipt), stored); err != nil {
			t.Fatal(err)
		}
		if stored.Data[backupDispatchReceiptKey] != string(body) || source.calls != 1 {
			t.Fatal("resolution changed receipt or dispatched again")
		}
	})
	dir := t.TempDir()
	if err := ArchiveDispatchedBackup(ctx, descriptorUIDClient{kube}, observer, "default", "operation", r.RequestID, dir, time.Millisecond); err != nil {
		t.Fatalf("archive not verified: %v", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadValidatedBackupEvidence(ctx, kube, op, r); err != nil {
		t.Fatal(err)
	}
	allEvidence := &corev1.ConfigMapList{}
	if err := kube.List(ctx, allEvidence, client.InNamespace(op.Namespace)); err != nil {
		t.Fatal(err)
	}
	encodedEvidence, err := json.Marshal(allEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedEvidence), password) || strings.Contains(string(encodedEvidence), "private-token") {
		t.Fatal("archive or source evidence leaked a credential")
	}
	observer.afterDownload = func() { t.Fatal("offline observation attempted download") }
	observer.config = nil
	observer.details = nil
	if err := ArchiveDispatchedBackup(ctx, descriptorUIDClient{kube}, observer, "default", "operation", r.RequestID, dir, time.Millisecond); err != nil {
		t.Fatalf("offline restart: %v", err)
	}
	t.Run("descriptor-only hint recovers evidence", func(t *testing.T) {
		descriptor := &corev1.ConfigMap{}
		sourceEvidence := &corev1.ConfigMap{}
		if err := kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: "descriptor"}, descriptor); err != nil {
			t.Fatal(err)
		}
		if err := kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: sourceEvidenceName(r.RequestID)}, sourceEvidence); err != nil {
			t.Fatal(err)
		}
		var d ArchiveDescriptor
		if err := migrationprotocol.Decode([]byte(descriptor.Data["descriptor.json"]), &d); err != nil {
			t.Fatal(err)
		}
		e := migrationprotocol.Evidence{Version: "v1", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: r.Stage, SourceID: r.SourceID, TargetUID: r.Target.UID, ArchiveDescriptorRef: &ha.MigrationObjectReference{Name: descriptor.Name, UID: string(descriptor.UID)}, ArchiveDigest: d.Digest}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		yes := true
		hint := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "hint", Namespace: op.Namespace, UID: "hint-uid"}, Immutable: &yes, Data: map[string]string{"result": string(b)}}
		current := op.DeepCopy()
		current.Spec.Resolutions = []ha.MigrationResolution{{Version: "v1", Stage: r.Stage, RequestID: r.RequestID, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: hint.Name, UID: string(hint.UID), Key: "result"}}}
		clone := fake.NewClientBuilder().WithScheme(s).WithObjects(current, token.DeepCopy(), key.DeepCopy(), pvc.DeepCopy(), hint, descriptor, sourceEvidence).Build()
		if _, _, err := ReadValidatedBackupEvidence(ctx, clone, current, r); err != nil {
			t.Fatal("descriptor-only result not reconstructed", err)
		}
		e.ArchiveDigest = "sha256:" + strings.Repeat("f", 64)
		b, _ = json.Marshal(e)
		hint.Data["result"] = string(b)
		if err := clone.Update(ctx, hint); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadValidatedBackupEvidence(ctx, clone, current, r); err == nil {
			t.Fatal("foreign archive digest accepted")
		}
	})
	for _, scenario := range []string{"key version", "PVC UID", "missing provenance", "corrupt file", "missing descriptor", "lost descriptor response"} {
		t.Run(scenario, func(t *testing.T) {
			objects := make([]client.Object, 0, 7)
			for _, object := range []client.Object{&ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: op.Name, Namespace: op.Namespace}}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: op.Namespace}}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: op.Namespace}}, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvc.Name, Namespace: op.Namespace}}, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: op.Namespace}}, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: sourceEvidenceName(r.RequestID), Namespace: op.Namespace}}, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "descriptor", Namespace: op.Namespace}}} {
				if err := kube.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
					t.Fatal(err)
				}
				objects = append(objects, object)
			}
			clone := fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "backup.tar"), []byte(archiveBytes), 0600); err != nil {
				t.Fatal(err)
			}
			get := func(obj client.Object) {
				t.Helper()
				if err := clone.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "key version":
				v := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: op.Namespace}}
				get(v)
				v.Data["key"] = []byte("rotated")
				if err := clone.Update(ctx, v); err != nil {
					t.Fatal(err)
				}
			case "PVC UID":
				v := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvc.Name, Namespace: op.Namespace}}
				get(v)
				v.UID = "replaced"
				if err := clone.Update(ctx, v); err != nil {
					t.Fatal(err)
				}
			case "missing provenance":
				v := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: sourceEvidenceName(r.RequestID), Namespace: op.Namespace}}
				get(v)
				if err := clone.Delete(ctx, v); err != nil {
					t.Fatal(err)
				}
			case "corrupt file":
				bad := []byte(archiveBytes)
				bad[512+136+50] ^= 1
				if err := os.WriteFile(filepath.Join(dir, "backup.tar"), bad, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing descriptor", "lost descriptor response":
				v := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "descriptor", Namespace: op.Namespace}}
				get(v)
				if err := clone.Delete(ctx, v); err != nil {
					t.Fatal(err)
				}
			}
			var writer client.Client = descriptorUIDClient{clone}
			if scenario == "lost descriptor response" {
				writer = &loseArchiveCreateResponse{Client: writer}
			}
			err := ArchiveDispatchedBackup(ctx, writer, observer, "default", "operation", r.RequestID, dir, time.Millisecond)
			if scenario == "missing descriptor" || scenario == "lost descriptor response" {
				if err != nil {
					t.Fatal("offline publication recovery failed", err)
				}
				if _, _, err := ReadValidatedBackupEvidence(ctx, clone, op, r); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unsafe evidence or bytes accepted")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "backup.tar")); err != nil {
		t.Fatal("archive not stored")
	}
	if source.calls != 1 {
		t.Fatal("archive observation sent another POST")
	}
	if strings.Contains(cm.Data[backupDispatchReceiptKey], "private-token") || strings.Contains(cm.Data[backupDispatchReceiptKey], password) {
		t.Fatal("receipt leaked credentials")
	}
	if err := kube.Delete(ctx, cm); err != nil {
		t.Fatal(err)
	}
	if err := RunBackupWorker(ctx, kube, s, "default", "operation", r.RequestID, source); err == nil || source.calls != 1 {
		t.Fatal("lost receipt reopened POST permission")
	}
	other := op.DeepCopy()
	other.UID, other.ResourceVersion = "uid-2", ""
	r.OperationUID = "uid-2"
	r.RequestID, err = migrationprotocol.RequestID(r)
	if err != nil {
		t.Fatal(err)
	}
	other.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{r}
	other.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	other.Spec.ExecutionDispatches = nil
	kube2 := fake.NewClientBuilder().WithScheme(s).WithObjects(other, token.DeepCopy(), key.DeepCopy()).Build()
	lost := &loseWorkerDispatchResponse{Client: kube2}
	if err := RunBackupWorker(ctx, lost, s, "default", "operation", r.RequestID, source); err == nil || source.calls != 1 {
		t.Fatal("uncertain marker authorized POST")
	}
	if err := RunBackupWorker(ctx, kube2, s, "default", "operation", r.RequestID, source); err == nil || source.calls != 1 {
		t.Fatal("lost marker response reopened POST")
	}
	third := other.DeepCopy()
	third.UID = "uid-3"
	r.OperationUID = "uid-3"
	r.RequestID, err = migrationprotocol.RequestID(r)
	if err != nil {
		t.Fatal(err)
	}
	third.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{r}
	third.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	third.Spec.ExecutionDispatches = nil
	rotated := token.DeepCopy()
	rotated.ResourceVersion = "2"
	kube3 := fake.NewClientBuilder().WithScheme(s).WithObjects(third, rotated, key.DeepCopy()).Build()
	if err := RunBackupWorker(ctx, kube3, s, "default", "operation", r.RequestID, source); err == nil || source.calls != 1 || strings.Contains(err.Error(), "private-token") {
		t.Fatal("rotated credential permitted POST or leaked")
	}
	fourth := other.DeepCopy()
	fourth.UID = "uid-4"
	r.OperationUID = "uid-4"
	r.RequestID, err = migrationprotocol.RequestID(r)
	if err != nil {
		t.Fatal(err)
	}
	fourth.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{r}
	fourth.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	fourth.Spec.ExecutionDispatches = nil
	kube4 := fake.NewClientBuilder().WithScheme(s).WithObjects(fourth, token.DeepCopy(), key.DeepCopy()).Build()
	concurrent := &verifyingConcurrentSource{concurrentDispatchSource: &concurrentDispatchSource{backupReader: source.backupReader}, projections: source.projections}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = RunBackupWorker(ctx, kube4, s, "default", "operation", r.RequestID, concurrent)
		}()
	}
	wg.Wait()
	if n := concurrent.calls.Load(); n != 1 {
		t.Fatalf("concurrent workers sent %d requests", n)
	}
	fifth := other.DeepCopy()
	fifth.UID = "uid-5"
	r.OperationUID = "uid-5"
	r.RequestID, err = migrationprotocol.RequestID(r)
	if err != nil {
		t.Fatal(err)
	}
	fifth.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{r}
	fifth.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	fifth.Spec.ExecutionDispatches = nil
	kube5 := fake.NewClientBuilder().WithScheme(s).WithObjects(fifth, token.DeepCopy(), key.DeepCopy()).Build()
	bad := &verifyingDispatchSource{dispatchSource: source.dispatchSource, projections: map[string][]byte{"TodoItems/todo.test": []byte(`{"entityId":"todo.test","items":[]}`)}}
	if err := RunBackupWorker(ctx, kube5, s, "default", "operation", r.RequestID, bad); err == nil || source.calls != 1 {
		t.Fatal("verification mismatch permitted POST")
	}
}

func TestWorkerReadsOnlyAcceptedMarkedRequest(t *testing.T) {
	s := runtime.NewScheme()
	if err := ha.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	d := "sha256:" + strings.Repeat("a", 64)
	items := []ha.MigrationVerificationItem{{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: d}}
	vd, err := migrationprotocol.VerificationDigest(items)
	if err != nil {
		t.Fatal(err)
	}
	secret := &ha.MigrationSecretIdentity{Name: "credential", Key: "token", UID: "secret-uid", ResourceVersion: "1"}
	r := ha.MigrationExecutionRequest{Version: "v1", Stage: "backup", OperationUID: "uid", SourceID: "https://source.example", SourceEndpoint: "https://source.example", ReportID: "report", Target: ha.MigrationTarget{Name: "target", UID: "target-uid"}, ScopeDigest: d, VerificationDigest: vd, ArchiveStorage: &ha.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"}, SourceToken: secret, RecoveryKey: secret}
	r.RequestID, err = migrationprotocol.RequestID(r)
	if err != nil {
		t.Fatal(err)
	}
	op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "operation", Namespace: "default", UID: "uid"}, Spec: ha.HomeAssistantMigrationSpec{ExecutionProtocol: "v1", SourceID: r.SourceID, ReportID: r.ReportID, Target: r.Target, Verification: items, ExecutionRequests: []ha.MigrationExecutionRequest{r}}}
	kube := fake.NewClientBuilder().WithScheme(s).WithObjects(op).Build()
	if _, err := ReadWorkerRequest(context.Background(), kube, "default", "operation", "backup", r.RequestID); err == nil {
		t.Fatal("unmarked request accepted")
	}
	op.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	if err := kube.Update(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	got, err := ReadWorkerRequest(context.Background(), kube, "default", "operation", "backup", r.RequestID)
	if err != nil || got.RequestID != r.RequestID {
		t.Fatal("marked request rejected", err)
	}
	op.Spec.CancelRequested = true
	if err := kube.Update(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorkerRequest(context.Background(), kube, "default", "operation", "backup", r.RequestID); err == nil {
		t.Fatal("cancelled worker accepted")
	}
}

func TestWorkerReadsPinnedCredentialWithoutLeakingRotatedValue(t *testing.T) {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default", UID: "uid", ResourceVersion: "1"}, Data: map[string][]byte{"token": []byte("private-token")}}
	kube := fake.NewClientBuilder().WithScheme(s).WithObjects(secret).Build()
	id := &ha.MigrationSecretIdentity{Name: "token", Key: "token", UID: "uid", ResourceVersion: "1"}
	value, err := ReadPinnedCredential(context.Background(), kube, "default", id)
	if err != nil || string(value) != "private-token" {
		t.Fatalf("credential unavailable: %v", err)
	}
	id.ResourceVersion = "2"
	if _, err := ReadPinnedCredential(context.Background(), kube, "default", id); err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("unsafe credential error: %v", err)
	}
}
