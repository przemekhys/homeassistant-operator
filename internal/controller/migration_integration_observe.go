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
	if integration.Annotations[migrationPausedAnnotation] == "true" {
		return ctrl.Result{}, nil
	}
	identity, err := migrationObjectIdentity(integration)
	if err != nil || integration.Spec.HomeAssistantRef.Name == "" {
		return ctrl.Result{}, nil
	}
	reason := "TargetUnavailable"
	ha := &hav1.HomeAssistant{}
	err = r.Get(
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
	if meta.SetStatusCondition(
		&integration.Status.Conditions,
		metav1.Condition{
			Type:               "MigrationAdoption",
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            "Integration adoption is blocked until its complete configuration can be verified",
			ObservedGeneration: integration.Generation,
		},
	) {
		if err := r.Status().Update(ctx, integration); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}
