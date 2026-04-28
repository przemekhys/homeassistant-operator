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
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const (
	areaFinalizerName = "ha.homeassistant.io/area-finalizer"

	reasonAreaReady         = "AreaReady"
	reasonAreaHANotReady    = "HANotReady"
	reasonAreaTokenNotAvail = "TokenNotAvailable"
	reasonAreaFailed        = "AreaFailed"
	reasonAreaFloorNotFound = "FloorNotFound"
)

// HomeAssistantAreaReconciler reconciles a HomeAssistantArea object
type HomeAssistantAreaReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	NewHAClient func(baseURL string) *haclient.Client
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantareas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantareas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantareas/finalizers,verbs=update
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

func (r *HomeAssistantAreaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	area := &hav1alpha1.HomeAssistantArea{}
	if err := r.Get(ctx, req.NamespacedName, area); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// --- DELETION ---
	if !area.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(area, areaFinalizerName) {
			r.handleDeletion(ctx, area)
			controllerutil.RemoveFinalizer(area, areaFinalizerName)
			if err := r.Update(ctx, area); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// --- FINALIZER ---
	if !controllerutil.ContainsFinalizer(area, areaFinalizerName) {
		controllerutil.AddFinalizer(area, areaFinalizerName)
		if err := r.Update(ctx, area); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// --- FETCH HomeAssistant CR ---
	haRef := types.NamespacedName{
		Name:      area.Spec.HomeAssistantRef.Name,
		Namespace: area.Namespace,
	}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		log.Info("HomeAssistant not found, requeueing", "name", haRef.Name)
		return r.setCondition(ctx, area, metav1.ConditionFalse, reasonAreaHANotReady,
			fmt.Sprintf("HomeAssistant %s not found", haRef.Name), 30*time.Second)
	}

	// --- API TOKEN ---
	token, tokenErr := getAPIToken(ctx, r.Client, ha)
	if tokenErr != nil {
		log.Info("API token not available, requeueing")
		return r.setCondition(ctx, area, metav1.ConditionFalse, reasonAreaTokenNotAvail,
			"API token not available", 30*time.Second)
	}

	haClient := r.haClientFor(ha)

	// --- RESOLVE floor reference ---
	var floorID string
	if area.Spec.FloorName != "" {
		floors, err := r.listFloors(ctx, haClient, token)
		if err != nil {
			log.Error(err, "Failed to list floors for area resolution")
			return r.setCondition(ctx, area, metav1.ConditionFalse, reasonAreaFailed,
				fmt.Sprintf("Failed to list floors: %v", err), 30*time.Second)
		}
		floor := findFloorByName(floors, area.Spec.FloorName)
		if floor == nil {
			log.Info("Floor not found, requeueing", "floorName", area.Spec.FloorName)
			return r.setCondition(ctx, area, metav1.ConditionFalse, reasonAreaFloorNotFound,
				fmt.Sprintf("Floor %q not found in Home Assistant", area.Spec.FloorName), 30*time.Second)
		}
		floorID = floor.FloorID
	}

	// --- RESOLVE label references ---
	var labelIDs []string
	if len(area.Spec.Labels) > 0 {
		labels, err := r.listLabels(ctx, haClient, token)
		if err != nil {
			log.Error(err, "Failed to list labels for area resolution")
			return r.setCondition(ctx, area, metav1.ConditionFalse, reasonAreaFailed,
				fmt.Sprintf("Failed to list labels: %v", err), 30*time.Second)
		}
		for _, labelName := range area.Spec.Labels {
			label := findLabelByName(labels, labelName)
			if label != nil {
				labelIDs = append(labelIDs, label.LabelID)
			} else {
				log.Info("Label not found, skipping", "labelName", labelName)
			}
		}
	}

	// --- LIST existing areas ---
	existingAreas, err := r.listAreas(ctx, haClient, token)
	if err != nil {
		log.Error(err, "Failed to list areas")
		return r.setCondition(ctx, area, metav1.ConditionFalse, reasonAreaFailed,
			fmt.Sprintf("Failed to list areas: %v", err), 30*time.Second)
	}

	// --- FIND existing (by ID when known, by name for adoption) ---
	var existingArea *haAreaEntry
	if area.Status.AreaID != "" {
		existingArea = findAreaByID(existingAreas, area.Status.AreaID)
		if existingArea == nil {
			// Area was deleted externally — clear stale ID and re-create
			log.Info("Area was deleted externally, re-creating", "name", area.Spec.Name)
			area.Status.AreaID = ""
		} else {
			if r.needsAreaUpdate(area, existingArea, floorID, labelIDs) {
				if err := r.updateArea(ctx, haClient, token, area, floorID, labelIDs); err != nil {
					log.Error(err, "Failed to update area")
					return r.setCondition(ctx, area, metav1.ConditionFalse, reasonAreaFailed,
						fmt.Sprintf("Failed to update area: %v", err), 30*time.Second)
				}
				log.Info("Area updated in HA", "name", area.Spec.Name)
			}
			return r.setCondition(ctx, area, metav1.ConditionTrue, reasonAreaReady,
				"Area configured in Home Assistant", 0)
		}
	}

	// --- ADOPT by name (only when no ID set) ---
	if area.Status.AreaID == "" {
		existingArea = findAreaByName(existingAreas, area.Spec.Name)
	}
	if existingArea != nil {
		area.Status.AreaID = existingArea.AreaID
		log.Info("Adopted existing area", "name", area.Spec.Name, "areaID", area.Status.AreaID)
		if r.needsAreaUpdate(area, existingArea, floorID, labelIDs) {
			if err := r.updateArea(ctx, haClient, token, area, floorID, labelIDs); err != nil {
				log.Error(err, "Failed to update adopted area")
				return r.setCondition(ctx, area, metav1.ConditionFalse, reasonAreaFailed,
					fmt.Sprintf("Failed to update adopted area: %v", err), 30*time.Second)
			}
		}
		return r.setCondition(ctx, area, metav1.ConditionTrue, reasonAreaReady,
			"Area configured in Home Assistant", 0)
	}

	// --- CREATE ---
	areaID, err := r.createArea(ctx, haClient, token, area, floorID, labelIDs)
	if err != nil {
		log.Error(err, "Failed to create area")
		return r.setCondition(ctx, area, metav1.ConditionFalse, reasonAreaFailed,
			fmt.Sprintf("Failed to create area: %v", err), 30*time.Second)
	}

	area.Status.AreaID = areaID
	log.Info("Area created in HA", "name", area.Spec.Name, "areaID", areaID)

	return r.setCondition(ctx, area, metav1.ConditionTrue, reasonAreaReady,
		"Area configured in Home Assistant", 0)
}

// handleDeletion removes the area from HA (best-effort)
func (r *HomeAssistantAreaReconciler) handleDeletion(ctx context.Context, area *hav1alpha1.HomeAssistantArea) {
	log := logf.FromContext(ctx)

	if area.Status.AreaID == "" {
		return
	}

	haRef := types.NamespacedName{
		Name:      area.Spec.HomeAssistantRef.Name,
		Namespace: area.Namespace,
	}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		log.Info("HomeAssistant not found during area deletion, skipping cleanup")
		return
	}

	token, err := getAPIToken(ctx, r.Client, ha)
	if err != nil {
		log.Info("API token not available during area deletion, skipping cleanup")
		return
	}

	haClient := r.haClientFor(ha)
	if err := r.deleteArea(ctx, haClient, token, area.Status.AreaID); err != nil {
		log.Error(err, "Failed to delete area from HA (best-effort)", "areaID", area.Status.AreaID)
	} else {
		log.Info("Area deleted from HA", "areaID", area.Status.AreaID)
	}
}

