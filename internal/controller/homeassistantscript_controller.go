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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const (
	scriptsYamlKey         = "scripts.yaml"
	generatedScriptsSuffix = "-scripts"
	scriptFinalizerName    = "ha.homeassistant.io/script-finalizer"

	// Condition reasons for HomeAssistantScript
	reasonScriptGenerated = "ScriptGenerated"
	reasonInvalidScript   = "InvalidScript"
)

// HomeAssistantScriptReconciler reconciles a HomeAssistantScript object
type HomeAssistantScriptReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Recorder    events.EventRecorder
	NewHAClient func(baseURL string) *haclient.Client // overridable for testing
}

// haClientFor returns a HA API client for the given HomeAssistant instance.
func (r *HomeAssistantScriptReconciler) haClientFor(ha *hav1alpha1.HomeAssistant) *haclient.Client {
	haURL := buildHomeAssistantURL(ha)
	if r.NewHAClient != nil {
		return r.NewHAClient(haURL)
	}
	return haclient.NewClient(haURL)
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantscripts,verbs=get;list;watch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantscripts,verbs=create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantscripts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantscripts/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *HomeAssistantScriptReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the HomeAssistantScript instance
	script := &hav1alpha1.HomeAssistantScript{}
	if err := r.Get(ctx, req.NamespacedName, script); err != nil {
		if errors.IsNotFound(err) {
			log.Info("HomeAssistantScript resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get HomeAssistantScript")
		return ctrl.Result{}, err
	}

	// Handle finalizer for proper cleanup
	if !script.DeletionTimestamp.IsZero() {
		// Resource is being deleted
		if controllerutil.ContainsFinalizer(script, scriptFinalizerName) {
			log.Info("Handling deletion - deleting script via HA REST API")

			// Delete script via HA REST API (best effort — proceed even if HA is unavailable)
			haRef := types.NamespacedName{
				Name:      script.Spec.HomeAssistantRef.Name,
				Namespace: script.Namespace,
			}
			if ha, haErr := r.validateHomeAssistantRef(ctx, haRef, script); haErr == nil {
				if token, tokenErr := getAPIToken(ctx, r.Client, ha); tokenErr == nil {
					haClient := r.haClientFor(ha)
					id := script.Spec.ID
					if id == "" {
						id = script.Name
					}
					if delErr := haClient.DeleteScript(ctx, token, id); delErr != nil {
						log.Info("Failed to delete script via HA API (proceeding with finalizer removal)", "error", delErr)
					}
				} else {
					log.Info("API token not available during deletion, proceeding with finalizer removal")
				}
			}

			// Remove finalizer to allow deletion
			controllerutil.RemoveFinalizer(script, scriptFinalizerName)
			if err := r.Update(ctx, script); err != nil {
				log.Error(err, "Failed to remove finalizer")
				return ctrl.Result{}, err
			}
			log.Info("Finalizer removed, script deleted")
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(script, scriptFinalizerName) {
		log.Info("Adding finalizer to script")
		controllerutil.AddFinalizer(script, scriptFinalizerName)
		if err := r.Update(ctx, script); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		// Continue with reconciliation after adding finalizer
		log.V(1).Info("Finalizer added, continuing reconciliation")
	}

	// Validate HomeAssistant reference exists
	haRef := types.NamespacedName{
		Name:      script.Spec.HomeAssistantRef.Name,
		Namespace: script.Namespace,
	}
	ha, err := r.validateHomeAssistantRef(ctx, haRef, script)
	if err != nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Calculate hash of the script
	scriptHash, err := r.calculateScriptHash(script)
	if err != nil {
		log.Error(err, "Failed to calculate script hash")
		return ctrl.Result{}, err
	}

	// Get API token — requeue if not available yet (bootstrap may still be running)
	token, tokenErr := getAPIToken(ctx, r.Client, ha)
	if tokenErr != nil {
		log.Info("API token not available, requeueing")
		meta.SetStatusCondition(&script.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: script.Generation,
			Reason:             reasonTokenNotAvailable,
			Message:            errMsgTokenNotAvailable,
		})
		meta.SetStatusCondition(&script.Status.Conditions, metav1.Condition{
			Type:               "ReloadReady",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: script.Generation,
			Reason:             reasonTokenNotAvailable,
			Message:            errMsgTokenNotAvailable,
		})
		if statusErr := r.Status().Update(ctx, script); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// POST script via HA REST API
	if err := r.reconcileScriptViaAPI(ctx, script, ha, token); err != nil {
		log.Error(err, "Failed to POST script via HA REST API")
		meta.SetStatusCondition(&script.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             "ReconciliationFailed",
			Message:            fmt.Sprintf("Failed to POST script via HA API: %v", err),
			ObservedGeneration: script.Generation,
		})
		// Clear stale TokenNotAvailable from ReloadReady — token was found, failure is in the API call
		meta.SetStatusCondition(&script.Status.Conditions, metav1.Condition{
			Type:               "ReloadReady",
			Status:             metav1.ConditionFalse,
			Reason:             "ReconciliationFailed",
			Message:            fmt.Sprintf("Failed to POST script via HA API: %v", err),
			ObservedGeneration: script.Generation,
		})
		if statusErr := r.Status().Update(ctx, script); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// REST API PUT above already applied the script in HA in-memory.
	// No separate script.reload call needed — record apply time if hash changed.
	if script.Status.ScriptHash != scriptHash {
		log.Info("Script hash changed, recording REST API apply",
			"oldHash", script.Status.ScriptHash,
			"newHash", scriptHash)
		now := metav1.Now()
		script.Status.LastReloadTime = &now
		script.Status.LastReloadMethod = "api"
		script.Status.LastError = ""
		meta.SetStatusCondition(&script.Status.Conditions, metav1.Condition{
			Type:               "ReloadReady",
			Status:             metav1.ConditionTrue,
			ObservedGeneration: script.Generation,
			Reason:             "ReloadSuccessful",
			Message:            "Script applied via REST API",
		})
	}

	script.Status.ScriptHash = scriptHash
	script.Status.ObservedGeneration = script.Generation
	meta.SetStatusCondition(&script.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             reasonScriptGenerated,
		Message:            "Script successfully generated and loaded",
		ObservedGeneration: script.Generation,
	})

	if err := r.Status().Update(ctx, script); err != nil {
		log.Error(err, "Failed to update HomeAssistantScript status")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled HomeAssistantScript")
	return ctrl.Result{}, nil
}

// scriptToYaml converts HomeAssistantScript CR to YAML-compatible map
func (r *HomeAssistantScriptReconciler) scriptToYaml(
	script *hav1alpha1.HomeAssistantScript,
) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	// Alias (required)
	result["alias"] = script.Spec.Alias

	// Description (optional)
	if script.Spec.Description != "" {
		result["description"] = script.Spec.Description
	}

	// Icon (optional)
	if script.Spec.Icon != "" {
		result["icon"] = script.Spec.Icon
	}

	// Mode (optional, defaults handled by kubebuilder)
	if script.Spec.Mode != "" {
		result["mode"] = string(script.Spec.Mode)
	}

	// Max and MaxExceeded (only relevant for queued/parallel modes)
	if script.Spec.Mode == hav1alpha1.ScriptModeQueued || script.Spec.Mode == hav1alpha1.ScriptModeParallel {
		if script.Spec.Max != nil {
			result["max"] = *script.Spec.Max
		}
		if script.Spec.MaxExceeded != "" {
			result["max_exceeded"] = script.Spec.MaxExceeded
		}
	}

	// Fields (optional) - input parameters
	if len(script.Spec.Fields) > 0 {
		fields := make(map[string]interface{})
		for fieldName, fieldSpec := range script.Spec.Fields {
			var fieldData map[string]interface{}
			if err := json.Unmarshal(fieldSpec.Raw, &fieldData); err != nil {
				return nil, fmt.Errorf("failed to unmarshal field %s: %w", fieldName, err)
			}
			fields[fieldName] = fieldData
		}
		result["fields"] = fields
	}

	// Sequence (required) - list of actions
	var sequence []interface{}
	for i, action := range script.Spec.Sequence {
		var actionData map[string]interface{}
		if err := json.Unmarshal(action.Raw, &actionData); err != nil {
			return nil, fmt.Errorf("failed to unmarshal action %d: %w", i, err)
		}
		sequence = append(sequence, actionData)
	}
	result["sequence"] = sequence

	return result, nil
}

// reconcileScriptViaAPI creates or updates this script in Home Assistant
// via REST API (PUT /api/config/script/config/{id}).
// scriptToYaml returns the script config without the ID key, which is
// what the REST API expects (id is passed in the URL path).
// HA writes the result to scripts.yaml on the PVC (writable).
func (r *HomeAssistantScriptReconciler) reconcileScriptViaAPI(
	ctx context.Context,
	script *hav1alpha1.HomeAssistantScript,
	ha *hav1alpha1.HomeAssistant,
	token string,
) error {
	scriptData, err := r.scriptToYaml(script)
	if err != nil {
		return fmt.Errorf("failed to convert script to map: %w", err)
	}

	id := script.Spec.ID
	if id == "" {
		id = script.Name
	}

	haClient := r.haClientFor(ha)

	// If spec.id was renamed, delete the old script from HA to avoid orphans.
	prevID := script.Annotations[lastAppliedIDAnnotationKey]
	if prevID != "" && prevID != id {
		if delErr := haClient.DeleteScript(ctx, token, prevID); delErr != nil {
			log := logf.FromContext(ctx)
			log.Error(delErr, "Failed to delete old script ID from HA (continuing)", "prevID", prevID)
		}
	}

	if err := haClient.PutScript(ctx, token, id, scriptData); err != nil {
		return err
	}

	// Persist the applied ID so future reconciles can detect renames.
	orig := script.DeepCopy()
	if script.Annotations == nil {
		script.Annotations = map[string]string{}
	}
	script.Annotations[lastAppliedIDAnnotationKey] = id
	if patchErr := r.Patch(ctx, script, client.MergeFrom(orig)); patchErr != nil {
		log := logf.FromContext(ctx)
		log.Error(patchErr, "Failed to patch script annotation")
	}
	return nil
}

// calculateScriptHash computes SHA256 hash of the script spec.
// Includes the effective script ID (spec.id or CR name) so that ID-only
// changes also update the hash and trigger a reload.
func (r *HomeAssistantScriptReconciler) calculateScriptHash(
	script *hav1alpha1.HomeAssistantScript,
) (string, error) {
	// Effective ID matches the key used in the scripts ConfigMap
	scriptID := script.Spec.ID
	if scriptID == "" {
		scriptID = script.Name
	}

	// Convert spec to YAML for consistent hashing
	yamlData, err := r.scriptToYaml(script)
	if err != nil {
		return "", err
	}

	yamlBytes, err := yaml.Marshal(yamlData)
	if err != nil {
		return "", err
	}

	// Prefix with script ID so ID-only changes are captured
	data := append([]byte(scriptID+"\n"), yamlBytes...)
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash), nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *HomeAssistantScriptReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1alpha1.HomeAssistantScript{}).
		Watches(
			&hav1alpha1.HomeAssistant{},
			handler.EnqueueRequestsFromMapFunc(r.findScriptsForHomeAssistant),
		).
		Named("homeassistantscript").
		Complete(r)
}

