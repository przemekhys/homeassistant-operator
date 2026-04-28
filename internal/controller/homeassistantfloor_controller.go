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
	floorFinalizerName = "ha.homeassistant.io/floor-finalizer"

	reasonFloorReady         = "FloorReady"
	reasonFloorHANotReady    = "HANotReady"
	reasonFloorTokenNotAvail = "TokenNotAvailable"
	reasonFloorFailed        = "FloorFailed"
)

// HomeAssistantFloorReconciler reconciles a HomeAssistantFloor object
type HomeAssistantFloorReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	NewHAClient func(baseURL string) *haclient.Client
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantfloors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantfloors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantfloors/finalizers,verbs=update
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

func (r *HomeAssistantFloorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	floor := &hav1alpha1.HomeAssistantFloor{}
	if err := r.Get(ctx, req.NamespacedName, floor); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// --- DELETION ---
	if !floor.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(floor, floorFinalizerName) {
			r.handleDeletion(ctx, floor)
			controllerutil.RemoveFinalizer(floor, floorFinalizerName)
			if err := r.Update(ctx, floor); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// --- FINALIZER ---
	if !controllerutil.ContainsFinalizer(floor, floorFinalizerName) {
		controllerutil.AddFinalizer(floor, floorFinalizerName)
		if err := r.Update(ctx, floor); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// --- FETCH HomeAssistant CR ---
	haRef := types.NamespacedName{
		Name:      floor.Spec.HomeAssistantRef.Name,
		Namespace: floor.Namespace,
	}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		log.Info("HomeAssistant not found, requeueing", "name", haRef.Name)
		return r.setCondition(ctx, floor, metav1.ConditionFalse, reasonFloorHANotReady,
			fmt.Sprintf("HomeAssistant %s not found", haRef.Name), 30*time.Second)
	}

	// --- API TOKEN ---
	token, tokenErr := getAPIToken(ctx, r.Client, ha)
	if tokenErr != nil {
		log.Info("API token not available, requeueing")
		return r.setCondition(ctx, floor, metav1.ConditionFalse, reasonFloorTokenNotAvail,
			"API token not available", 30*time.Second)
	}

	haClient := r.haClientFor(ha)

	// --- LIST existing floors ---
	existingFloors, err := r.listFloors(ctx, haClient, token)
	if err != nil {
		log.Error(err, "Failed to list floors")
		return r.setCondition(ctx, floor, metav1.ConditionFalse, reasonFloorFailed,
			fmt.Sprintf("Failed to list floors: %v", err), 30*time.Second)
	}

	// --- FIND existing (by ID when known, by name for adoption) ---
	var existingFloor *haFloorEntry
	if floor.Status.FloorID != "" {
		existingFloor = findFloorByID(existingFloors, floor.Status.FloorID)
		if existingFloor == nil {
			// Floor was deleted externally — clear stale ID and re-create
			log.Info("Floor was deleted externally, re-creating", "name", floor.Spec.Name)
			floor.Status.FloorID = ""
		} else {
			// Check if update needed
			if r.needsFloorUpdate(floor, existingFloor) {
				if err := r.updateFloor(ctx, haClient, token, floor); err != nil {
					log.Error(err, "Failed to update floor")
					return r.setCondition(ctx, floor, metav1.ConditionFalse, reasonFloorFailed,
						fmt.Sprintf("Failed to update floor: %v", err), 30*time.Second)
				}
				log.Info("Floor updated in HA", "name", floor.Spec.Name)
			}
			return r.setCondition(ctx, floor, metav1.ConditionTrue, reasonFloorReady,
				"Floor configured in Home Assistant", 0)
		}
	}

	// --- ADOPT by name (only when no ID set) ---
	if floor.Status.FloorID == "" {
		existingFloor = findFloorByName(existingFloors, floor.Spec.Name)
	}
	if existingFloor != nil {
		floor.Status.FloorID = existingFloor.FloorID
		log.Info("Adopted existing floor", "name", floor.Spec.Name, "floorID", floor.Status.FloorID)
		if r.needsFloorUpdate(floor, existingFloor) {
			if err := r.updateFloor(ctx, haClient, token, floor); err != nil {
				log.Error(err, "Failed to update adopted floor")
				return r.setCondition(ctx, floor, metav1.ConditionFalse, reasonFloorFailed,
					fmt.Sprintf("Failed to update adopted floor: %v", err), 30*time.Second)
			}
		}
		return r.setCondition(ctx, floor, metav1.ConditionTrue, reasonFloorReady,
			"Floor configured in Home Assistant", 0)
	}

	// --- CREATE ---
	floorID, err := r.createFloor(ctx, haClient, token, floor)
	if err != nil {
		log.Error(err, "Failed to create floor")
		return r.setCondition(ctx, floor, metav1.ConditionFalse, reasonFloorFailed,
			fmt.Sprintf("Failed to create floor: %v", err), 30*time.Second)
	}

	floor.Status.FloorID = floorID
	log.Info("Floor created in HA", "name", floor.Spec.Name, "floorID", floorID)

	return r.setCondition(ctx, floor, metav1.ConditionTrue, reasonFloorReady,
		"Floor configured in Home Assistant", 0)
}

// handleDeletion removes the floor from HA (best-effort)
func (r *HomeAssistantFloorReconciler) handleDeletion(ctx context.Context, floor *hav1alpha1.HomeAssistantFloor) {
	log := logf.FromContext(ctx)

	if floor.Status.FloorID == "" {
		return
	}

	haRef := types.NamespacedName{
		Name:      floor.Spec.HomeAssistantRef.Name,
		Namespace: floor.Namespace,
	}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		log.Info("HomeAssistant not found during floor deletion, skipping cleanup")
		return
	}

	token, err := getAPIToken(ctx, r.Client, ha)
	if err != nil {
		log.Info("API token not available during floor deletion, skipping cleanup")
		return
	}

	haClient := r.haClientFor(ha)
	if err := r.deleteFloor(ctx, haClient, token, floor.Status.FloorID); err != nil {
		log.Error(err, "Failed to delete floor from HA (best-effort)", "floorID", floor.Status.FloorID)
	} else {
		log.Info("Floor deleted from HA", "floorID", floor.Status.FloorID)
	}
}

