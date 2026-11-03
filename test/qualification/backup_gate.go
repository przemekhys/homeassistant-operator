package main

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/controller"
	"github.com/przemekhys/homeassistant-operator/test/utils"
)

// scopedBackupGate is linked only into this test program, never the production
// manager. The namespace, target and actual image are verified on every call.
type scopedBackupGate struct {
	Reader client.Reader
	Target utils.BackupRestoreQualificationTarget
}

func (g scopedBackupGate) AuthorizeBackup(ctx context.Context, target *hav1.HomeAssistant) error {
	if target == nil || g.Reader == nil {
		return fmt.Errorf("qualification target is unavailable")
	}
	if err := g.Target.Validate(g.Target.ImageDigest, target.Namespace, string(target.UID)); err != nil {
		return err
	}
	namespace := &corev1.Namespace{}
	if err := g.Reader.Get(ctx, types.NamespacedName{Name: target.Namespace}, namespace); err != nil {
		return fmt.Errorf("qualification namespace is unavailable")
	}
	if !namespace.DeletionTimestamp.IsZero() || namespace.Labels[utils.BackupQualificationNamespaceLabel] != "true" {
		return fmt.Errorf("qualification requires an explicitly isolated namespace")
	}
	digest, err := controller.VerifyBackupTargetImage(ctx, g.Reader, target)
	if err != nil {
		return fmt.Errorf("qualification target image is unverified")
	}
	return g.Target.Validate(digest, target.Namespace, string(target.UID))
}

// The pinned Container candidate returns the archive ID as backup_job_id.
// This source-specific rule is not granted to arbitrary qualification images.
func (g scopedBackupGate) ArchiveForBackupJob(
	ctx context.Context, target *hav1.HomeAssistant, jobID string,
) (string, error) {
	if err := g.AuthorizeBackup(ctx, target); err != nil {
		return "", err
	}
	if jobID != "" {
		for _, digest := range utils.HomeAssistantBackupRestoreCandidate.PlatformDigests {
			if digest == g.Target.ImageDigest {
				return jobID, nil
			}
		}
	}
	return "", fmt.Errorf("candidate backup job/archive mapping has not been verified")
}