// findScriptsForHomeAssistant returns reconcile requests for all HomeAssistantScript
// resources that reference the given HomeAssistant
func (r *HomeAssistantScriptReconciler) findScriptsForHomeAssistant(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	ha := obj.(*hav1alpha1.HomeAssistant)

	scriptList := &hav1alpha1.HomeAssistantScriptList{}
	if err := r.List(ctx, scriptList, client.InNamespace(ha.Namespace)); err != nil {
		return []reconcile.Request{}
	}

	var requests []reconcile.Request
	for _, script := range scriptList.Items {
		if script.Spec.HomeAssistantRef.Name == ha.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      script.Name,
					Namespace: script.Namespace,
				},
			})
		}
	}

	return requests
}

// validateHomeAssistantRef validates that referenced HomeAssistant exists
// and sets appropriate status condition if not found
func (r *HomeAssistantScriptReconciler) validateHomeAssistantRef(
	ctx context.Context,
	haRef types.NamespacedName,
	script *hav1alpha1.HomeAssistantScript,
) (*hav1alpha1.HomeAssistant, error) {
	log := logf.FromContext(ctx)

	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "Referenced HomeAssistant not found",
				"name", haRef.Name)
			meta.SetStatusCondition(&script.Status.Conditions,
				metav1.Condition{
					Type:   conditionTypeReady,
					Status: metav1.ConditionFalse,
					Reason: reasonInvalidScript,
					Message: fmt.Sprintf(
						"HomeAssistant %s not found",
						haRef.Name,
					),
					ObservedGeneration: script.Generation,
				})
			if err := r.Status().Update(ctx, script); err != nil {
				log.Error(err, "Failed to update status")
			}
			return nil, err
		}
		log.Error(err, "Failed to get HomeAssistant")
		return nil, err
	}
	return ha, nil
}
