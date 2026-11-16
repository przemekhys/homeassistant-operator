package migration

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

type ImportRequest struct {
	Report    *Report
	ReportID  string
	Name      string
	Namespace string
	Confirmed bool
	// SourceTokenSecretRef identifies an existing source credential, not its value.
	SourceTokenSecretRef *corev1.SecretKeySelector
	// RecoveryKeySecretRef points to an existing Secret; the value is not imported.
	RecoveryKeySecretRef *corev1.SecretKeySelector
	// SecretData contains only source values that were actually available to
	// the caller. It is optional and never originates from a Report.
	SecretData map[string][]byte
	Scheme     *runtime.Scheme
}

type ImportResult struct {
	Migration *hav1alpha1.HomeAssistantMigration
	Reused    bool
	Secret    *corev1.Secret `json:"-"`
	Partial   bool
}

// Import creates the durable migration operation only after validating the
// exact report and target. Only mapped kinds can be imported; this is
// create-only and never updates a collision.
func Import(
	ctx context.Context,
	kube client.Client,
	request ImportRequest,
) (*ImportResult, error) {
	if kube == nil || request.Report == nil || !request.Confirmed ||
		strings.TrimSpace(request.Name) == "" || strings.TrimSpace(request.Namespace) == "" {
		return nil, fmt.Errorf(
			"migration import requires a report, namespace, name, and explicit confirmation",
		)
	}
	report := *request.Report
	report.Objects = append([]ReportObject(nil), request.Report.Objects...)
	if err := report.Finalize(); err != nil {
		return nil, err
	}
	if request.ReportID == "" || request.ReportID != report.ID {
		return nil, fmt.Errorf("migration import report ID does not match report content")
	}
	if request.Namespace != report.Target.Namespace {
		return nil, fmt.Errorf("migration import namespace does not match approved report")
	}
	if request.SourceTokenSecretRef != nil &&
		(strings.TrimSpace(request.SourceTokenSecretRef.Name) == "" || strings.TrimSpace(request.SourceTokenSecretRef.Key) == "" ||
			(request.SourceTokenSecretRef.Optional != nil && *request.SourceTokenSecretRef.Optional)) {
		return nil, fmt.Errorf("source token Secret reference requires a name and key")
	}
	if ref := request.RecoveryKeySecretRef; ref != nil &&
		(strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.Key) == "" ||
			(ref.Optional != nil && *ref.Optional)) {
		return nil, fmt.Errorf("recovery key Secret reference requires a name and key")
	}
	if err := ConvertReport(&report); err != nil {
		return nil, err
	}
	for _, object := range report.Objects {
		if object.Classification == ClassificationImportable && request.Scheme == nil {
			return nil, fmt.Errorf("migration import requires a scheme for owned resources")
		}
	}
	target := &hav1.HomeAssistant{}
	targetKey := types.NamespacedName{Namespace: request.Namespace, Name: report.Target.Name}
	if err := kube.Get(ctx, targetKey, target); err != nil {
		return nil, fmt.Errorf("read migration target: %w", err)
	}
	if string(target.UID) != report.Target.UID {
		return nil, fmt.Errorf("migration target UID does not match approved report")
	}
	backupScopeDigest := ""
	if report.Backup != nil && report.Backup.Status == "Ready" {
		var err error
		backupScopeDigest, err = BackupScopeDigest(report.Backup)
		if err != nil {
			return nil, fmt.Errorf("approved report has invalid backup scope")
		}
	}
	migration := &hav1alpha1.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{Name: request.Name, Namespace: request.Namespace},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			ExecutionProtocol: "v1",
			SourceID:          report.SourceID,
			Target: hav1alpha1.MigrationTarget{
				Name: report.Target.Name,
				UID:  report.Target.UID,
			},
			ReportID:             report.ID,
			BackupScopeDigest:    backupScopeDigest,
			SourceTokenSecretRef: request.SourceTokenSecretRef,
			RecoveryKeySecretRef: request.RecoveryKeySecretRef,
			Approvals: []hav1alpha1.MigrationApproval{
				{ID: "import", Digest: report.ID, Approved: true},
			},
		},
	}
	// The CLI uses the validated URL as the report's source identity. Legacy
	// reports with opaque identities remain importable but cannot start backup.
	if u, err := url.Parse(report.SourceID); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
		migration.Spec.SourceEndpoint = report.SourceID
	}
	createErr := kube.Create(ctx, migration)
	result := &ImportResult{Migration: migration}
	if createErr != nil {
		if !apierrors.IsAlreadyExists(createErr) {
			return nil, fmt.Errorf("create migration operation: %w", createErr)
		}
		existing := &hav1alpha1.HomeAssistantMigration{}
		key := types.NamespacedName{Namespace: request.Namespace, Name: request.Name}
		if getErr := kube.Get(ctx, key, existing); getErr != nil {
			return nil, fmt.Errorf("create migration operation: %w", createErr)
		}
		if existing.Spec.SourceID != migration.Spec.SourceID ||
			(existing.Spec.ExecutionProtocol != "" && existing.Spec.ExecutionProtocol != migration.Spec.ExecutionProtocol) ||
			existing.Spec.SourceEndpoint != migration.Spec.SourceEndpoint ||
			existing.Spec.Target != migration.Spec.Target ||
			existing.Spec.ReportID != migration.Spec.ReportID ||
			existing.Spec.BackupScopeDigest != migration.Spec.BackupScopeDigest ||
			!reflect.DeepEqual(existing.Spec.RecoveryKeySecretRef, migration.Spec.RecoveryKeySecretRef) ||
			!reflect.DeepEqual(existing.Spec.SourceTokenSecretRef, migration.Spec.SourceTokenSecretRef) {
			return nil, fmt.Errorf(
				"migration operation name collides with different approved intent",
			)
		}
		result.Migration, result.Reused = existing, true
	}
	for _, object := range report.Objects {
		if object.Classification != ClassificationImportable {
			continue
		}
		if err := importFloor(ctx, kube, request.Scheme, result.Migration, report.Target, object); err != nil {
			result.Partial = true
			return result, fmt.Errorf("migration import is partial: %w", err)
		}
	}
	if len(request.SecretData) != 0 {
		secret, err := CreateImportedSecret(
			ctx,
			kube,
			request.Scheme,
			result.Migration,
			request.SecretData,
		)
		if err != nil {
			result.Partial = true
			return result, fmt.Errorf("migration import is partial: %w", err)
		}
		result.Secret = secret
	}
	return result, nil
}

