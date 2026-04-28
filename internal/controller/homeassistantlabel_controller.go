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
	labelFinalizerName = "ha.homeassistant.io/label-finalizer"

	reasonLabelReady         = "LabelReady"
	reasonLabelHANotReady    = "HANotReady"
	reasonLabelTokenNotAvail = "TokenNotAvailable"
	reasonLabelFailed        = "LabelFailed"
)

// HomeAssistantLabelReconciler reconciles a HomeAssistantLabel object
type HomeAssistantLabelReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	NewHAClient func(baseURL string) *haclient.Client
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantlabels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantlabels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantlabels/finalizers,verbs=update
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

func (r *HomeAssistantLabelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	label := &hav1alpha1.HomeAssistantLabel{}
	if err := r.Get(ctx, req.NamespacedName, label); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// --- DELETION ---
	if !label.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(label, labelFinalizerName) {
			r.handleDeletion(ctx, label)
			controllerutil.RemoveFinalizer(label, labelFinalizerName)
			if err := r.Update(ctx, label); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// --- FINALIZER ---
	if !controllerutil.ContainsFinalizer(label, labelFinalizerName) {
		controllerutil.AddFinalizer(label, labelFinalizerName)
		if err := r.Update(ctx, label); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// --- FETCH HomeAssistant CR ---
	haRef := types.NamespacedName{
		Name:      label.Spec.HomeAssistantRef.Name,
		Namespace: label.Namespace,
	}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		log.Info("HomeAssistant not found, requeueing", "name", haRef.Name)
		return r.setCondition(ctx, label, metav1.ConditionFalse, reasonLabelHANotReady,
			fmt.Sprintf("HomeAssistant %s not found", haRef.Name), 30*time.Second)
	}

	// --- API TOKEN ---
	token, tokenErr := getAPIToken(ctx, r.Client, ha)
	if tokenErr != nil {
		log.Info("API token not available, requeueing")
		return r.setCondition(ctx, label, metav1.ConditionFalse, reasonLabelTokenNotAvail,
			"API token not available", 30*time.Second)
	}

	haClient := r.haClientFor(ha)

	// --- LIST existing labels ---
	existingLabels, err := r.listLabels(ctx, haClient, token)
	if err != nil {
		log.Error(err, "Failed to list labels")
		return r.setCondition(ctx, label, metav1.ConditionFalse, reasonLabelFailed,
			fmt.Sprintf("Failed to list labels: %v", err), 30*time.Second)
	}

	// --- FIND existing (by ID when known, by name for adoption) ---
	var existingLabel *haLabelEntry
	if label.Status.LabelID != "" {
		existingLabel = findLabelByID(existingLabels, label.Status.LabelID)
		if existingLabel == nil {
			// Label was deleted externally — clear stale ID and re-create
			log.Info("Label was deleted externally, re-creating", "name", label.Spec.Name)
			label.Status.LabelID = ""
		} else {
			if r.needsLabelUpdate(label, existingLabel) {
				if err := r.updateLabel(ctx, haClient, token, label); err != nil {
					log.Error(err, "Failed to update label")
					return r.setCondition(ctx, label, metav1.ConditionFalse, reasonLabelFailed,
						fmt.Sprintf("Failed to update label: %v", err), 30*time.Second)
				}
				log.Info("Label updated in HA", "name", label.Spec.Name)
			}
			return r.setCondition(ctx, label, metav1.ConditionTrue, reasonLabelReady,
				"Label configured in Home Assistant", 0)
		}
	}

	// --- ADOPT by name (only when no ID set) ---
	if label.Status.LabelID == "" {
		existingLabel = findLabelByName(existingLabels, label.Spec.Name)
	}
	if existingLabel != nil {
		label.Status.LabelID = existingLabel.LabelID
		log.Info("Adopted existing label", "name", label.Spec.Name, "labelID", label.Status.LabelID)
		if r.needsLabelUpdate(label, existingLabel) {
			if err := r.updateLabel(ctx, haClient, token, label); err != nil {
				log.Error(err, "Failed to update adopted label")
				return r.setCondition(ctx, label, metav1.ConditionFalse, reasonLabelFailed,
					fmt.Sprintf("Failed to update adopted label: %v", err), 30*time.Second)
			}
		}
		return r.setCondition(ctx, label, metav1.ConditionTrue, reasonLabelReady,
			"Label configured in Home Assistant", 0)
	}

	// --- CREATE ---
	labelID, err := r.createLabel(ctx, haClient, token, label)
	if err != nil {
		log.Error(err, "Failed to create label")
		return r.setCondition(ctx, label, metav1.ConditionFalse, reasonLabelFailed,
			fmt.Sprintf("Failed to create label: %v", err), 30*time.Second)
	}

	label.Status.LabelID = labelID
	log.Info("Label created in HA", "name", label.Spec.Name, "labelID", labelID)

	return r.setCondition(ctx, label, metav1.ConditionTrue, reasonLabelReady,
		"Label configured in Home Assistant", 0)
}

// handleDeletion removes the label from HA (best-effort)
func (r *HomeAssistantLabelReconciler) handleDeletion(ctx context.Context, label *hav1alpha1.HomeAssistantLabel) {
	log := logf.FromContext(ctx)

	if label.Status.LabelID == "" {
		return
	}

	haRef := types.NamespacedName{
		Name:      label.Spec.HomeAssistantRef.Name,
		Namespace: label.Namespace,
	}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		log.Info("HomeAssistant not found during label deletion, skipping cleanup")
		return
	}

	token, err := getAPIToken(ctx, r.Client, ha)
	if err != nil {
		log.Info("API token not available during label deletion, skipping cleanup")
		return
	}

	haClient := r.haClientFor(ha)
	if err := r.deleteLabel(ctx, haClient, token, label.Status.LabelID); err != nil {
		log.Error(err, "Failed to delete label from HA (best-effort)", "labelID", label.Status.LabelID)
	} else {
		log.Info("Label deleted from HA", "labelID", label.Status.LabelID)
	}
}

