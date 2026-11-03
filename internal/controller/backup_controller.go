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
	"fmt"
	"hash/fnv"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/coordination"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const (
	conditionTypeBackupConfigured     = "BackupConfigured"
	conditionTypeBackupHealthy        = "BackupHealthy"
	conditionTypeBackupKeySecured     = "BackupKeySecured"
	reasonBackupConfigured            = "BackupConfigured"
	reasonBackupConfigFailed          = "BackupConfigFailed"
	reasonBackupTokenNotAvailable     = "TokenNotAvailable"
	reasonBackupUnmanaged             = "BackupUnmanaged"
	reasonBackupSynchronizationPaused = "SynchronizationPaused"
	reasonBackupKeySecured            = "BackupKeyRetained"
	reasonBackupKeyUnavailable        = "BackupKeyUnavailable"
	reasonBackupKeyCoordinationFailed = "BackupKeyCoordinationFailed"
	reasonBackupKeyRetentionFailed    = "BackupKeyRetentionFailed"

	backupKeyObservationInterval = time.Minute
	backupKeyObservationJitter   = 6 * time.Second
)

// reconcileBackupConfig configures HA's built-in backup system via WebSocket API.
// Called after bootstrap — requires a valid API token.
func (r *HomeAssistantReconciler) reconcileBackupConfig(
	ctx context.Context,
	ha *hav1.HomeAssistant,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if ha.Spec.Backup == nil {
		if meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupConfigured) != nil {
			meta.RemoveStatusCondition(&ha.Status.Conditions, conditionTypeBackupConfigured)
			if err := r.Status().Update(ctx, ha); err != nil {
				log.Error(err, "Failed to clear BackupConfigured condition")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	// A missing or unreadable durable coordinator must fail closed before even
	// observing native configuration: otherwise key observation could recreate a
	// fresh Open root and inadvertently authorize a later ordinary update. A
	// present closed gate is handled below so KeyProtection observation remains
	// available during pauses and restore holds.
	if result, err := childSynchronizationGate(ctx, r.Coordination, r.Client, ha, ha.Name); err != nil {
		return result, err
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
	// Build HA client (operator always speaks HTTP to HA inside the cluster)
	haClient := coordinatedHAClient(ctx, ha, r.Coordination, r.Client, r.NewHAClient)

	// Get current backup config from HA
	currentConfig, err := haClient.GetBackupConfig(ctx, token)
	if err != nil {
		msg := backupConfigErrorMessage("get", err)
		log.Info(msg)
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
	// Key retention is observation-only and deliberately occurs before the
	// management and synchronization gates. Thus a reconnect-triggered reconcile
	// observes a rotated key immediately even while configuration writes pause.
	r.observeBackupKey(ctx, ha, currentConfig.CreateBackup.Password)

	if ha.Spec.Backup.ManagementPolicy == "Unmanaged" {
		meta.SetStatusCondition(
			&ha.Status.Conditions,
			metav1.Condition{
				Type:               conditionTypeBackupConfigured,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: ha.Generation,
				Reason:             reasonBackupUnmanaged,
				Message:            "Backup configuration is observed but not managed",
			},
		)
		if err := r.Status().Update(ctx, ha); err != nil {
			return ctrl.Result{}, err
		}
		return backupKeyObservationResult(ha), nil
	}
	if ha.Spec.Synchronization != nil && ha.Spec.Synchronization.Paused {
		meta.SetStatusCondition(
			&ha.Status.Conditions,
			metav1.Condition{
				Type:               conditionTypeBackupConfigured,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: ha.Generation,
				Reason:             reasonBackupSynchronizationPaused,
				Message:            "Backup configuration writes are suspended while synchronization is paused",
			},
		)
		if err := r.Status().Update(ctx, ha); err != nil {
			return ctrl.Result{}, err
		}
		return backupKeyObservationResult(ha), nil
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
		setBackupHealthUnknown(ha)
		if err := r.Status().Update(ctx, ha); err != nil {
			log.Error(err, "Failed to update backup status")
			return ctrl.Result{}, err
		}
		return backupKeyObservationResult(ha), nil
	}
	if desired.CreateBackup != nil {
		agents, err := haClient.ListBackupAgents(ctx, token)
		if err != nil {
			return r.backupConfigFailure(ctx, ha, "Unable to discover backup destinations")
		}
		if err := validateBackupAgents(desired.CreateBackup.AgentIDs, agents); err != nil {
			return r.backupConfigFailure(ctx, ha, "One or more configured backup destinations are unavailable")
		}
	}

	// Apply new config
	log.Info("Updating backup configuration in HA",
		"recurrence", desired.Schedule.Recurrence)
	if err := haClient.ConfigureBackup(ctx, token, desired); err != nil {
		msg := backupConfigErrorMessage("configure", err)
		log.Info(msg)
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
	setBackupHealthUnknown(ha)
	if err := r.Status().Update(ctx, ha); err != nil {
		log.Error(err, "Failed to update backup status")
		return ctrl.Result{}, err
	}

	return backupKeyObservationResult(ha), nil
}

// backupKeyObservationResult schedules a stable per-instance offset in the
// 54-66 second range. It avoids a fleet synchronizing while remaining stable
// across reconciles and restarts.
func backupKeyObservationResult(ha *hav1.HomeAssistant) ctrl.Result {
	return ctrl.Result{RequeueAfter: backupKeyObservationDelay(ha)}
}

func backupKeyObservationDelay(ha *hav1.HomeAssistant) time.Duration {
	key := string(ha.UID)
	if key == "" {
		key = ha.Namespace + "/" + ha.Name
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return backupKeyObservationInterval - backupKeyObservationJitter +
		time.Duration(h.Sum32()%uint32(2*backupKeyObservationJitter/time.Second+1))*time.Second
}

// observeBackupKey retains a nonempty observed native backup password without
// ever exposing it through status, events, errors, or logs. Its condition is
// intentionally independent of BackupConfigured: failure to retain a key must
// not misrepresent the state of native backup configuration. The KeyProtection
// permit is allowed while paused or held, but never alongside another permit.
func (r *HomeAssistantReconciler) observeBackupKey(ctx context.Context, ha *hav1.HomeAssistant, password *string) {
	if password == nil || *password == "" {
		setBackupKeySecuredCondition(ha, metav1.ConditionFalse, reasonBackupKeyUnavailable,
			"Native backup configuration did not provide a key to retain")
		return
	}
	log := logf.FromContext(ctx)
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	store := coordination.Store{Reader: reader, Client: r.Client}
	if _, err := store.CreateRoot(ctx, ha.Namespace, ha.UID); err != nil {
		setBackupKeySecuredCondition(ha, metav1.ConditionFalse, reasonBackupKeyCoordinationFailed,
			"Backup key retention coordination is unavailable")
		log.V(1).Info("Backup key observation coordination unavailable")
		return
	}
	owner := "backup-key-observation/" + string(ha.UID)
	permit, err := store.AcquirePermit(ctx, ha.Namespace, ha.UID, owner, "KeyProtection", ha.UID)
	if err != nil {
		// A held permit is expected during a native writer; do not retry around it.
		setBackupKeySecuredCondition(ha, metav1.ConditionFalse, reasonBackupKeyCoordinationFailed,
			"Backup key retention is waiting for coordination")
		log.V(1).Info("Backup key observation waiting for coordination permit")
		return
	}
	// This permit ID is durable before Retain can create a Secret. If its Create
	// response is uncertain, Retain reconciles the same preselected name.
	plan := BackupKeySecretPlan{Name: "backup-key-" + permit.ID}
	flightErr := store.TransitionPermit(ctx, ha.Namespace, ha.UID, owner, permit.ID, coordination.PermitInFlight)
	if flightErr != nil {
		setBackupKeySecuredCondition(ha, metav1.ConditionFalse, reasonBackupKeyCoordinationFailed,
			"Backup key retention coordination is unavailable")
		log.V(1).Info("Backup key observation permit transition unavailable")
		return
	}
	secret, err := (BackupKeyHistory{Client: r.Client}).Retain(ctx, ha, *password, plan)
	if err != nil {
		// The write could have reached the API server; preserve the safety
		// boundary rather than attempting a second name or write.
		_ = store.TransitionPermit(ctx, ha.Namespace, ha.UID, owner, permit.ID, coordination.PermitUnknown)
		setBackupKeySecuredCondition(ha, metav1.ConditionFalse, reasonBackupKeyRetentionFailed,
			"Observed backup key could not be retained")
		log.V(1).Info("Backup key observation could not retain key")
		return
	}
	if ha.Status.Backup == nil {
		ha.Status.Backup = &hav1.BackupStatus{}
	}
	ha.Status.Backup.CurrentKeySecretRef = &hav1.BackupKeySecretReference{
		Name: secret.Name,
		UID:  string(secret.UID),
		Key:  backupKeySecretDataKey,
	}
	setBackupKeySecuredCondition(ha, metav1.ConditionTrue, reasonBackupKeySecured,
		fmt.Sprintf("Observed backup key retained in Secret %q", secret.Name))
	confirmErr := store.TransitionPermit(ctx, ha.Namespace, ha.UID, owner, permit.ID, coordination.PermitConfirmed)
	if confirmErr != nil {
		log.V(1).Info("Backup key observation confirmation unavailable")
		return
	}
	if err := store.ReleasePermit(ctx, ha.Namespace, ha.UID, owner, permit.ID); err != nil {
		log.V(1).Info("Backup key observation permit release unavailable")
	}
}

// setBackupKeySecuredCondition records only the retention outcome and, on
// success, a Kubernetes Secret name. It must never include a password, digest,
// native API response, or underlying client error.
func setBackupKeySecuredCondition(ha *hav1.HomeAssistant, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type:               conditionTypeBackupKeySecured,
		Status:             status,
		ObservedGeneration: ha.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// backupConfigErrorMessage deliberately exposes only an operation and a bounded
// client error category. Native errors can include credentials or user data.
func backupConfigErrorMessage(operation string, err error) string {
	kind := "Unknown"
	var backupErr *haclient.BackupError
	if errors.As(err, &backupErr) {
		kind = string(backupErr.Kind)
	}
	return fmt.Sprintf("Unable to %s native backup configuration (%s)", operation, kind)
}

// setBackupHealthUnknown distinguishes an applied native schedule from a
// confirmed completed backup. Operation observation owns later transitions.
func setBackupHealthUnknown(ha *hav1.HomeAssistant) {
	if meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupHealthy) != nil {
		return
	}
	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type:               conditionTypeBackupHealthy,
		Status:             metav1.ConditionUnknown,
		ObservedGeneration: ha.Generation,
		Reason:             "BackupOutcomeUnknown",
		Message:            "No completed native backup has been observed",
	})
}

// setBackupHealth projects the outcome of a native backup separately from the
// desired backup configuration. A configured schedule is never evidence of a
// completed archive.
func setBackupHealth(ha *hav1.HomeAssistant, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type:               conditionTypeBackupHealthy,
		Status:             status,
		ObservedGeneration: ha.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func (r *HomeAssistantReconciler) backupConfigFailure(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	message string,
) (ctrl.Result, error) {
	r.emitBackupEvent(ha, corev1.EventTypeWarning, "BackupConfigFailed", message)
	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type:               conditionTypeBackupConfigured,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: ha.Generation,
		Reason:             reasonBackupConfigFailed,
		Message:            message,
	})
	if err := r.Status().Update(ctx, ha); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func validateBackupAgents(required []string, available []haclient.BackupAgent) error {
	known := make(map[string]struct{}, len(available))
	for _, agent := range available {
		known[agent.ID] = struct{}{}
	}
	for _, agentID := range required {
		if _, ok := known[agentID]; !ok {
			return fmt.Errorf("backup destination unavailable")
		}
	}
	return nil
}

// buildBackupConfigRequest converts BackupSpec to haclient.BackupConfigRequest
func (r *HomeAssistantReconciler) buildBackupConfigRequest(spec *hav1.BackupSpec) *haclient.BackupConfigRequest {
	if !spec.Enabled {
		return &haclient.BackupConfigRequest{Schedule: &haclient.BackupSchedule{Recurrence: "never"}}
	}

	recurrence, days := nativeBackupRecurrence(spec.Recurrence)
	req := &haclient.BackupConfigRequest{
		Schedule: &haclient.BackupSchedule{
			Recurrence: recurrence,
			Days:       days,
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

func nativeBackupRecurrence(recurrence string) (string, []string) {
	switch recurrence {
	case "mon", "tue", "wed", "thu", "fri", "sat", "sun":
		return "custom_days", []string{recurrence}
	case "never":
		return "never", nil
	case "", "daily":
		return "daily", nil
	default:
		return recurrence, nil
	}
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
		if !stringSliceEqual(current.Schedule.Days, desired.Schedule.Days) {
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
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
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
	ha *hav1.HomeAssistant,
	eventType, reason, message string,
) {
	if r.Recorder != nil {
		r.Recorder.Eventf(ha, nil, eventType, reason, reason, message)
	}
}
