package controller

import (
	"context"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func migrationRestorePreparationEnabled(ha *hav1.HomeAssistant) bool {
	return ha.Spec.Alpha != nil && ha.Spec.Alpha.MigrationRestore != nil &&
		ha.Spec.Alpha.MigrationRestore.Enabled
}

// migrationRestoreStartAllowed defaults to true for ordinary HomeAssistant
// resources. An opted-in restore target is scaled to zero until its exact UID
// is bound to a durable operation approved for restore and source stop. Until
// onboarding isolation is implemented, even those approvals cannot safely
// expose the unauthenticated target and the workload remains stopped.
func (r *HomeAssistantReconciler) migrationRestoreStartAllowed(
	ctx context.Context,
	ha *hav1.HomeAssistant,
) (bool, error) {
	if !migrationRestorePreparationEnabled(ha) {
		return true, nil
	}
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	var migrations hav1alpha1.HomeAssistantMigrationList
	if err := reader.List(ctx, &migrations, client.InNamespace(ha.Namespace)); err != nil {
		return false, err
	}
	for _, migration := range migrations.Items {
		if migration.Spec.Target.Name != ha.Name ||
			migration.Spec.Target.UID != string(ha.UID) {
			continue
		}
		var restore *hav1alpha1.MigrationApproval
		for i := range migration.Spec.Approvals {
			if migration.Spec.Approvals[i].ID == "restore" {
				if restore != nil {
					return false, nil
				}
				restore = &migration.Spec.Approvals[i]
			}
		}
		if !restoreApprovalBound(&migration, restore) {
			continue
		}
		stopped, err := sourceStopVerified(ctx, reader, &migration)
		if err != nil {
			return false, err
		}
		if stopped {
			return false, nil
		}
	}
	return false, nil
}
