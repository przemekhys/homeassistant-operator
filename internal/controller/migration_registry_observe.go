package controller

import (
	"context"
	"encoding/json"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// observeMigrationRegistry adopts only by fresh observation, without ever
// allowing imported objects to reach the ordinary mutation paths.
func observeMigrationRegistry(
	ctx context.Context,
	kube client.Client,
	obj client.Object,
	kind, ref string,
	makeClient func(*hav1.HomeAssistant) *haclient.Client,
) (ctrl.Result, error) {
	reason := "IncompleteIdentity"
	digest := ""
	var referenceFloor string
	var referenceLabels []string
	paused := obj.GetAnnotations()[migrationPausedAnnotation] == "true"
	identity, identityErr := migrationObjectIdentity(obj)
	if paused {
		reason = "MigrationPaused"
	} else if identityErr == nil && ref != "" {
		reason = "TargetUnavailable"
		ha := &hav1.HomeAssistant{}
		err := kube.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: ref}, ha)
		if err == nil && string(ha.UID) != identity.TargetUID {
			reason = "TargetIdentityMismatch"
		} else if err == nil {
			reason = "TokenUnavailable"
			if token, tokenErr := getAPIToken(ctx, kube, ha); tokenErr == nil {
				reason = "RegistryUnavailable"
				haClient := makeClient(ha)
				raw, readErr := haClient.SendWebSocketCommand(ctx, token, "config/"+kind+"_registry/list", nil)
				if readErr == nil {
					if area, ok := obj.(*hav1.HomeAssistantArea); ok {
						floorID, labelIDs, resolveErr := resolveMigrationAreaReferences(ctx, kube, area, identity.TargetUID)
						if resolveErr != nil {
							reason = "RegistryReferencesUnverified"
						} else if !migrationAreaRegistryReferencesExist(ctx, haClient, token, floorID, labelIDs) {
							reason = "RegistryReferencesUnverified"
						} else {
							referenceFloor, referenceLabels = floorID, labelIDs
							reason = compareMigrationRegistryArea(area, identity.SourceID, raw, floorID, labelIDs)
						}
					} else {
						reason = compareMigrationRegistry(obj, kind, identity.SourceID, raw)
					}
					if reason == "RegistryConflict" {
						digest = migrationRegistryComparisonDigest(obj, identity, raw, referenceFloor, referenceLabels)
						if validMigrationOverwriteAnnotation(obj.GetAnnotations()[migrationOverwriteApprovalAnnotation], string(obj.GetUID()), obj.GetGeneration(), identity, digest) {
							reason = "UnsafeOverwriteUnsupported"
						}
					}
				}
			}
		}
	}
	changed, supported := updateMigrationRegistryStatus(obj, identity, reason, digest)
	if !supported {
		return ctrl.Result{}, nil
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

func updateMigrationRegistryStatus(obj client.Object, identity migrationIdentity, reason, digest string) (bool, bool) {
	var conditions *[]metav1.Condition
	changed := false
	switch v := obj.(type) {
	case *hav1.HomeAssistantFloor:
		conditions = &v.Status.Conditions
		if reason == "Adopted" && (v.Status.FloorID != identity.SourceID || v.Status.ObservedGeneration != obj.GetGeneration()) {
			v.Status.FloorID, v.Status.ObservedGeneration = identity.SourceID, obj.GetGeneration()
			changed = true
		}
	case *hav1.HomeAssistantArea:
		conditions = &v.Status.Conditions
		if reason == "Adopted" && (v.Status.AreaID != identity.SourceID || v.Status.ObservedGeneration != obj.GetGeneration()) {
			v.Status.AreaID, v.Status.ObservedGeneration = identity.SourceID, obj.GetGeneration()
			changed = true
		}
	case *hav1.HomeAssistantLabel:
		conditions = &v.Status.Conditions
		if reason == "Adopted" && (v.Status.LabelID != identity.SourceID || v.Status.ObservedGeneration != obj.GetGeneration()) {
			v.Status.LabelID, v.Status.ObservedGeneration = identity.SourceID, obj.GetGeneration()
			changed = true
		}
	default:
		return false, false
	}
	status, message := metav1.ConditionFalse, "Registry adoption remains blocked pending verification"
	if digest != "" {
		message += "; comparison=" + digest
	}
	if reason == "Adopted" {
		status, message = metav1.ConditionTrue, "Existing registry object verified without modifying Home Assistant"
	}
	if meta.SetStatusCondition(
		conditions,
		metav1.Condition{
			Type:               "MigrationAdoption",
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: obj.GetGeneration(),
		},
	) {
		changed = true
	}
	readyMessage := "Imported resource is not yet adopted"
	if reason == "Adopted" {
		readyMessage = "Read-only adoption verified; ordinary synchronization remains disabled"
	}
	if meta.SetStatusCondition(
		conditions,
		metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            readyMessage,
			ObservedGeneration: obj.GetGeneration(),
		},
	) {
		changed = true
	}
	return changed, true
}

