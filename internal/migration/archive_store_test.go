package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type descriptorUIDClient struct{ client.Client }

type loseArchiveCreateResponse struct{ client.Client }

func (c *loseArchiveCreateResponse) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	return errors.New("lost create response")
}

func (c descriptorUIDClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*corev1.ConfigMap); ok {
		obj.SetUID(types.UID("descriptor-uid"))
	}
	return c.Client.Create(ctx, obj, opts...)
}

type archiveFailingReader struct{}

func (archiveFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("private archive contents")
}

func TestArchiveDescriptorPublicationIsOwnedAndCreateOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	data := "encrypted-archive"
	r, err := StoreProtectedArchive(ctx, dir, "backup.tar", strings.NewReader(data), int64(len(data)), true)
	if err != nil {
		t.Fatal(err)
	}
	s := runtime.NewScheme()
	if err := ha.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: "default", UID: "operation"}, Spec: ha.HomeAssistantMigrationSpec{SourceID: "source", ArchiveStorage: &ha.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"}, ExecutionRequests: []ha.MigrationExecutionRequest{{Stage: "backup", RequestID: "sha256:" + strings.Repeat("a", 64), ScopeDigest: "sha256:" + strings.Repeat("b", 64)}}}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "archive", Namespace: "default", UID: "pvc", OwnerReferences: []metav1.OwnerReference{{APIVersion: ha.GroupVersion.String(), Kind: "HomeAssistantMigration", Name: op.Name, UID: op.UID, Controller: ptrBool(true)}}}}
	d := ArchiveDescriptor{Version: "v1", OperationUID: "operation", RequestID: "sha256:" + strings.Repeat("a", 64), SourceID: "source", JobID: "ha/job", BackupID: "ha/backup", AgentID: "backup.local", PVCUID: "pvc", Filename: "backup.tar", Size: r.Size, Digest: r.Digest, ScopeDigest: "sha256:" + strings.Repeat("b", 64), Protected: true}
	kube := descriptorUIDClient{fake.NewClientBuilder().WithScheme(s).WithObjects(op, pvc).Build()}
	cm, err := PublishArchiveDescriptor(ctx, kube, s, op, pvc, dir, "descriptor", d)
	if err != nil {
		t.Fatal(err)
	}
	if owner := metav1.GetControllerOf(cm); owner == nil || owner.UID != op.UID || cm.Immutable == nil || !*cm.Immutable {
		t.Fatal("descriptor is not immutable and owned")
	}
	read, err := ReadArchiveDescriptor(ctx, kube, op, pvc, dir, cm.Name, string(cm.UID))
	if err != nil || read.Digest != d.Digest {
		t.Fatalf("descriptor did not survive reload: %v", err)
	}
	if _, err := ReadArchiveDescriptor(ctx, kube, op, pvc, dir, cm.Name, "foreign-uid"); err == nil {
		t.Fatal("foreign descriptor accepted")
	}
	if again, err := PublishArchiveDescriptor(ctx, kube, s, op, pvc, dir, "descriptor", d); err != nil || again.UID != cm.UID {
		t.Fatal("identical publication did not recover existing descriptor")
	}
	d.JobID = "foreign"
	if _, err := PublishArchiveDescriptor(ctx, kube, s, op, pvc, dir, "descriptor", d); err == nil {
		t.Fatal("conflicting descriptor accepted")
	}
}

func ptrBool(b bool) *bool { return &b }

func TestStoreProtectedArchiveStreamsPrivatelyAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	data := "protected-archive-fixture"
	receipt, err := StoreProtectedArchive(context.Background(), dir, "archive.tar", strings.NewReader(data), int64(len(data)), true)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(data))
	if receipt.Size != int64(len(data)) || receipt.Digest != "sha256:"+hex.EncodeToString(hash[:]) {
		t.Fatalf("receipt = %#v", receipt)
	}
	path := filepath.Join(dir, "archive.tar")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("archive mode: %v %v", info, err)
	}
	if _, err := StoreProtectedArchive(context.Background(), dir, "archive.tar", strings.NewReader("other"), 5, true); err == nil {
		t.Fatal("existing archive overwritten")
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != data {
		t.Fatal("existing archive was changed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging files remain: %v %v", entries, err)
	}
}

func TestStoreProtectedArchiveRejectsUnsafeOrIncompleteInput(t *testing.T) {
	for _, test := range []struct {
		name      string
		filename  string
		contents  string
		size      int64
		protected bool
		cancel    bool
	}{
		{"unprotected", "archive", "data", 4, false, false},
		{"short", "archive", "data", 5, true, false},
		{"long", "archive", "data", 3, true, false},
		{"empty", "archive", "", 0, true, false},
		{"traversal", "../archive", "data", 4, true, false},
		{"cancelled", "archive", "data", 4, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				cancel()
			}
			if _, err := StoreProtectedArchive(ctx, dir, test.filename, strings.NewReader(test.contents), test.size, test.protected); err == nil {
				t.Fatal("invalid archive accepted")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("unexpected artifacts: %v %v", entries, err)
			}
		})
	}
}

func TestStoreProtectedArchiveRedactsStreamErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := StoreProtectedArchive(context.Background(), dir, "archive", archiveFailingReader{}, 5, true)
	if err == nil || strings.Contains(err.Error(), "private archive contents") {
		t.Fatalf("unsafe error: %v", err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("unexpected artifacts: %v %v", entries, readErr)
	}
}

func TestArchiveDescriptorDetectsChangedBytes(t *testing.T) {
	dir := t.TempDir()
	data := "encrypted-archive"
	r, err := StoreProtectedArchive(context.Background(), dir, "backup.tar", strings.NewReader(data), int64(len(data)), true)
	if err != nil {
		t.Fatal(err)
	}
	d := ArchiveDescriptor{Version: "v1", OperationUID: "operation", RequestID: "sha256:" + strings.Repeat("a", 64), SourceID: "source", JobID: "ha/job", BackupID: "ha/backup", AgentID: "backup.local", PVCUID: "pvc", Filename: "backup.tar", Size: r.Size, Digest: r.Digest, ScopeDigest: "sha256:" + strings.Repeat("b", 64), Protected: true}
	if err := VerifyStoredArchive(context.Background(), dir, d); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backup.tar"), []byte("encrypted-archivE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyStoredArchive(context.Background(), dir, d); err == nil {
		t.Fatal("changed archive accepted")
	}
	d.Filename = "../backup.tar"
	if err := VerifyStoredArchive(context.Background(), dir, d); err == nil {
		t.Fatal("unsafe path accepted")
	}
}
