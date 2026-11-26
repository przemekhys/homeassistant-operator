package controller

import (
	"context"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// observeImportedIntegration never enters config-flow or deletion paths. HA
// cannot reveal all config-flow values, so even an exact entry ID is not enough
// to authorize adoption or a future reconfiguration.
func (r *HomeAssistantIntegrationReconciler) observeImportedIntegration(
	ctx context.Context,
	integration *hav1.HomeAssistantIntegration,
) (ctrl.Result, error) {
	paused := integration.Annotations[migrationPausedAnnotation] == "true"
	identity, err := migrationObjectIdentity(integration)
	reason := "IncompleteIdentity"
	if paused {
		reason = "MigrationPaused"
	} else if err == nil && integration.Spec.HomeAssistantRef.Name != "" {
		reason = r.observeIntegrationTarget(ctx, integration, identity)
	}
	changed := meta.SetStatusCondition(&integration.Status.Conditions, metav1.Condition{
		Type: "MigrationAdoption", Status: metav1.ConditionFalse, Reason: reason,
		Message:            "Integration adoption is blocked until its complete configuration can be verified",
		ObservedGeneration: integration.Generation,
	})
	if meta.SetStatusCondition(&integration.Status.Conditions, metav1.Condition{
		Type: conditionTypeReady, Status: metav1.ConditionFalse, Reason: reason,
		Message:            "Imported integration is read-only; normal synchronisation is disabled",
		ObservedGeneration: integration.Generation,
	}) {
		changed = true
	}
	if changed {
		if err := r.Status().Update(ctx, integration); err != nil {
			return ctrl.Result{}, err
		}
	}
	if paused {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func (r *HomeAssistantIntegrationReconciler) observeIntegrationTarget(ctx context.Context, integration *hav1.HomeAssistantIntegration, identity migrationIdentity) string {
	reason := "TargetUnavailable"
	ha := &hav1.HomeAssistant{}
	err := r.Get(
		ctx,
		client.ObjectKey{
			Namespace: integration.Namespace,
			Name:      integration.Spec.HomeAssistantRef.Name,
		},
		ha,
	)
	if err == nil && string(ha.UID) != identity.TargetUID {
		reason = "TargetIdentityMismatch"
	} else if err == nil {
		reason = "TokenUnavailable"
		if token, tokenErr := getAPIToken(ctx, r.Client, ha); tokenErr == nil {
			reason = "IntegrationUnavailable"
			if _, readErr := r.haClientFor(ha).ReadMigrationIntegration(ctx, token, identity.SourceID); readErr == nil {
				reason = "IntegrationConfigurationIncomplete"
			}
		}
	}
	return reason
}
