package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ArchiveReceipt records the byte identity of an archive without exposing its contents.
type ArchiveReceipt struct {
	Digest string
	Size   int64
}

// PublishArchiveDescriptor records byte identity after a complete transfer.
// The immutable ConfigMap is not a proof of backup protection or key validity.
func PublishArchiveDescriptor(ctx context.Context, kube client.Client, scheme *runtime.Scheme, op *ha.HomeAssistantMigration, pvc *corev1.PersistentVolumeClaim, dir, name string, d ArchiveDescriptor) (*corev1.ConfigMap, error) {
	if ctx == nil || kube == nil || scheme == nil || op == nil || pvc == nil || op.UID == "" || op.Namespace == "" || pvc.Namespace != op.Namespace || string(op.UID) != d.OperationUID || string(pvc.UID) != d.PVCUID || op.Spec.SourceID != d.SourceID || op.Spec.ArchiveStorage == nil || op.Spec.ArchiveStorage.PVCName != pvc.Name || op.Spec.ArchiveStorage.DescriptorName != name {
		return nil, fmt.Errorf("archive identity is invalid")
	}
	owner := metav1.GetControllerOf(pvc)
	if owner == nil || owner.UID != op.UID {
		return nil, fmt.Errorf("archive PVC is not owned by migration")
	}
	currentOp := &ha.HomeAssistantMigration{}
	currentPVC := &corev1.PersistentVolumeClaim{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), currentOp); err != nil || currentOp.UID != op.UID || !currentOp.DeletionTimestamp.IsZero() || currentOp.Spec.CancelRequested {
		return nil, fmt.Errorf("archive operation unavailable")
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(pvc), currentPVC); err != nil || currentPVC.UID != pvc.UID {
		return nil, fmt.Errorf("archive PVC unavailable or changed")
	}
	currentOwner := metav1.GetControllerOf(currentPVC)
	if currentOwner == nil || currentOwner.UID != currentOp.UID || currentOp.Spec.SourceID != d.SourceID || currentOp.Spec.ArchiveStorage == nil || currentOp.Spec.ArchiveStorage.PVCName != pvc.Name || currentOp.Spec.ArchiveStorage.DescriptorName != name {
		return nil, fmt.Errorf("archive operation binding changed")
	}
	accepted := false
	for _, r := range currentOp.Spec.ExecutionRequests {
		if r.Stage == "backup" && r.RequestID == d.RequestID && r.ScopeDigest == d.ScopeDigest {
			accepted = true
		}
	}
	if !accepted {
		return nil, fmt.Errorf("archive request is not accepted")
	}
	if err := VerifyStoredArchive(ctx, dir, d); err != nil {
		return nil, err
	}
	return createImmutableEvidence(ctx, kube, scheme, op, name, "descriptor.json", d)
}

// ReadArchiveDescriptor revalidates the descriptor and stored bytes after a restart.
func ReadArchiveDescriptor(ctx context.Context, reader client.Reader, op *ha.HomeAssistantMigration, pvc *corev1.PersistentVolumeClaim, dir, name, uid string) (*ArchiveDescriptor, error) {
	if ctx == nil || reader == nil || op == nil || pvc == nil || op.UID == "" || uid == "" || pvc.Namespace != op.Namespace || op.Spec.ArchiveStorage == nil || op.Spec.ArchiveStorage.PVCName != pvc.Name || op.Spec.ArchiveStorage.DescriptorName != name {
		return nil, fmt.Errorf("archive identity is invalid")
	}
	owner := metav1.GetControllerOf(pvc)
	if owner == nil || owner.UID != op.UID {
		return nil, fmt.Errorf("archive PVC is not owned by migration")
	}
	currentPVC := &corev1.PersistentVolumeClaim{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(pvc), currentPVC); err != nil || currentPVC.UID != pvc.UID {
		return nil, fmt.Errorf("archive PVC unavailable or changed")
	}
	currentOwner := metav1.GetControllerOf(currentPVC)
	if currentOwner == nil || currentOwner.UID != op.UID {
		return nil, fmt.Errorf("archive PVC is not owned by migration")
	}
	cm := &corev1.ConfigMap{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: name}, cm); err != nil || string(cm.UID) != uid || cm.Immutable == nil || !*cm.Immutable {
		return nil, fmt.Errorf("archive descriptor unavailable or changed")
	}
	owner = metav1.GetControllerOf(cm)
	if owner == nil || owner.UID != op.UID {
		return nil, fmt.Errorf("archive descriptor is not owned by migration")
	}
	var d ArchiveDescriptor
	if err := migrationprotocol.Decode([]byte(cm.Data["descriptor.json"]), &d); err != nil || d.OperationUID != string(op.UID) || d.SourceID != op.Spec.SourceID || d.PVCUID != string(pvc.UID) {
		return nil, fmt.Errorf("archive descriptor binding is invalid")
	}
	accepted := false
	for _, r := range op.Spec.ExecutionRequests {
		if r.Stage == "backup" && r.RequestID == d.RequestID && r.ScopeDigest == d.ScopeDigest {
			accepted = true
		}
	}
	if !accepted {
		return nil, fmt.Errorf("archive request is not accepted")
	}
	if err := VerifyStoredArchive(ctx, dir, d); err != nil {
		return nil, err
	}
	return &d, nil
}