// haClientFor returns a haclient.Client for the given HA instance
func (r *HomeAssistantAreaReconciler) haClientFor(ha *hav1alpha1.HomeAssistant) *haclient.Client {
	baseURL := buildHomeAssistantURL(ha)
	if r.NewHAClient != nil {
		return r.NewHAClient(baseURL)
	}
	return haclient.NewClient(baseURL)
}

// setCondition updates the Ready condition and status
func (r *HomeAssistantAreaReconciler) setCondition(
	ctx context.Context,
	area *hav1alpha1.HomeAssistantArea,
	status metav1.ConditionStatus,
	reason, message string,
	requeueAfter time.Duration,
) (ctrl.Result, error) {
	meta.SetStatusCondition(&area.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: area.Generation,
	})
	area.Status.ObservedGeneration = area.Generation
	if status == metav1.ConditionFalse {
		area.Status.LastError = message
	} else {
		area.Status.LastError = ""
	}

	if err := r.Status().Update(ctx, area); err != nil {
		return ctrl.Result{}, err
	}
	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// --- WebSocket operations ---

type haAreaEntry struct {
	AreaID   string   `json:"area_id"`
	Name     string   `json:"name"`
	Icon     string   `json:"icon"`
	FloorID  string   `json:"floor_id"`
	LabelIDs []string `json:"labels"`
}