// haClientFor returns a haclient.Client for the given HA instance
func (r *HomeAssistantFloorReconciler) haClientFor(ha *hav1alpha1.HomeAssistant) *haclient.Client {
	baseURL := buildHomeAssistantURL(ha)
	if r.NewHAClient != nil {
		return r.NewHAClient(baseURL)
	}
	return haclient.NewClient(baseURL)
}

// setCondition updates the Ready condition and status
func (r *HomeAssistantFloorReconciler) setCondition(
	ctx context.Context,
	floor *hav1alpha1.HomeAssistantFloor,
	status metav1.ConditionStatus,
	reason, message string,
	requeueAfter time.Duration,
) (ctrl.Result, error) {
	meta.SetStatusCondition(&floor.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: floor.Generation,
	})
	floor.Status.ObservedGeneration = floor.Generation
	if status == metav1.ConditionFalse {
		floor.Status.LastError = message
	} else {
		floor.Status.LastError = ""
	}

	if err := r.Status().Update(ctx, floor); err != nil {
		return ctrl.Result{}, err
	}
	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// --- WebSocket operations ---

type haFloorEntry struct {
	FloorID string `json:"floor_id"`
	Name    string `json:"name"`
	Level   *int   `json:"level"`
	Icon    string `json:"icon"`
}

func (r *HomeAssistantFloorReconciler) listFloors(
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

func (r *HomeAssistantFloorReconciler) createFloor(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
	floor *hav1alpha1.HomeAssistantFloor,
) (string, error) {
	data := map[string]interface{}{
		"name": floor.Spec.Name,
	}
	if floor.Spec.Level != nil {
		data["level"] = *floor.Spec.Level
	}
	if floor.Spec.Icon != "" {
		data["icon"] = floor.Spec.Icon
	}

	result, err := haClient.SendWebSocketCommand(ctx, token, "config/floor_registry/create", data)
	if err != nil {
		return "", err
	}

	var created haFloorEntry
	if err := json.Unmarshal(result, &created); err != nil {
		return "", fmt.Errorf("failed to unmarshal created floor: %w", err)
	}
	return created.FloorID, nil
}

func (r *HomeAssistantFloorReconciler) updateFloor(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
	floor *hav1alpha1.HomeAssistantFloor,
) error {
	data := map[string]interface{}{
		"floor_id": floor.Status.FloorID,
		"name":     floor.Spec.Name,
		"icon":     floor.Spec.Icon,
	}
	if floor.Spec.Level != nil {
		data["level"] = *floor.Spec.Level
	} else {
		data["level"] = nil
	}

	_, err := haClient.SendWebSocketCommand(ctx, token, "config/floor_registry/update", data)
	return err
}

func (r *HomeAssistantFloorReconciler) deleteFloor(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
	floorID string,
) error {
	_, err := haClient.SendWebSocketCommand(ctx, token, "config/floor_registry/delete", map[string]interface{}{
		"floor_id": floorID,
	})
	return err
}

func findFloorByName(floors []haFloorEntry, name string) *haFloorEntry {
	for i := range floors {
		if floors[i].Name == name {
			return &floors[i]
		}
	}
	return nil
}

func findFloorByID(floors []haFloorEntry, id string) *haFloorEntry {
	for i := range floors {
		if floors[i].FloorID == id {
			return &floors[i]
		}
	}
	return nil
}

func (r *HomeAssistantFloorReconciler) needsFloorUpdate(
	floor *hav1alpha1.HomeAssistantFloor,
	existing *haFloorEntry,
) bool {
	if existing.Name != floor.Spec.Name {
		return true
	}
	if floor.Spec.Icon != existing.Icon {
		return true
	}
	if floor.Spec.Level != nil && existing.Level != nil {
		if *floor.Spec.Level != *existing.Level {
			return true
		}
	} else if floor.Spec.Level != existing.Level {
		// One is nil and the other is not
		return true
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *HomeAssistantFloorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1alpha1.HomeAssistantFloor{}).
		Named("homeassistantfloor").
		Complete(r)
}
