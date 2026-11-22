package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func migrationRestorePreparationEnabled(ha *hav1.HomeAssistant) bool {
	return ha.Spec.Alpha != nil && ha.Spec.Alpha.MigrationRestore != nil &&
		ha.Spec.Alpha.MigrationRestore.Enabled
}

// migrationVerificationAndResumePassed reports whether a target HomeAssistant has
// durably completed post-restore technical verification and received the separate
// resume consent. It never disables the opt-in and never treats a running Pod, a
// 2xx response or a bare receipt as proof: the state machine must have reached
// AwaitingManualCutover or Completed, which requires a verified result digest and
// a resume approval bound to that same digest.
func migrationVerificationAndResumePassed(ctx context.Context, reader client.Reader, ha *hav1.HomeAssistant) (bool, error) {
	if !migrationRestorePreparationEnabled(ha) {
		return true, nil
	}
	if reader == nil {
		// Without durable state we cannot prove verification; stay blocked.
		return false, nil
	}
	var migrations hav1alpha1.HomeAssistantMigrationList
	if err := reader.List(ctx, &migrations, client.InNamespace(ha.Namespace)); err != nil {
		return false, err
	}
	var matches []*hav1alpha1.HomeAssistantMigration
	for i := range migrations.Items {
		mig := &migrations.Items[i]
		if mig.Spec.Target.Name != ha.Name || mig.Spec.Target.UID != string(ha.UID) {
			continue
		}
		if mig.Spec.CancelRequested || !mig.DeletionTimestamp.IsZero() || migrationprotocol.AbortRequested(mig) {
			continue
		}
		matches = append(matches, mig)
	}
	// More than one live operation for the same target is ambiguous: release
	// nothing until an administrator resolves it.
	if len(matches) != 1 {
		return false, nil
	}
	for _, mig := range matches {
		// Only a phase produced by the state machine proves that access and the
		// approved verification set were actually checked against the target.
		if mig.Status.Phase != "AwaitingManualCutover" && mig.Status.Phase != "Completed" {
			continue
		}
		var restore *hav1alpha1.MigrationExecutionRequest
		for j := range mig.Spec.ExecutionRequests {
			if mig.Spec.ExecutionRequests[j].Stage == "restore" {
				restore = &mig.Spec.ExecutionRequests[j]
				break
			}
		}
		if restore == nil {
			continue
		}
		for _, approval := range mig.Spec.Approvals {
			if approval.ID != "resume" || !approval.Approved {
				continue
			}
			bound := approval.TargetUID == restore.Target.UID &&
				approval.ArchiveDigest == restore.ArchiveDigest &&
				approval.VerificationDigest == restore.VerificationDigest &&
				migrationprotocol.ValidDigest(approval.Digest)
			if bound {
				return true, nil
			}
		}
	}
	return false, nil
}

// migrationProxyServerSecret returns only the server-side projection. The
// full bundle and worker private key are never mounted into the target.
func (r *HomeAssistantReconciler) migrationProxyServerSecret(ctx context.Context, target *hav1.HomeAssistant) (string, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	if reader == nil || target == nil {
		return "", fmt.Errorf("migration proxy reader unavailable")
	}
	var operations hav1alpha1.HomeAssistantMigrationList
	if err := reader.List(ctx, &operations, client.InNamespace(target.Namespace)); err != nil {
		return "", fmt.Errorf("migration proxy lookup unavailable")
	}
	var operation *hav1alpha1.HomeAssistantMigration
	for i := range operations.Items {
		candidate := &operations.Items[i]
		if candidate.Spec.Target.Name == target.Name && candidate.Spec.Target.UID == string(target.UID) && candidate.Spec.ProxyCredentialBinding != nil {
			if operation != nil {
				return "", fmt.Errorf("migration proxy binding ambiguous")
			}
			operation = candidate
		}
	}
	if operation == nil {
		return "", nil
	}
	binding := operation.Spec.ProxyCredentialBinding
	if operation.Spec.CancelRequested || !operation.DeletionTimestamp.IsZero() || migrationprotocol.AbortRequested(operation) || migrationprotocol.ValidateAccepted(operation) != nil {
		return "", fmt.Errorf("migration proxy binding unavailable")
	}
	bundle := &corev1.Secret{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: binding.Bundle.Name}, bundle); err != nil || string(bundle.UID) != binding.Bundle.UID || bundle.ResourceVersion != binding.BundleResourceVersion || bundle.Immutable == nil || !*bundle.Immutable {
		return "", fmt.Errorf("migration proxy bundle unavailable")
	}
	material := &migration.ProxyCredentials{CA: bundle.Data["ca.crt"], ServerCertificate: bundle.Data["server.crt"], ServerKey: bundle.Data["server.key"], WorkerCertificate: bundle.Data["client.crt"], WorkerKey: bundle.Data["client.key"]}
	if migration.ValidateProxyCredentials(material, time.Now()) != nil || validateProxyCredentialProjections(reader, operation, *binding, bundle) != nil {
		return "", fmt.Errorf("migration proxy bundle unavailable")
	}
	return proxyServerCredentialName(binding.Bundle.Name), nil
}

