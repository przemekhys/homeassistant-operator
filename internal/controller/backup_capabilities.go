package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// BackupCapabilityGate authorizes native generation, not observation. Candidate
// gates are injected only by the separate qualification program, never by CRs
// or production manager flags or environment variables.
type BackupCapabilityGate interface {
	AuthorizeBackup(context.Context, *hav1.HomeAssistant) error
}

type BackupArchiveResolver interface {
	ArchiveForBackupJob(context.Context, *hav1.HomeAssistant, string) (string, error)
}

// NativeBackupCapabilityGate uses only reviewed image-specific records. Its
// zero-value catalog deliberately grants no execution authority.
type NativeBackupCapabilityGate struct {
	Reader  client.Reader
	Catalog *haclient.CapabilityCatalog
}

func (g NativeBackupCapabilityGate) AuthorizeBackup(ctx context.Context, target *hav1.HomeAssistant) error {
	digest, err := VerifyBackupTargetImage(ctx, g.Reader, target)
	if err != nil || !g.Catalog.Supports(digest, haclient.CapabilityBackup) {
		return fmt.Errorf("native backup image qualification is unavailable")
	}
	return nil
}

func (g NativeBackupCapabilityGate) ArchiveForBackupJob(
	ctx context.Context, target *hav1.HomeAssistant, jobID string,
) (string, error) {
	digest, err := VerifyBackupTargetImage(ctx, g.Reader, target)
	if err != nil || jobID == "" || !g.Catalog.BackupJobIsArchiveID(digest) {
		return "", fmt.Errorf("native backup job/archive correlation is unqualified")
	}
	return jobID, nil
}

// VerifyBackupTargetImage reads the live target, workload and pod directly at
// the execution boundary. Desired tags or a cached status are not evidence of
// which image is running. All failures are deliberately free of API responses.
func VerifyBackupTargetImage(ctx context.Context, reader client.Reader, target *hav1.HomeAssistant) (string, error) {
	denied := fmt.Errorf("native backup target image cannot be verified")
	if reader == nil || target == nil || target.UID == "" {
		return "", denied
	}
	live := &hav1.HomeAssistant{}
	key := types.NamespacedName{Namespace: target.Namespace, Name: target.Name}
	if err := reader.Get(ctx, key, live); err != nil || live.UID != target.UID || !live.DeletionTimestamp.IsZero() {
		return "", denied
	}
	sts := &appsv1.StatefulSet{}
	if err := reader.Get(ctx, key, sts); err != nil || sts.UID == "" || !sts.DeletionTimestamp.IsZero() {
		return "", denied
	}
	if !backupWorkloadOwnedBy(sts, live, "HomeAssistant", hav1.GroupVersion.String()) {
		return "", denied
	}
	pod := &corev1.Pod{}
	key.Name += "-0"
	if err := reader.Get(ctx, key, pod); err != nil {
		return "", denied
	}
	if pod.UID == "" || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning {
		return "", denied
	}
	if !backupWorkloadOwnedBy(pod, sts, "StatefulSet", "apps/v1") {
		return "", denied
	}
	var expected string
	for _, container := range pod.Spec.Containers {
		if container.Name == "home-assistant" {
			_, digest, pinned := strings.Cut(container.Image, "@")
			if !pinned || !haclient.ValidBackupImageDigest(digest) {
				return "", denied
			}
			expected = digest
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "home-assistant" {
			continue
		}
		if !status.Ready || status.State.Running == nil {
			return "", denied
		}
		actual := status.ImageID
		if _, digest, ok := strings.Cut(actual, "@"); ok {
			actual = digest
		} else if _, digest, ok := strings.Cut(actual, "://"); ok {
			actual = digest
		}
		if expected == "" || actual != expected || !haclient.ValidBackupImageDigest(actual) {
			return "", denied
		}
		return actual, nil
	}
	return "", denied
}

func backupWorkloadOwnedBy(object, parent metav1.Object, kind, apiVersion string) bool {
	owner := metav1.GetControllerOf(object)
	return owner != nil && owner.UID == parent.GetUID() && owner.Name == parent.GetName() &&
		owner.Kind == kind && owner.APIVersion == apiVersion
}