func importFloor(ctx context.Context, kube client.Client, scheme *runtime.Scheme,
	operation *hav1alpha1.HomeAssistantMigration, target Target, object ReportObject,
) error {
	if operation.UID == "" {
		return fmt.Errorf("migration UID is missing")
	}
	proposal, err := decodeFloorProposal(object)
	if err != nil {
		return err
	}
	floor := &hav1.HomeAssistantFloor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      object.Name,
			Namespace: operation.Namespace,
			Annotations: map[string]string{
				"ha.homeassistant.io/migration-imported":   "true",
				"ha.homeassistant.io/migration-paused":     "true",
				"ha.homeassistant.io/migration-source-id":  object.SourceID,
				"ha.homeassistant.io/migration-target-uid": target.UID,
			},
		},
		Spec: hav1.HomeAssistantFloorSpec{
			HomeAssistantRef: hav1.HomeAssistantReference{Name: target.Name},
			Name:             proposal.Name,
			Icon:             proposal.Icon,
			Level:            proposal.Level,
		},
	}
	if err := controllerutil.SetControllerReference(operation, floor, scheme); err != nil {
		return fmt.Errorf("cannot set resource owner")
	}
	if err := kube.Create(ctx, floor); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create floor: %w", err)
		}
		current := &hav1.HomeAssistantFloor{}
		if err := kube.Get(ctx, client.ObjectKeyFromObject(floor), current); err != nil {
			return fmt.Errorf("read existing floor: %w", err)
		}
		owner := metav1.GetControllerOf(current)
		if owner == nil || owner.UID != operation.UID ||
			!reflect.DeepEqual(current.Spec, floor.Spec) ||
			current.Annotations["ha.homeassistant.io/migration-imported"] != "true" ||
			current.Annotations["ha.homeassistant.io/migration-paused"] != "true" ||
			current.Annotations["ha.homeassistant.io/migration-source-id"] != object.SourceID ||
			current.Annotations["ha.homeassistant.io/migration-target-uid"] != target.UID {
			return fmt.Errorf("floor name collides with existing resource")
		}
	}
	return nil
}