// ArchiveDescriptor identifies encrypted bytes independently of the source.
// It records claimed protection but does not itself prove key recoverability.
type ArchiveDescriptor struct {
	Version      string             `json:"version"`
	OperationUID string             `json:"operationUID"`
	RequestID    string             `json:"requestID"`
	SourceID     string             `json:"sourceID"`
	JobID        string             `json:"jobID"`
	BackupID     string             `json:"backupID"`
	AgentID      string             `json:"agentID"`
	PVCUID       string             `json:"pvcUID"`
	Filename     string             `json:"filename"`
	Size         int64              `json:"size"`
	Digest       string             `json:"digest"`
	ScopeDigest  string             `json:"scopeDigest"`
	Protected    bool               `json:"protected"`
	Validation   *ArchiveValidation `json:"validation,omitempty"`
}

// VerifyStoredArchive checks completeness and byte identity, not actual
// protection, scope, recovery-key validity or PVC ownership.
func VerifyStoredArchive(ctx context.Context, dir string, d ArchiveDescriptor) error {
	if ctx == nil || dir == "" || d.Version != "v1" || strings.TrimSpace(d.OperationUID) == "" || !migrationprotocol.ValidDigest(d.RequestID) || strings.TrimSpace(d.SourceID) == "" || strings.TrimSpace(d.JobID) == "" || strings.TrimSpace(d.BackupID) == "" || strings.TrimSpace(d.AgentID) == "" || strings.TrimSpace(d.PVCUID) == "" || d.Filename == "" || d.Filename == "." || d.Filename == ".." || filepath.Base(d.Filename) != d.Filename || d.Size <= 0 || !migrationprotocol.ValidDigest(d.Digest) || !migrationprotocol.ValidDigest(d.ScopeDigest) || !d.Protected {
		return fmt.Errorf("invalid archive descriptor")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("archive storage unavailable")
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(d.Filename)
	if err != nil {
		return fmt.Errorf("archive unavailable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != d.Size {
		return fmt.Errorf("archive size or type mismatch")
	}
	h := sha256.New()
	n, err := io.Copy(h, &contextReader{ctx: ctx, reader: f})
	if err != nil || n != d.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != d.Digest {
		return fmt.Errorf("archive digest mismatch")
	}
	return nil
}

// StoreProtectedArchive streams an externally verified protected archive to a
// private directory without overwriting an existing destination. The caller is
// responsible for verifying protection and for securing the directory and its
// volume. Publication and the receipt must not be mistaken for a tested restore.
// A publication error can leave the destination present and must be inspected,
// never blindly retried or overwritten.
func StoreProtectedArchive(ctx context.Context, dir, name string, source io.Reader, expectedSize int64, protected bool) (ArchiveReceipt, error) {
	var receipt ArchiveReceipt
	if ctx == nil || source == nil || !protected || expectedSize <= 0 || expectedSize == math.MaxInt64 || dir == "" || name == "" ||
		name == "." || name == ".." || filepath.Base(name) != name {
		return receipt, fmt.Errorf("protected archive and safe destination are required")
	}
	temp, err := os.CreateTemp(dir, ".migration-archive-*")
	if err != nil {
		return receipt, fmt.Errorf("create private archive staging file")
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(temp, hash), &contextReader{ctx: ctx, reader: io.LimitReader(source, expectedSize+1)})
	if copyErr != nil || size != expectedSize {
		_ = temp.Close()
		return receipt, fmt.Errorf("archive stream was incomplete")
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return receipt, fmt.Errorf("sync staged archive")
	}
	if err := temp.Close(); err != nil {
		return receipt, fmt.Errorf("close staged archive")
	}
	if err := ctx.Err(); err != nil {
		return receipt, fmt.Errorf("archive publication cancelled")
	}
	if err := os.Link(tempName, filepath.Join(dir, name)); err != nil {
		return receipt, fmt.Errorf("publish archive without overwriting existing data")
	}
	directory, err := os.Open(dir)
	if err != nil {
		return receipt, fmt.Errorf("sync archive directory")
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return receipt, fmt.Errorf("sync archive directory")
	}
	return ArchiveReceipt{Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
