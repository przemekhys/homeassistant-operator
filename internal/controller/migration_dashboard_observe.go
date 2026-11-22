package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// findMigrationDashboardID refuses ambiguous IDs and never falls back to path.
func findMigrationDashboardID(dashboards []haclient.Dashboard, id string) *haclient.Dashboard {
	var match *haclient.Dashboard
	for i := range dashboards {
		if id != "" && dashboards[i].ID == id {
			if match != nil {
				return nil
			}
			match = &dashboards[i]
		}
	}
	return match
}

func (r *HomeAssistantDashboardReconciler) observeImportedDashboard(
	ctx context.Context,
	dashboard *hav1alpha1.HomeAssistantDashboard,
) (ctrl.Result, error) {
	paused := dashboard.Annotations[migrationPausedAnnotation] == "true"
	identity, err := migrationObjectIdentity(dashboard)
	reason := "IncompleteIdentity"
	digest := ""
	ha := &hav1.HomeAssistant{}
	if paused {
		reason = "MigrationPaused"
	} else if err == nil && dashboard.Spec.HomeAssistantRef.Name != "" {
		reason = "TargetUnavailable"
		err = r.Get(ctx, client.ObjectKey{Namespace: dashboard.Namespace, Name: dashboard.Spec.HomeAssistantRef.Name}, ha)
		if err == nil && string(ha.UID) != identity.TargetUID {
			reason = "TargetIdentityMismatch"
		} else if err == nil {
			reason = "TokenUnavailable"
			if token, tokenErr := getAPIToken(ctx, r.Client, ha); tokenErr == nil {
				reason, digest = r.compareImportedDashboard(ctx, dashboard, ha, token, identity)
				if digest != "" && validMigrationOverwriteAnnotation(dashboard.Annotations[migrationOverwriteApprovalAnnotation], string(dashboard.UID), dashboard.Generation, identity, digest) {
					reason = "UnsafeOverwriteUnsupported"
				}
			}
		}
	}
	status, message := metav1.ConditionFalse, "Dashboard adoption remains blocked pending verification"
	if digest != "" {
		message += "; comparison=" + digest
	}
	if reason == "Adopted" {
		status, message = metav1.ConditionTrue, "Existing dashboard verified without modifying Home Assistant"
	}
	changed := false
	if reason == "Adopted" &&
		(dashboard.Status.DashboardID != identity.SourceID || dashboard.Status.ObservedGeneration != dashboard.Generation) {
		dashboard.Status.DashboardID = identity.SourceID
		dashboard.Status.ObservedGeneration = dashboard.Generation
		changed = true
	}
	if meta.SetStatusCondition(
		&dashboard.Status.Conditions,
		metav1.Condition{
			Type:               "MigrationAdoption",
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: dashboard.Generation,
		},
	) {
		changed = true
	}
	if meta.SetStatusCondition(
		&dashboard.Status.Conditions,
		metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            "Imported resource is read-only; normal synchronization is disabled",
			ObservedGeneration: dashboard.Generation,
		},
	) {
		changed = true
	}
	if changed {
		if err := r.Status().Update(ctx, dashboard); err != nil {
			return ctrl.Result{}, err
		}
	}
	if paused {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func (r *HomeAssistantDashboardReconciler) compareImportedDashboard(
	ctx context.Context,
	dashboard *hav1alpha1.HomeAssistantDashboard,
	ha *hav1.HomeAssistant,
	token string,
	identity migrationIdentity,
) (string, string) {
	config, err := r.resolveConfig(ctx, dashboard)
	if err != nil {
		return "DefinitionUnavailable", ""
	}
	haClient := r.haClientFor(ha)
	raw, err := haClient.SendWebSocketCommand(ctx, token, "lovelace/dashboards/list", nil)
	if err != nil {
		return "DashboardUnavailable", ""
	}
	dashboards, err := migrationDashboardList(raw)
	if err != nil {
		return "DashboardMetadataIncomplete", ""
	}
	existing := findMigrationDashboardID(dashboards, identity.SourceID)
	if existing == nil {
		return "DashboardIdentityUnverified", ""
	}
	if existing.Mode != haclient.DashboardModeStorage {
		return "DashboardNotStorageManaged", ""
	}
	path := dashboard.Spec.URLPath
	if dashboard.Spec.DefaultDashboard {
		path = "lovelace"
	}
	metadataConflict := existing.URLPath != path || existing.Title != dashboard.Spec.Title ||
		existing.Icon != dashboard.Spec.Icon ||
		existing.ShowInSidebar != (dashboard.Spec.ShowInSidebar == nil || *dashboard.Spec.ShowInSidebar) ||
		existing.RequireAdmin != (dashboard.Spec.RequireAdmin != nil && *dashboard.Spec.RequireAdmin)
	actual, err := haClient.GetDashboardConfig(ctx, token, existing.URLPath)
	if err != nil {
		return "DefinitionUnavailable", ""
	}
	wanted, err := canonicalMigrationJSON(config)
	if err != nil {
		return "DefinitionUnavailable", ""
	}
	observed, err := canonicalMigrationJSON(actual)
	if err != nil {
		return "DefinitionUnavailable", ""
	}
	if metadataConflict || !bytes.Equal(wanted, observed) {
		wantedData, wantErr := json.Marshal(struct {
			Spec   hav1alpha1.HomeAssistantDashboardSpec `json:"spec"`
			Config json.RawMessage                       `json:"config"`
		}{dashboard.Spec, wanted})
		observedData, observedErr := json.Marshal(struct {
			Metadata haclient.Dashboard `json:"metadata"`
			Config   json.RawMessage    `json:"config"`
		}{*existing, observed})
		if wantErr != nil || observedErr != nil {
			return "DefinitionUnavailable", ""
		}
		digest := comparisonDigest(
			identity.SourceID,
			identity.TargetUID,
			wantedData,
			observedData,
		)
		if metadataConflict {
			return "DashboardMetadataConflict", digest
		}
		return "DashboardDefinitionConflict", digest
	}
	latestRaw, err := haClient.SendWebSocketCommand(ctx, token, "lovelace/dashboards/list", nil)
	if err != nil {
		return "DashboardUnavailable", ""
	}
	latest, err := migrationDashboardList(latestRaw)
	if err != nil {
		return "DashboardMetadataIncomplete", ""
	}
	current := findMigrationDashboardID(latest, identity.SourceID)
	if current == nil || *current != *existing {
		return "DashboardMetadataChanged", ""
	}
	return "Adopted", ""
}

// migrationDashboardList rejects incomplete metadata instead of treating
// omitted managed fields as their Go zero values.
func migrationDashboardList(raw json.RawMessage) ([]haclient.Dashboard, error) {
	if err := validateMigrationJSON(raw); err != nil {
		return nil, err
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil {
		return nil, fmt.Errorf("invalid dashboard list")
	}
	result := make([]haclient.Dashboard, 0, len(entries))
	for _, entry := range entries {
		for _, key := range []string{"id", "url_path", "mode", "title", "show_in_sidebar", "require_admin"} {
			v, ok := entry[key]
			if !ok || string(v) == "null" {
				return nil, fmt.Errorf("incomplete dashboard metadata")
			}
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		var dashboard haclient.Dashboard
		if err := json.Unmarshal(encoded, &dashboard); err != nil {
			return nil, err
		}
		result = append(result, dashboard)
	}
	return result, nil
}