func (r *HomeAssistantAreaReconciler) listAreas(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
) ([]haAreaEntry, error) {
	result, err := haClient.SendWebSocketCommand(ctx, token, "config/area_registry/list", nil)
	if err != nil {
		return nil, err
	}
	var areas []haAreaEntry
	if err := json.Unmarshal(result, &areas); err != nil {
		return nil, fmt.Errorf("failed to unmarshal area list: %w", err)
	}
	return areas, nil
}

func (r *HomeAssistantAreaReconciler) listFloors(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
) ([]haFloorEntry, error) {
	result, err := haClient.SendWebSocketCommand(ctx, token, "config/floor_registry/list", nil)
	if err != nil {
		return nil, err
	}
	var floors []haFloorEntry
	if err := json.Unmarshal(result, &floors); err != nil {
		return nil, fmt.Errorf("failed to unmarshal floor list: %w", err)
	}
	return floors, nil
}

func (r *HomeAssistantAreaReconciler) listLabels(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
) ([]haLabelEntry, error) {
	result, err := haClient.SendWebSocketCommand(ctx, token, "config/label_registry/list", nil)
	if err != nil {
		return nil, err
	}
	var labels []haLabelEntry
	if err := json.Unmarshal(result, &labels); err != nil {
		return nil, fmt.Errorf("failed to unmarshal label list: %w", err)
	}
	return labels, nil
}

func (r *HomeAssistantAreaReconciler) createArea(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
	area *hav1alpha1.HomeAssistantArea,
	floorID string,
	labelIDs []string,
) (string, error) {
	data := map[string]interface{}{
		"name": area.Spec.Name,
	}
	if area.Spec.Icon != "" {
		data["icon"] = area.Spec.Icon
	}
	if floorID != "" {
		data["floor_id"] = floorID
	}
	if len(labelIDs) > 0 {
		data["labels"] = labelIDs
	}

	result, err := haClient.SendWebSocketCommand(ctx, token, "config/area_registry/create", data)
	if err != nil {
		return "", err
	}

	var created haAreaEntry
	if err := json.Unmarshal(result, &created); err != nil {
		return "", fmt.Errorf("failed to unmarshal created area: %w", err)
	}
	return created.AreaID, nil
}

func (r *HomeAssistantAreaReconciler) updateArea(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
	area *hav1alpha1.HomeAssistantArea,
	floorID string,
	labelIDs []string,
) error {
	data := map[string]interface{}{
		"area_id":  area.Status.AreaID,
		"name":     area.Spec.Name,
		"icon":     area.Spec.Icon,
		"floor_id": floorID,
	}
	if labelIDs == nil {
		data["labels"] = []string{}
	} else {
		data["labels"] = labelIDs
	}

	_, err := haClient.SendWebSocketCommand(ctx, token, "config/area_registry/update", data)
	return err
}

func (r *HomeAssistantAreaReconciler) deleteArea(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
	areaID string,
) error {
	_, err := haClient.SendWebSocketCommand(ctx, token, "config/area_registry/delete", map[string]interface{}{
		"area_id": areaID,
	})
	return err
}

func findAreaByName(areas []haAreaEntry, name string) *haAreaEntry {
	for i := range areas {
		if areas[i].Name == name {
			return &areas[i]
		}
	}
	return nil
}

func findAreaByID(areas []haAreaEntry, id string) *haAreaEntry {
	for i := range areas {
		if areas[i].AreaID == id {
			return &areas[i]
		}
	}
	return nil
}

func (r *HomeAssistantAreaReconciler) needsAreaUpdate(
	area *hav1alpha1.HomeAssistantArea,
	existing *haAreaEntry,
	floorID string,
	labelIDs []string,
) bool {
	if existing.Name != area.Spec.Name {
		return true
	}
	if area.Spec.Icon != existing.Icon {
		return true
	}
	if floorID != existing.FloorID {
		return true
	}
	if !stringSlicesEqual(labelIDs, existing.LabelIDs) {
		return true
	}
	return false
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
		if seen[v] < 0 {
			return false
		}
	}
	return true
}

// SetupWithManager sets up the controller with the Manager.
func (r *HomeAssistantAreaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1alpha1.HomeAssistantArea{}).
		Named("homeassistantarea").
		Complete(r)
}
