package controller

import (
	"context"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// observeImportedDefinition is deliberately read-only with respect to HA.
// Automation remains unqualified because its enabled state is not in the definition.
func observeImportedDefinition(
	ctx context.Context,
	kube client.Client,
	obj client.Object,
	kind, refName, specID string,
	newClient func(*hav1.HomeAssistant) *haclient.Client,
) (ctrl.Result, error) {
	paused := obj.GetAnnotations()[migrationPausedAnnotation] == "true"
	identity, err := migrationObjectIdentity(obj)
	result := migrationAdoptionResult{
		State:  migrationAdoptionBlocked,
		Reason: "IncompleteIdentity",
	}
	ha := &hav1.HomeAssistant{}
	if paused {
		result.State, result.Reason = migrationAdoptionPaused, "MigrationPaused"
	} else if err == nil && specID != "" && specID == identity.SourceID && refName != "" {
		result.Reason = "TargetUnavailable"
		err = kube.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: refName}, ha)
		if err == nil && string(ha.UID) == identity.TargetUID {
			result.Reason = "TokenUnavailable"
			if token, tokenErr := getAPIToken(ctx, kube, ha); tokenErr == nil {
				result.Reason = "DefinitionUnavailable"
				if definition, readErr := newClient(ha).ReadMigrationDefinition(ctx, token, kind, identity.SourceID); readErr == nil {
					result = compareMigrationDefinition(obj, identity.SourceID, definition)
					if result.State == migrationAdoptionAdopted && kind == "automation" {
						result.State = migrationAdoptionBlocked
						result.Reason = "AdoptionNotQualified"
					}
				}
			}
		} else if err == nil {
			result.Reason = "TargetIdentityMismatch"
		}
	}
	if kind != "automation" && result.State == migrationAdoptionConflict &&
		validMigrationOverwriteAnnotation(
			obj.GetAnnotations()[migrationOverwriteApprovalAnnotation],
			string(obj.GetUID()),
			obj.GetGeneration(),
			identity,
			result.ComparisonDigest,
		) {
		result.State = migrationAdoptionBlocked
		result.Reason = "UnsafeOverwriteUnsupported"
	}
	var conditions *[]metav1.Condition
	switch v := obj.(type) {
	case *hav1.HomeAssistantAutomation:
		conditions = &v.Status.Conditions
	case *hav1.HomeAssistantScene:
		conditions = &v.Status.Conditions
	case *hav1.HomeAssistantScript:
		conditions = &v.Status.Conditions
	default:
		return ctrl.Result{}, nil
	}
	status := metav1.ConditionFalse
	if result.State == migrationAdoptionAdopted {
		status = metav1.ConditionTrue
		result.Reason = "Adopted"
	}
	changed := meta.SetStatusCondition(
		conditions,
		metav1.Condition{
			Type:               "MigrationAdoption",
			Status:             status,
			Reason:             result.Reason,
			Message:            redactedAdoptionDiff(result),
			ObservedGeneration: obj.GetGeneration(),
		},
	)
	if meta.SetStatusCondition(
		conditions,
		metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             result.Reason,
			Message:            "Imported resource is read-only; normal synchronization is disabled",
			ObservedGeneration: obj.GetGeneration(),
		},
	) {
		changed = true
	}
	if changed {
		if err := kube.Status().Update(ctx, obj); err != nil {
			return ctrl.Result{}, err
		}
	}
	if paused {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}
