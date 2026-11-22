package v1alpha1

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
)

// SetupHomeAssistantMigrationWebhookWithManager registers validation for the
// destructive migration operation before a controller can observe it.
func SetupHomeAssistantMigrationWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &hav1alpha1.HomeAssistantMigration{}).
		WithValidator(&HomeAssistantMigrationCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-ha-homeassistant-io-v1alpha1-homeassistantmigration,mutating=false,failurePolicy=fail,sideEffects=None,groups=ha.homeassistant.io,resources=homeassistantmigrations,verbs=create;update,versions=v1alpha1,name=vhomeassistantmigration-v1alpha1.kb.io,admissionReviewVersions=v1

type HomeAssistantMigrationCustomValidator struct{}

var _ admission.Validator[*hav1alpha1.HomeAssistantMigration] = &HomeAssistantMigrationCustomValidator{}

func (v *HomeAssistantMigrationCustomValidator) ValidateCreate(
	_ context.Context,
	migration *hav1alpha1.HomeAssistantMigration,
) (admission.Warnings, error) {
	if len(migration.Spec.ExecutionRequests) > 0 || len(migration.Spec.ExecutionAttempts) > 0 ||
		len(migration.Spec.ExecutionDispatches) > 0 || migration.Spec.ProxyCredentialBinding != nil {
		return nil, fmt.Errorf("new migration cannot contain execution records")
	}
	return nil, validateHomeAssistantMigration(migration)
}

//nolint:gocyclo // Validation and lifecycle branches are intentionally fail-closed.
func (v *HomeAssistantMigrationCustomValidator) ValidateUpdate(
	_ context.Context,
	old, current *hav1alpha1.HomeAssistantMigration,
) (admission.Warnings, error) {
	if old.Spec.ExecutionProtocol != current.Spec.ExecutionProtocol {
		return nil, fmt.Errorf("execution protocol is immutable")
	}
	if old.Spec.ProxyCredentialBinding != nil && !reflect.DeepEqual(old.Spec.ProxyCredentialBinding, current.Spec.ProxyCredentialBinding) {
		return nil, fmt.Errorf("proxy credential binding is immutable")
	}
	if old.Spec.ProxyCredentialBinding == nil && current.Spec.ProxyCredentialBinding != nil && (current.Spec.CancelRequested || migrationprotocol.AbortRequested(current)) {
		return nil, fmt.Errorf("proxy credential binding is blocked")
	}
	for _, r := range old.Spec.ExecutionRequests {
		found := false
		for _, n := range current.Spec.ExecutionRequests {
			if reflect.DeepEqual(r, n) {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("execution requests are append-only")
		}
	}
	for _, a := range old.Spec.ExecutionAttempts {
		found := false
		for _, n := range current.Spec.ExecutionAttempts {
			if a == n {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("execution attempts are append-only")
		}
	}
	for _, a := range old.Spec.ExecutionDispatches {
		found := false
		for _, n := range current.Spec.ExecutionDispatches {
			if a == n {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("execution dispatches are append-only")
		}
	}
	if migrationprotocol.AbortRequested(current) {
		for _, dispatch := range current.Spec.ExecutionDispatches {
			found := false
			for _, previous := range old.Spec.ExecutionDispatches {
				if dispatch == previous {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("new dispatches cannot be claimed after abort")
			}
		}
	}
	if len(old.Spec.ExecutionRequests) > 0 && !reflect.DeepEqual(old.Spec.Verification, current.Spec.Verification) {
		return nil, fmt.Errorf("accepted verification set is immutable")
	}
	for _, r := range current.Spec.ExecutionRequests {
		found := false
		for _, n := range old.Spec.ExecutionRequests {
			if reflect.DeepEqual(r, n) {
				found = true
			}
		}
		if !found {
			if err := migrationprotocol.ValidateNewRequest(current, r); err != nil {
				return nil, err
			}
		}
	}
	for _, stage := range []string{"backup", "upload", "restore"} {
		key := "ha.homeassistant.io/migration-request-" + stage
		if old.Annotations[key] != "" && current.Annotations[key] != old.Annotations[key] {
			return nil, fmt.Errorf("accepted migration request is immutable")
		}
	}
	if old.Annotations["ha.homeassistant.io/migration-request-backup"] != "" &&
		(old.Spec.SourceEndpoint != current.Spec.SourceEndpoint ||
			!reflect.DeepEqual(old.Spec.ArchiveStorage, current.Spec.ArchiveStorage) ||
			!reflect.DeepEqual(old.Spec.SourceTokenSecretRef, current.Spec.SourceTokenSecretRef) ||
			!reflect.DeepEqual(old.Spec.RecoveryKeySecretRef, current.Spec.RecoveryKeySecretRef)) {
		return nil, fmt.Errorf("accepted backup request inputs are immutable")
	}
	if old.Spec.SourceID != current.Spec.SourceID || old.Spec.Target != current.Spec.Target {
		return nil, fmt.Errorf("migration source and target are immutable")
	}
	if old.Spec.ReportID != current.Spec.ReportID || old.Spec.BackupScopeDigest != current.Spec.BackupScopeDigest {
		return nil, fmt.Errorf("migration report and backup scope are immutable")
	}
	if len(current.Spec.Resolutions) < len(old.Spec.Resolutions) || (len(old.Spec.Resolutions) > 0 && !reflect.DeepEqual(old.Spec.Resolutions, current.Spec.Resolutions[:len(old.Spec.Resolutions)])) {
		return nil, fmt.Errorf("migration resolutions are append-only")
	}
	return nil, validateHomeAssistantMigration(current)
}

func (v *HomeAssistantMigrationCustomValidator) ValidateDelete(
	_ context.Context,
	_ *hav1alpha1.HomeAssistantMigration,
) (admission.Warnings, error) {
	return nil, nil
}

//nolint:gocyclo // Validation and lifecycle branches are intentionally fail-closed.
func validateHomeAssistantMigration(migration *hav1alpha1.HomeAssistantMigration) error {
	if err := migrationprotocol.ValidateAccepted(migration); err != nil {
		return err
	}
	if err := migrationprotocol.ValidateApprovals(migration); err != nil {
		return err
	}
	if strings.TrimSpace(migration.Spec.SourceID) == "" ||
		strings.TrimSpace(migration.Spec.Target.Name) == "" ||
		strings.TrimSpace(migration.Spec.Target.UID) == "" {
		return fmt.Errorf("migration sourceID, target name, and target UID are required")
	}
	if endpoint := migration.Spec.SourceEndpoint; endpoint != "" {
		u, err := url.Parse(endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("source endpoint must be an HTTP URL without credentials, query, or fragment")
		}
		if endpoint != migration.Spec.SourceID {
			return fmt.Errorf("source endpoint must match the approved source identity")
		}
	}
	if ref := migration.Spec.SourceTokenSecretRef; ref != nil {
		if strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.Key) == "" || (ref.Optional != nil && *ref.Optional) {
			return fmt.Errorf("source token Secret reference must have a name, key, and be required")
		}
	}
	for _, resolution := range migration.Spec.Resolutions {
		if resolution.Version != "v1" || strings.TrimSpace(resolution.RequestID) == "" ||
			(resolution.Stage != "backup" && resolution.Stage != "upload" && resolution.Stage != "restore") ||
			(resolution.Decision != "ObserveExternalResult" && resolution.Decision != "Abort") {
			return fmt.Errorf("invalid migration resolution")
		}
	}
	if len(migration.Spec.Verification) > 256 {
		return fmt.Errorf("too many verification items")
	}
	verificationIDs := make(map[string]bool, len(migration.Spec.Verification))
	for _, item := range migration.Spec.Verification {
		identity := item.Kind + "\x00" + item.ID
		if strings.TrimSpace(item.Kind) == "" || strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.ExpectedDigest) == "" || verificationIDs[identity] {
			return fmt.Errorf("verification items must be complete and unique")
		}
		verificationIDs[identity] = true
	}
	if ref := migration.Spec.RecoveryKeySecretRef; ref != nil {
		if strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.Key) == "" || (ref.Optional != nil && *ref.Optional) {
			return fmt.Errorf("recovery key Secret reference must have a name, key, and be required")
		}
	}
	seen := map[string]struct{}{}
	for _, approval := range migration.Spec.Approvals {
		if strings.TrimSpace(approval.ID) == "" || strings.TrimSpace(approval.Digest) == "" {
			return fmt.Errorf("every migration approval requires an ID and digest")
		}
		if _, duplicate := seen[approval.ID]; duplicate {
			return fmt.Errorf("migration approval %q is duplicated", approval.ID)
		}
		seen[approval.ID] = struct{}{}
		if (approval.ID == "backup" || approval.ID == "backup-settings-stable") && approval.Approved &&
			(strings.TrimSpace(migration.Spec.ReportID) == "" ||
				strings.TrimSpace(migration.Spec.BackupScopeDigest) == "" ||
				approval.Digest != migration.Spec.ReportID ||
				approval.TargetUID != migration.Spec.Target.UID ||
				approval.ScopeDigest != migration.Spec.BackupScopeDigest) {
			return fmt.Errorf("backup approval is not bound to the report, target, and scope")
		}
		if (approval.ID == "resume" || approval.ID == "manual-cutover") && approval.Approved &&
			(migration.Spec.Archive == nil || strings.TrimSpace(migration.Spec.Archive.Digest) == "" ||
				approval.TargetUID != migration.Spec.Target.UID || approval.ArchiveDigest != migration.Spec.Archive.Digest) {
			return fmt.Errorf("%s approval is not bound to the target and archive", approval.ID)
		}
		if approval.ID == "restore" && approval.Approved {
			if migration.Spec.Archive == nil ||
				strings.TrimSpace(migration.Spec.Archive.BackupID) == "" ||
				strings.TrimSpace(migration.Spec.Archive.AgentID) == "" ||
				strings.TrimSpace(migration.Spec.Archive.Digest) == "" ||
				strings.TrimSpace(migration.Spec.RestoreScopeDigest) == "" {
				return fmt.Errorf("approved restore requires an archive and restore scope")
			}
			if approval.TargetUID != migration.Spec.Target.UID ||
				approval.ArchiveDigest != migration.Spec.Archive.Digest ||
				approval.ScopeDigest != migration.Spec.RestoreScopeDigest {
				return fmt.Errorf(
					"restore approval is not bound to the target, archive, and scope",
				)
			}
		}
	}
	return nil
}