// migrationRestoreStartAllowed defaults to true for ordinary HomeAssistant
// resources. An opted-in restore target is scaled to zero until its exact UID
// is bound to a durable operation with verified isolation (proxy credentials),
// restore approval, and source stop verified. This allows the restore window
// where the target runs with proxy sidecar but before post-restore verification.
// After verification, separate resume and manual-cutover consents are required
// to release ordinary bootstrap/projections/API writes.
func (r *HomeAssistantReconciler) migrationRestoreStartAllowed(
	ctx context.Context,
	ha *hav1.HomeAssistant,
) (bool, error) {
	if !migrationRestorePreparationEnabled(ha) {
		return true, nil
	}
	// The credential-mount admission webhook is the identity boundary that
	// NetworkPolicy labels cannot provide. A hand-run binary and a chart install
	// with webhooks disabled must therefore keep restore targets stopped.
	if os.Getenv("MIGRATION_ISOLATION_ADMISSION_ENABLED") != "true" {
		return false, nil
	}
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	var migrations hav1alpha1.HomeAssistantMigrationList
	if err := reader.List(ctx, &migrations, client.InNamespace(ha.Namespace)); err != nil {
		return false, err
	}
	for _, mig := range migrations.Items {
		if mig.Spec.Target.Name != ha.Name ||
			mig.Spec.Target.UID != string(ha.UID) {
			continue
		}
		// A cancelled or deleting migration must never release the target.
		if mig.Spec.CancelRequested || !mig.DeletionTimestamp.IsZero() || migrationprotocol.AbortRequested(&mig) {
			continue
		}
		// Check for bound restore approval
		var restore *hav1alpha1.MigrationApproval
		for i := range mig.Spec.Approvals {
			if mig.Spec.Approvals[i].ID == "restore" {
				if restore != nil {
					return false, nil
				}
				restore = &mig.Spec.Approvals[i]
			}
		}
		if !restoreApprovalBound(&mig, restore) {
			continue
		}
		// Check source stop verified
		stopped, err := sourceStopVerified(ctx, reader, &mig)
		if err != nil {
			return false, err
		}
		if !stopped {
			continue
		}
		// Check proxy credential binding exists (isolation prepared)
		if mig.Spec.ProxyCredentialBinding == nil {
			continue
		}
		// Verify proxy bundle is still valid and bound.
		bundle := &corev1.Secret{}
		if err := reader.Get(ctx, client.ObjectKey{Namespace: mig.Namespace, Name: mig.Spec.ProxyCredentialBinding.Bundle.Name}, bundle); err != nil ||
			string(bundle.UID) != mig.Spec.ProxyCredentialBinding.Bundle.UID ||
			bundle.ResourceVersion != mig.Spec.ProxyCredentialBinding.BundleResourceVersion ||
			bundle.Immutable == nil || !*bundle.Immutable {
			continue
		}
		// The restore window only needs the target running with its proxy
		// sidecar. The upload receipt is produced by a worker that itself needs
		// the target to run, so requiring it here would deadlock.
		return true, nil
	}
	return false, nil
}