func migrationRegistryComparisonDigest(
	obj client.Object,
	identity migrationIdentity,
	raw json.RawMessage,
	floorID string,
	labelIDs []string,
) string {
	var spec any
	switch v := obj.(type) {
	case *hav1.HomeAssistantFloor:
		spec = v.Spec
	case *hav1.HomeAssistantArea:
		spec = v.Spec
	case *hav1.HomeAssistantLabel:
		spec = v.Spec
	default:
		return ""
	}
	desired, err := json.Marshal(struct {
		Spec     any      `json:"spec"`
		FloorID  string   `json:"floorID"`
		LabelIDs []string `json:"labelIDs"`
	}{spec, floorID, labelIDs})
	if err != nil {
		return ""
	}
	observed, err := canonicalMigrationJSON(raw)
	if err != nil {
		return ""
	}
	return comparisonDigest(identity.SourceID, identity.TargetUID, desired, observed)
}

func compareMigrationRegistry(obj client.Object, kind, id string, raw json.RawMessage) string {
	return compareMigrationRegistryWithRefs(obj, kind, id, raw, "", nil, false)
}

func compareMigrationRegistryArea(
	area *hav1.HomeAssistantArea,
	id string,
	raw json.RawMessage,
	floorID string,
	labelIDs []string,
) string {
	return compareMigrationRegistryWithRefs(area, "area", id, raw, floorID, labelIDs, true)
}

func compareMigrationRegistryWithRefs(
	obj client.Object,
	kind, id string,
	raw json.RawMessage,
	expectedFloorID string,
	expectedLabelIDs []string,
	refsVerified bool,
) string {
	if err := validateMigrationJSON(raw); err != nil {
		return "RegistryInvalid"
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil {
		return "RegistryInvalid"
	}
	var match map[string]json.RawMessage
	for _, entry := range entries {
		var entryID string
		if json.Unmarshal(entry[kind+"_id"], &entryID) != nil {
			return "RegistryIncomplete"
		}
		if entryID == id {
			if match != nil {
				return "RegistryIdentityAmbiguous"
			}
			match = entry
		}
	}
	if match == nil {
		return "RegistryIdentityNotFound"
	}
	// These fields are compared only when explicitly present; absent data must
	// never be confused with the zero value by Go JSON decoding.
	var name string
	if json.Unmarshal(match["name"], &name) != nil || name == "" {
		return "RegistryIncomplete"
	}
	var desiredName string
	switch v := obj.(type) {
	case *hav1.HomeAssistantFloor:
		desiredName = v.Spec.Name
	case *hav1.HomeAssistantArea:
		desiredName = v.Spec.Name
	case *hav1.HomeAssistantLabel:
		desiredName = v.Spec.Name
	default:
		return "RegistryInvalid"
	}
	if name != desiredName {
		return "RegistryConflict"
	}
	// Until associations are represented by durable IDs, area references cannot
	// be compared safely by name. Do not infer identity from matching names.
	if area, ok := obj.(*hav1.HomeAssistantArea); ok && !refsVerified &&
		(area.Spec.FloorName != "" || len(area.Spec.Labels) != 0) {
		return "RegistryReferencesUnverified"
	}
	var actualIcon *string
	if value, ok := match["icon"]; !ok || json.Unmarshal(value, &actualIcon) != nil {
		return "RegistryIncomplete"
	}
	icon := ""
	if actualIcon != nil {
		icon = *actualIcon
	}
	switch v := obj.(type) {
	case *hav1.HomeAssistantFloor:
		return compareMigrationFloorFields(v, match, icon)
	case *hav1.HomeAssistantArea:
		return compareMigrationAreaFields(v, match, icon, expectedFloorID, expectedLabelIDs)
	case *hav1.HomeAssistantLabel:
		return compareMigrationLabelFields(v, match, icon)
	}
	return "RegistryInvalid"
}

func compareMigrationFloorFields(floor *hav1.HomeAssistantFloor, fields map[string]json.RawMessage, icon string) string {
	var level *int
	if value, ok := fields["level"]; !ok || json.Unmarshal(value, &level) != nil {
		return "RegistryIncomplete"
	}
	if icon != floor.Spec.Icon || (level == nil) != (floor.Spec.Level == nil) ||
		(level != nil && *level != *floor.Spec.Level) {
		return "RegistryConflict"
	}
	return "Adopted"
}

func compareMigrationAreaFields(area *hav1.HomeAssistantArea, fields map[string]json.RawMessage,
	icon, expectedFloor string, expectedLabels []string,
) string {
	var floor *string
	var labels []string
	floorRaw, floorPresent := fields["floor_id"]
	labelsRaw, labelsPresent := fields["labels"]
	if !floorPresent || !labelsPresent || json.Unmarshal(floorRaw, &floor) != nil ||
		json.Unmarshal(labelsRaw, &labels) != nil {
		return "RegistryIncomplete"
	}
	actualFloor := ""
	if floor != nil {
		actualFloor = *floor
	}
	if icon != area.Spec.Icon || actualFloor != expectedFloor || !stringSlicesEqual(labels, expectedLabels) {
		return "RegistryConflict"
	}
	return "Adopted"
}

func compareMigrationLabelFields(label *hav1.HomeAssistantLabel, fields map[string]json.RawMessage, icon string) string {
	var color *string
	if value, ok := fields["color"]; !ok || json.Unmarshal(value, &color) != nil {
		return "RegistryIncomplete"
	}
	actualColor := ""
	if color != nil {
		actualColor = *color
	}
	if icon != label.Spec.Icon || actualColor != label.Spec.Color {
		return "RegistryConflict"
	}
	return "Adopted"
}
