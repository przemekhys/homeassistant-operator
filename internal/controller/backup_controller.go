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
	"errors"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const (
	conditionTypeBackupConfigured = "BackupConfigured"
	reasonBackupConfigured        = "BackupConfigured"
	reasonBackupConfigFailed      = "BackupConfigFailed"
	reasonBackupTokenNotAvailable = "TokenNotAvailable"
	reasonBackupUnmanaged         = "Unmanaged"
	reasonBackupAgentUnavailable  = "AgentUnavailable"
)

// earliestRequeue preserves the shortest outstanding backup deadline while
// allowing configuration, observation, and a one-shot request to run in one
// reconcile. A zero duration means no requested requeue.
func earliestRequeue(results ...ctrl.Result) ctrl.Result {
	var earliest time.Duration
	for _, result := range results {
		if result.RequeueAfter > 0 && (earliest == 0 || result.RequeueAfter < earliest) {
			earliest = result.RequeueAfter
		}
	}
	return ctrl.Result{RequeueAfter: earliest}
}

// reconcileBackupConfig configures HA's built-in backup system via WebSocket API.
// Called after bootstrap — requires a valid API token.
func (r *HomeAssistantReconciler) reconcileBackupConfig(
	ctx context.Context,
	ha *hav1.HomeAssistant,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	managed := ha.Spec.Backup != nil && ha.Spec.Backup.Enabled

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

	// Build HA client (operator always speaks HTTP to HA inside the cluster)
	haClient := newHAClientForHA(ha, r.NewHAClient)

	// Get current backup config from HA
	currentConfig, err := haClient.GetBackupConfig(ctx, token)
	if err != nil {
		log.Info("Failed to get backup config from HA")
		reason, message := backupConfigReadFailure(err)
		r.emitBackupEvent(ha, corev1.EventTypeWarning, reason, message)
		meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
			Type:               conditionTypeBackupConfigured,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: ha.Generation,
			Reason:             reason,
			Message:            message,
		})
		if err := r.Status().Update(ctx, ha); err != nil {
			log.Error(err, "Failed to update backup status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if !managed {
		meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
			Type: conditionTypeBackupConfigured, Status: metav1.ConditionUnknown,
			ObservedGeneration: ha.Generation, Reason: reasonBackupUnmanaged,
			Message: "backup configuration is unmanaged",
		})
		if err := r.Status().Update(ctx, ha); err != nil {
			return ctrl.Result{}, err
		}
		observationResult, err := r.reconcileBackupObservation(ctx, ha, haClient, token, currentConfig)
		if err != nil {
			return observationResult, err
		}
		requestResult, err := r.reconcileBackupRequest(ctx, ha, haClient, token, currentConfig)
		return earliestRequeue(observationResult, requestResult), err
	}

	// Observation and key retention remain independent of the requested agents.
	observationResult, err := r.reconcileBackupObservation(ctx, ha, haClient, token, currentConfig)
	if err != nil {
		return observationResult, err
	}

	// Build desired config from spec
	desired := r.buildBackupConfigRequest(ha.Spec.Backup)
	availableAgents, err := haClient.ListBackupAgents(ctx, token)
	if err != nil {
		meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
			Type: conditionTypeBackupConfigured, Status: metav1.ConditionUnknown,
			ObservedGeneration: ha.Generation, Reason: "ObservationUnavailable",
			Message: "cannot verify requested backup destinations",
		})
		if statusErr := r.Status().Update(ctx, ha); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return earliestRequeue(observationResult, ctrl.Result{RequeueAfter: 30 * time.Second}), nil
	}
	if missingBackupAgent(desired.CreateBackup.AgentIDs, availableAgents) {
		meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
			Type: conditionTypeBackupConfigured, Status: metav1.ConditionFalse,
			ObservedGeneration: ha.Generation, Reason: reasonBackupAgentUnavailable,
			Message: "a requested backup destination is unavailable",
		})
		if statusErr := r.Status().Update(ctx, ha); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return earliestRequeue(observationResult, ctrl.Result{RequeueAfter: 30 * time.Second}), nil
	}

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
		requestResult, err := r.reconcileBackupRequest(ctx, ha, haClient, token, currentConfig)
		return earliestRequeue(observationResult, requestResult), err
	}

	// Apply new config
	log.Info("Updating backup configuration in HA",
		"recurrence", desired.Schedule.Recurrence)
	if err := haClient.ConfigureBackup(ctx, token, desired); err != nil {
		log.Info("Failed to configure backup in HA")
		msg := "Home Assistant rejected or did not accept backup configuration"
		r.emitBackupEvent(ha, corev1.EventTypeWarning, "BackupConfigFailed", msg)
		meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
			Type:               conditionTypeBackupConfigured,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: ha.Generation,
			Reason:             reasonBackupConfigFailed,
			Message:            msg,
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

	// Read the configuration again before a one-shot request so it always uses
	// settings HA has acknowledged rather than a desired local approximation.
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// backupConfigReadFailure maps native capability errors to safe status messages.
func backupConfigReadFailure(err error) (string, string) {
	var haErr *haclient.Error
	if errors.As(err, &haErr) {
		switch haErr.Code {
		case "unknown_command":
			return "Unsupported", "backup commands are not supported by this Home Assistant instance"
		case "unauthorized", "forbidden":
			return "PermissionDenied", "permission to read backup configuration was denied"
		}
	}
	return reasonBackupConfigFailed, "cannot read backup configuration"
}

// missingBackupAgent prevents silently substituting another destination.
func missingBackupAgent(expected []string, available []haclient.BackupAgent) bool {
	seen := make(map[string]struct{}, len(available))
	for _, agent := range available {
		seen[agent.ID] = struct{}{}
	}
	for _, id := range expected {
		if _, found := seen[id]; !found {
			return true
		}
	}
	return false
}

// buildBackupConfigRequest converts BackupSpec to haclient.BackupConfigRequest
func (r *HomeAssistantReconciler) buildBackupConfigRequest(spec *hav1.BackupSpec) *haclient.BackupConfigRequest {
	req := &haclient.BackupConfigRequest{
		Schedule: &haclient.BackupSchedule{
			Recurrence: "daily",
		},
		Retention:    &haclient.BackupRetention{},
		CreateBackup: &haclient.BackupCreateConfig{},
	}
	if spec.Recurrence != "" {
		req.Schedule.Recurrence = spec.Recurrence
	}
	if isWeekday(spec.Recurrence) {
		req.Schedule.Recurrence = "custom_days"
		req.Schedule.Days = []string{spec.Recurrence}
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
	includeDatabase := true
	if spec.IncludeDatabase != nil {
		includeDatabase = *spec.IncludeDatabase
	}
	req.CreateBackup.IncludeDatabase = &includeDatabase
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
		if !stringSliceSetEqual(current.Schedule.Days, desired.Schedule.Days) {
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
		if !stringSliceSetEqual(current.CreateBackup.AgentIDs, desired.CreateBackup.AgentIDs) {
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

// stringSliceSetEqual compares destination sets independently of ordering.
func stringSliceSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa, bb := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	return stringSliceEqual(aa, bb)
}

// isWeekday recognizes individual weekday schedule values.
func isWeekday(value string) bool {
	switch value {
	case "mon", "tue", "wed", "thu", "fri", "sat", "sun":
		return true
	default:
		return false
	}
}

// emitBackupEvent emits a Kubernetes event for backup operations.
func (r *HomeAssistantReconciler) emitBackupEvent(
	ha *hav1.HomeAssistant,
	eventType, reason, message string,
) {
	if r.Recorder != nil {
		r.Recorder.Eventf(ha, nil, eventType, reason, reason, message)
	}
}