// haClientFor returns a haclient.Client for the given HA instance
func (r *HomeAssistantLabelReconciler) haClientFor(ha *hav1alpha1.HomeAssistant) *haclient.Client {
	baseURL := buildHomeAssistantURL(ha)
	if r.NewHAClient != nil {
		return r.NewHAClient(baseURL)
	}
	return haclient.NewClient(baseURL)
}

// setCondition updates the Ready condition and status
func (r *HomeAssistantLabelReconciler) setCondition(
	ctx context.Context,
	label *hav1alpha1.HomeAssistantLabel,
	status metav1.ConditionStatus,
	reason, message string,
	requeueAfter time.Duration,
) (ctrl.Result, error) {
	meta.SetStatusCondition(&label.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: label.Generation,
	})
	label.Status.ObservedGeneration = label.Generation
	if status == metav1.ConditionFalse {
		label.Status.LastError = message
	} else {
		label.Status.LastError = ""
	}

	if err := r.Status().Update(ctx, label); err != nil {
		return ctrl.Result{}, err
	}
	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// --- WebSocket operations ---

type haLabelEntry struct {
	LabelID string `json:"label_id"`
	Name    string `json:"name"`
	Icon    string `json:"icon"`
	Color   string `json:"color"`
}

func (r *HomeAssistantLabelReconciler) listLabels(
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

func (r *HomeAssistantLabelReconciler) createLabel(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
	label *hav1alpha1.HomeAssistantLabel,
) (string, error) {
	data := map[string]interface{}{
		"name": label.Spec.Name,
	}
	if label.Spec.Icon != "" {
		data["icon"] = label.Spec.Icon
	}
	if label.Spec.Color != "" {
		data["color"] = label.Spec.Color
	}

	result, err := haClient.SendWebSocketCommand(ctx, token, "config/label_registry/create", data)
	if err != nil {
		return "", err
	}

	var created haLabelEntry
	if err := json.Unmarshal(result, &created); err != nil {
		return "", fmt.Errorf("failed to unmarshal created label: %w", err)
	}
	return created.LabelID, nil
}

func (r *HomeAssistantLabelReconciler) updateLabel(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
	label *hav1alpha1.HomeAssistantLabel,
) error {
	data := map[string]interface{}{
		"label_id": label.Status.LabelID,
		"name":     label.Spec.Name,
		"icon":     label.Spec.Icon,
		"color":    label.Spec.Color,
	}

	_, err := haClient.SendWebSocketCommand(ctx, token, "config/label_registry/update", data)
	return err
}

func (r *HomeAssistantLabelReconciler) deleteLabel(
	ctx context.Context,
	haClient *haclient.Client,
	token string,
	labelID string,
) error {
	_, err := haClient.SendWebSocketCommand(ctx, token, "config/label_registry/delete", map[string]interface{}{
		"label_id": labelID,
	})
	return err
}

func findLabelByName(labels []haLabelEntry, name string) *haLabelEntry {
	for i := range labels {
		if labels[i].Name == name {
			return &labels[i]
		}
	}
	return nil
}

func findLabelByID(labels []haLabelEntry, id string) *haLabelEntry {
	for i := range labels {
		if labels[i].LabelID == id {
			return &labels[i]
		}
	}
	return nil
}

func (r *HomeAssistantLabelReconciler) needsLabelUpdate(
	label *hav1alpha1.HomeAssistantLabel,
	existing *haLabelEntry,
) bool {
	if existing.Name != label.Spec.Name {
		return true
	}
	if label.Spec.Icon != existing.Icon {
		return true
	}
	if label.Spec.Color != existing.Color {
		return true
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *HomeAssistantLabelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1alpha1.HomeAssistantLabel{}).
		Named("homeassistantlabel").
		Complete(r)
}
