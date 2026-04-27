/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const (
	conditionTypeBackupConfigured = "BackupConfigured"
	reasonBackupConfigured        = "BackupConfigured"
	reasonBackupConfigFailed      = "BackupConfigFailed"
	reasonBackupTokenNotAvailable = "TokenNotAvailable"
)

// reconcileBackupConfig configures HA's built-in backup system via WebSocket API.
// Called after bootstrap — requires a valid API token.
func (r *HomeAssistantReconciler) reconcileBackupConfig(
	ctx context.Context,
	ha *hav1alpha1.HomeAssistant,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// If backup not enabled, clear condition if it was set and return
	if ha.Spec.Backup == nil || !ha.Spec.Backup.Enabled {
		if meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupConfigured) != nil {
			meta.RemoveStatusCondition(&ha.Status.Conditions, conditionTypeBackupConfigured)
			if err := r.Status().Update(ctx, ha); err != nil {
				log.Error(err, "Failed to clear BackupConfigured condition")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Get API token
	token, err := getAPIToken(ctx, r.Client, ha)
	if err != nil {
		log.Info("API token not available for backup config, will retry")
		meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
			Type:               conditionTypeBackupConfigured,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: ha.Generation,
			Reason:             reasonBackupTokenNotAvailable,
			Message:            "API token not available — bootstrap may not be complete",
		})
		if err := r.Status().Update(ctx, ha); err != nil {
			log.Error(err, "Failed to update backup status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Build HA client
	haURL := buildHomeAssistantURL(ha)
	var haClient *haclient.Client
	if r.NewHAClient != nil {
		haClient = r.NewHAClient(haURL)
	} else {
		haClient = haclient.NewClient(haURL)
	}

	// Get current backup config from HA
	currentConfig, err := haClient.GetBackupConfig(ctx, token)
	if err != nil {
		log.Error(err, "Failed to get backup config from HA")
		msg := fmt.Sprintf("Failed to get backup config: %v", err)
		r.emitBackupEvent(ha, corev1.EventTypeWarning, "BackupConfigFailed", msg)
		meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
			Type:               conditionTypeBackupConfigured,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: ha.Generation,
			Reason:             reasonBackupConfigFailed,
			Message:            fmt.Sprintf("Failed to get backup config: %v", err),
		})
		if err := r.Status().Update(ctx, ha); err != nil {
			log.Error(err, "Failed to update backup status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Build desired config from spec
	desired := r.buildBackupConfigRequest(ha.Spec.Backup)

	// Compare and update only if needed
	if !r.needsBackupUpdate(currentConfig, desired) {
		log.V(1).Info("Backup config already matches desired state")
		meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
			Type:               conditionTypeBackupConfigured,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: ha.Generation,
			Reason:             reasonBackupConfigured,
			Message:            "Backup configuration is up to date",
		})
		if err := r.Status().Update(ctx, ha); err != nil {
			log.Error(err, "Failed to update backup status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Apply new config
	log.Info("Updating backup configuration in HA",
		"recurrence", desired.Schedule.Recurrence)
	if err := haClient.ConfigureBackup(ctx, token, desired); err != nil {
		log.Error(err, "Failed to configure backup in HA")
		msg := fmt.Sprintf("Failed to configure backup: %v", err)
		r.emitBackupEvent(ha, corev1.EventTypeWarning, "BackupConfigFailed", msg)
		meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
			Type:               conditionTypeBackupConfigured,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: ha.Generation,
			Reason:             reasonBackupConfigFailed,
			Message:            fmt.Sprintf("Failed to configure backup: %v", err),
		})
		if err := r.Status().Update(ctx, ha); err != nil {
			log.Error(err, "Failed to update backup status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	log.Info("Backup configuration updated successfully")
	r.emitBackupEvent(ha, corev1.EventTypeNormal, "BackupConfigured", "Backup configuration applied successfully")
	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type:               conditionTypeBackupConfigured,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: ha.Generation,
		Reason:             reasonBackupConfigured,
		Message:            "Backup configuration applied successfully",
	})
	if err := r.Status().Update(ctx, ha); err != nil {
		log.Error(err, "Failed to update backup status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// buildBackupConfigRequest converts BackupSpec to haclient.BackupConfigRequest
func (r *HomeAssistantReconciler) buildBackupConfigRequest(spec *hav1alpha1.BackupSpec) *haclient.BackupConfigRequest {
	req := &haclient.BackupConfigRequest{
		Schedule: &haclient.BackupSchedule{
			Recurrence: spec.Recurrence,
		},
		Retention:    &haclient.BackupRetention{},
		CreateBackup: &haclient.BackupCreateConfig{},
	}

	// Time: empty string in spec → nil (HA picks automatically)
	if spec.Time != "" {
		t := spec.Time
		req.Schedule.Time = &t
	}

	// Retention
	if spec.RetentionCopies != nil {
		copies := int(*spec.RetentionCopies)
		req.Retention.Copies = &copies
	}
	if spec.RetentionDays != nil {
		days := int(*spec.RetentionDays)
		req.Retention.Days = &days
	}

	// CreateBackup
	req.CreateBackup.IncludeDatabase = spec.IncludeDatabase
	if len(spec.AgentIDs) > 0 {
		req.CreateBackup.AgentIDs = spec.AgentIDs
	} else {
		req.CreateBackup.AgentIDs = []string{"backup.local"}
	}

	return req
}

// needsBackupUpdate compares current HA backup config with desired state.
func (r *HomeAssistantReconciler) needsBackupUpdate(
	current *haclient.BackupConfig,
	desired *haclient.BackupConfigRequest,
) bool {
	// Compare schedule
	if desired.Schedule != nil {
		if current.Schedule.Recurrence != desired.Schedule.Recurrence {
			return true
		}
		if !ptrStringEqual(current.Schedule.Time, desired.Schedule.Time) {
			return true
		}
	}

	// Compare retention
	if desired.Retention != nil {
		if !ptrIntEqual(current.Retention.Copies, desired.Retention.Copies) {
			return true
		}
		if !ptrIntEqual(current.Retention.Days, desired.Retention.Days) {
			return true
		}
	}

	// Compare create_backup
	if desired.CreateBackup != nil {
		if !ptrBoolEqual(current.CreateBackup.IncludeDatabase, desired.CreateBackup.IncludeDatabase) {
			return true
		}
		if !stringSliceEqual(current.CreateBackup.AgentIDs, desired.CreateBackup.AgentIDs) {
			return true
		}
	}

	return false
}

// ptrStringEqual compares two *string values (nil-safe).
func ptrStringEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// ptrIntEqual compares two *int values (nil-safe).
func ptrIntEqual(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// ptrBoolEqual compares two *bool values (nil-safe).
func ptrBoolEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// stringSliceEqual compares two string slices (order-sensitive).
func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// emitBackupEvent emits a Kubernetes event for backup operations.
func (r *HomeAssistantReconciler) emitBackupEvent(
	ha *hav1alpha1.HomeAssistant,
	eventType, reason, message string,
) {
	if r.Recorder != nil {
		r.Recorder.Eventf(ha, nil, eventType, reason, reason, message)
	}
}
