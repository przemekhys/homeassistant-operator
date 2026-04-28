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

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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
	integrationFinalizerName = "ha.homeassistant.io/integration-finalizer"

	// Condition types and reasons for HomeAssistantIntegration
	reasonIntegrationConfigured        = "IntegrationConfigured"
	reasonAlreadyConfigured            = "AlreadyConfigured"
	reasonIntegrationHANotReady        = "HANotReady"
	reasonIntegrationTokenNotAvailable = "TokenNotAvailable"
	reasonConfigFlowFailed             = "ConfigFlowFailed"
	reasonSecretResolutionFailed       = "SecretResolutionFailed"

	// Event reasons
	eventIntegrationConfigured   = "IntegrationConfigured"
	eventIntegrationFailed       = "IntegrationFailed"
	eventIntegrationRemoved      = "IntegrationRemoved"
	eventIntegrationAdopted      = "IntegrationAdopted"
	eventIntegrationReconfigured = "IntegrationReconfigured"
)

// HomeAssistantIntegrationReconciler reconciles a HomeAssistantIntegration object
type HomeAssistantIntegrationReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Recorder    events.EventRecorder
	NewHAClient func(baseURL string) *haclient.Client // overridable for testing
}

// haClientFor returns a HA API client for the given HomeAssistant instance.
func (r *HomeAssistantIntegrationReconciler) haClientFor(ha *hav1alpha1.HomeAssistant) *haclient.Client {
	haURL := buildHomeAssistantURL(ha)
	if r.NewHAClient != nil {
		return r.NewHAClient(haURL)
	}
	return haclient.NewClient(haURL)
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantintegrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantintegrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantintegrations/finalizers,verbs=update
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop.
func (r *HomeAssistantIntegrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	integration := &hav1alpha1.HomeAssistantIntegration{}
	if err := r.Get(ctx, req.NamespacedName, integration); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Remove legacy condition type from before the Ready rename (one-time migration).
	meta.RemoveStatusCondition(&integration.Status.Conditions, "IntegrationReady")

	// --- DELETION ---
	if !integration.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(integration, integrationFinalizerName) {
			r.handleDeletion(ctx, integration)
			controllerutil.RemoveFinalizer(integration, integrationFinalizerName)
			if err := r.Update(ctx, integration); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// --- FINALIZER ---
	if !controllerutil.ContainsFinalizer(integration, integrationFinalizerName) {
		controllerutil.AddFinalizer(integration, integrationFinalizerName)
		if err := r.Update(ctx, integration); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// --- FETCH HomeAssistant CR ---
	haRef := types.NamespacedName{Name: integration.Spec.HomeAssistantRef.Name, Namespace: integration.Namespace}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		log.Info("HomeAssistant not found, requeueing", "name", haRef.Name)
		return r.setFailedCondition(ctx, integration, reasonIntegrationHANotReady,
			fmt.Sprintf("HomeAssistant %s not found", haRef.Name), 30*time.Second)
	}

	// --- API TOKEN ---
	token, tokenErr := getAPIToken(ctx, r.Client, ha)
	if tokenErr != nil {
		log.Info("API token not available, requeueing")
		return r.setFailedCondition(ctx, integration, reasonIntegrationTokenNotAvailable,
			errMsgTokenNotAvailable, 30*time.Second)
	}

	haClient := r.haClientFor(ha)

	// --- RESOLVE CONFIGURATION ---
	resolvedConfig, fp, err := r.resolveConfiguration(ctx, integration)
	if err != nil {
		log.Error(err, "Failed to resolve configuration values")
		return r.setFailedCondition(ctx, integration, reasonSecretResolutionFailed,
			fmt.Sprintf("Failed to resolve configuration: %v", err), 30*time.Second)
	}

	// --- CONFIG HASH ---
	configHash := r.calculateConfigHash(fp)

	// --- RECONFIGURATION ---
	// If config changed and we already have an entry, delete it and start fresh
	if integration.Status.EntryID != "" && integration.Status.ConfigHash != configHash {
		log.Info("Configuration changed, removing old config entry",
			"entryID", integration.Status.EntryID,
			"oldHash", integration.Status.ConfigHash,
			"newHash", configHash)
		if delErr := haClient.RemoveConfigEntry(ctx, token, integration.Status.EntryID); delErr != nil {
			log.Error(delErr, "Failed to remove old config entry")
			r.emitEvent(integration, corev1.EventTypeWarning, eventIntegrationFailed,
				fmt.Sprintf("Failed to remove old config entry: %v", delErr))
			return r.setFailedCondition(ctx, integration, reasonConfigFlowFailed,
				fmt.Sprintf("Failed to remove old config entry: %v", delErr), 30*time.Second)
		}
		integration.Status.EntryID = ""
		r.emitEvent(integration, corev1.EventTypeNormal, eventIntegrationReconfigured,
			"Configuration changed, re-creating config entry")
	}

	// --- VERIFY EXISTING ENTRY ---
	if integration.Status.EntryID != "" {
		exists, verifyErr := r.verifyEntryExists(ctx, haClient, token, integration.Status.EntryID)
		if verifyErr != nil {
			log.Error(verifyErr, "Failed to verify config entry existence, will retry")
			return r.setFailedCondition(ctx, integration, reasonIntegrationHANotReady,
				fmt.Sprintf("Failed to verify config entry: %v", verifyErr), 30*time.Second)
		} else if !exists {
			log.Info("Config entry no longer exists in HA, will re-create", "entryID", integration.Status.EntryID)
			integration.Status.EntryID = ""
		} else {
			// Entry exists and config hasn't changed — idempotent, update status
			return r.setReadyCondition(ctx, integration, reasonIntegrationConfigured,
				"Integration is configured and active", configHash)
		}
	}

	// --- ADOPT or CREATE ---
	// Check if integration is already configured in HA (e.g. configured via UI)
	configured, checkErr := haClient.IsIntegrationConfigured(ctx, token, integration.Spec.Domain)
	if checkErr != nil {
		log.Error(checkErr, "Failed to check if integration is configured")
		return r.setFailedCondition(ctx, integration, reasonConfigFlowFailed,
			fmt.Sprintf("Failed to check integration status: %v", checkErr), 30*time.Second)
	}

	if configured {
		// Adopt existing entry
		entryID, adoptErr := r.findEntryID(ctx, haClient, token, integration.Spec.Domain)
		if adoptErr != nil {
			log.Error(adoptErr, "Failed to find entry ID for adoption")
			return r.setFailedCondition(ctx, integration, reasonConfigFlowFailed,
				fmt.Sprintf("Failed to adopt integration: %v", adoptErr), 30*time.Second)
		}
		log.Info("Adopting existing integration", "domain", integration.Spec.Domain, "entryID", entryID)
		r.emitEvent(integration, corev1.EventTypeNormal, eventIntegrationAdopted,
			fmt.Sprintf("Adopted existing %s integration (entryID: %s)", integration.Spec.Domain, entryID))
		integration.Status.EntryID = entryID
		return r.setReadyCondition(ctx, integration, reasonAlreadyConfigured,
			fmt.Sprintf("Adopted existing %s integration", integration.Spec.Domain), configHash)
	}

	// --- START CONFIG FLOW ---
	flowResp, flowErr := haClient.StartConfigFlow(ctx, token, integration.Spec.Domain)
	if flowErr != nil {
		log.Error(flowErr, "Failed to start config flow")
		r.emitEvent(integration, corev1.EventTypeWarning, eventIntegrationFailed,
			fmt.Sprintf("Failed to start config flow for %s: %v", integration.Spec.Domain, flowErr))
		return r.setFailedCondition(ctx, integration, reasonConfigFlowFailed,
			fmt.Sprintf("Failed to start config flow: %v", flowErr), 30*time.Second)
	}

	// If flow immediately reached create_entry (zero-config integrations like recorder)
	if flowResp.Type == "create_entry" {
		return r.handleCreateEntry(ctx, integration, configHash, flowResp, log)
	}

	// If flow aborted — only adopt when reason is "already_configured"
	if flowResp.Type == "abort" {
		if flowResp.Reason != "already_configured" {
			r.emitEvent(integration, corev1.EventTypeWarning, eventIntegrationFailed,
				fmt.Sprintf("Config flow for %s aborted: %s", integration.Spec.Domain, flowResp.Reason))
			return r.setFailedCondition(ctx, integration, reasonConfigFlowFailed,
				fmt.Sprintf("Config flow aborted: %s", flowResp.Reason), 30*time.Second)
		}
		log.Info("Config flow aborted (already_configured), adopting", "domain", integration.Spec.Domain)
		entryID, adoptErr := r.findEntryID(ctx, haClient, token, integration.Spec.Domain)
		if adoptErr != nil {
			return r.setFailedCondition(ctx, integration, reasonConfigFlowFailed,
				fmt.Sprintf("Flow aborted, failed to adopt: %v", adoptErr), 30*time.Second)
		}
		r.emitEvent(integration, corev1.EventTypeNormal, eventIntegrationAdopted,
			fmt.Sprintf("Adopted existing %s integration (entryID: %s)", integration.Spec.Domain, entryID))
		integration.Status.EntryID = entryID
		return r.setReadyCondition(ctx, integration, reasonAlreadyConfigured,
			fmt.Sprintf("Adopted existing %s integration", integration.Spec.Domain), configHash)
	}

	// --- SUBMIT CONFIG FLOW (single-step) ---
	if len(resolvedConfig) > 0 {
		submitResp, submitErr := haClient.SubmitConfigFlow(ctx, token, flowResp.FlowID, resolvedConfig)
		if submitErr != nil {
			log.Error(submitErr, "Failed to submit config flow")
			r.emitEvent(integration, corev1.EventTypeWarning, eventIntegrationFailed,
				fmt.Sprintf("Failed to submit config flow for %s: %v", integration.Spec.Domain, submitErr))
			return r.setFailedCondition(ctx, integration, reasonConfigFlowFailed,
				fmt.Sprintf("Failed to submit config flow: %v", submitErr), 30*time.Second)
		}
		if submitResp.Type != "create_entry" {
			r.emitEvent(integration, corev1.EventTypeWarning, eventIntegrationFailed,
				fmt.Sprintf("Config flow for %s did not reach create_entry (got: %s)", integration.Spec.Domain, submitResp.Type))
			return r.setFailedCondition(ctx, integration, reasonConfigFlowFailed,
				fmt.Sprintf("Config flow did not complete: type=%s", submitResp.Type), 30*time.Second)
		}
		return r.handleCreateEntry(ctx, integration, configHash, submitResp, log)
	}

	// No configuration and flow returned a form — we can't proceed without data_schema fields
	r.emitEvent(integration, corev1.EventTypeWarning, eventIntegrationFailed,
		fmt.Sprintf("Config flow for %s requires configuration fields but none provided", integration.Spec.Domain))
	return r.setFailedCondition(ctx, integration, reasonConfigFlowFailed,
		"Config flow requires configuration fields but spec.configuration is empty", 0)
}

// handleDeletion removes the config entry from HA (best-effort)
func (r *HomeAssistantIntegrationReconciler) handleDeletion(
	ctx context.Context,
	integration *hav1alpha1.HomeAssistantIntegration,
) {
	log := logf.FromContext(ctx)
	if integration.Status.EntryID == "" {
		return
	}

	haRef := types.NamespacedName{Name: integration.Spec.HomeAssistantRef.Name, Namespace: integration.Namespace}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		log.Info("HA not found during deletion, skipping config entry removal")
		return
	}

	token, tokenErr := getAPIToken(ctx, r.Client, ha)
	if tokenErr != nil {
		log.Info("API token not available during deletion, skipping config entry removal")
		return
	}

	haClient := r.haClientFor(ha)
	if delErr := haClient.RemoveConfigEntry(ctx, token, integration.Status.EntryID); delErr != nil {
		log.Info("Failed to remove config entry during deletion (best-effort)", "error", delErr)
		return
	}

	log.Info("Config entry removed", "entryID", integration.Status.EntryID)
	r.emitEvent(integration, corev1.EventTypeNormal, eventIntegrationRemoved,
		fmt.Sprintf("Config entry %s removed from Home Assistant", integration.Status.EntryID))
}

// handleCreateEntry processes a successful create_entry flow response
func (r *HomeAssistantIntegrationReconciler) handleCreateEntry(
	ctx context.Context,
	integration *hav1alpha1.HomeAssistantIntegration,
	configHash string,
	flowResp *haclient.FlowResponse,
	log interface{ Info(string, ...interface{}) },
) (ctrl.Result, error) {
	entryResult, parseErr := haclient.ParseConfigEntryResult(flowResp)
	if parseErr != nil {
		// Could not parse entry_id — still mark as configured (best effort)
		log.Info("Could not parse entry_id from flow response, integration may be configured", "error", parseErr)
		r.emitEvent(integration, corev1.EventTypeNormal, eventIntegrationConfigured,
			fmt.Sprintf("Integration %s configured (entry_id unknown)", integration.Spec.Domain))
		return r.setReadyCondition(ctx, integration, reasonIntegrationConfigured,
			fmt.Sprintf("Integration %s configured via config flow", integration.Spec.Domain), configHash)
	}

	log.Info("Integration configured via config flow",
		"domain", integration.Spec.Domain,
		"entryID", entryResult.EntryID)
	r.emitEvent(integration, corev1.EventTypeNormal, eventIntegrationConfigured,
		fmt.Sprintf("Integration %s configured (entryID: %s)", integration.Spec.Domain, entryResult.EntryID))
	integration.Status.EntryID = entryResult.EntryID
	return r.setReadyCondition(ctx, integration, reasonIntegrationConfigured,
		fmt.Sprintf("Integration %s configured via config flow", integration.Spec.Domain), configHash)
}

// configFingerprint holds non-sensitive identifiers used to compute the config hash.
// Plain values are included directly; Secret references are represented by their
// name, key, and resourceVersion (so a secret rotation triggers reconfiguration
// without storing the actual secret bytes in status.configHash).
type configFingerprint struct {
	domain      string
	plainValues map[string]string // fieldKey → plain text value
	secretRefs  map[string]string // fieldKey → "secretName/secretKey/resourceVersion"
}

// resolveConfiguration resolves all IntegrationValues (plain + SecretKeyRef) to a string map
// for submission to the HA Config Flow API. It also returns a configFingerprint with
// non-sensitive identifiers for stable hashing.
func (r *HomeAssistantIntegrationReconciler) resolveConfiguration(
	ctx context.Context,
	integration *hav1alpha1.HomeAssistantIntegration,
) (map[string]interface{}, configFingerprint, error) {
	resolved := make(map[string]interface{})
	fp := configFingerprint{
		domain:      integration.Spec.Domain,
		plainValues: make(map[string]string),
		secretRefs:  make(map[string]string),
	}
	for key, val := range integration.Spec.Configuration {
		if val.Value != nil {
			resolved[key] = *val.Value
			fp.plainValues[key] = *val.Value
			continue
		}
		if val.JSONValue != nil {
			var parsed interface{}
			if err := json.Unmarshal([]byte(*val.JSONValue), &parsed); err != nil {
				return nil, configFingerprint{}, fmt.Errorf(
					"field %q: jsonValue is not valid JSON: %w", key, err)
			}
			canonical, err := json.Marshal(parsed)
			if err != nil {
				return nil, configFingerprint{}, fmt.Errorf(
					"field %q: failed to re-marshal jsonValue: %w", key, err)
			}
			resolved[key] = parsed
			fp.plainValues[key] = string(canonical)
			continue
		}
		if val.SecretKeyRef != nil {
			secret := &corev1.Secret{}
			secretRef := types.NamespacedName{
				Name:      val.SecretKeyRef.Name,
				Namespace: integration.Namespace,
			}
			if err := r.Get(ctx, secretRef, secret); err != nil {
				return nil, configFingerprint{}, fmt.Errorf("failed to get Secret %s: %w", val.SecretKeyRef.Name, err)
			}
			secretValue, ok := secret.Data[val.SecretKeyRef.Key]
			if !ok {
				return nil, configFingerprint{}, fmt.Errorf(
					"key %q not found in Secret %s", val.SecretKeyRef.Key, val.SecretKeyRef.Name)
			}
			resolved[key] = string(secretValue)
			fp.secretRefs[key] = fmt.Sprintf("%s/%s/%s", val.SecretKeyRef.Name, val.SecretKeyRef.Key, secret.ResourceVersion)
			continue
		}
		// Neither Value nor SecretKeyRef — skip (XValidation prevents this at admission)
	}
	return resolved, fp, nil
}

// calculateConfigHash computes a stable hash from non-sensitive identifiers:
// domain, plain text field values, and secret name/key/resourceVersion references.
// Secret contents are never included in the hash.
func (r *HomeAssistantIntegrationReconciler) calculateConfigHash(fp configFingerprint) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "domain=%s", fp.domain)
	// Sort plain value keys for stability
	plainKeys := make([]string, 0, len(fp.plainValues))
	for k := range fp.plainValues {
		plainKeys = append(plainKeys, k)
	}
	sortStrings(plainKeys)
	for _, k := range plainKeys {
		_, _ = fmt.Fprintf(h, "|plain:%s=%s", k, fp.plainValues[k])
	}
	// Sort secret ref keys for stability
	secretKeys := make([]string, 0, len(fp.secretRefs))
	for k := range fp.secretRefs {
		secretKeys = append(secretKeys, k)
	}
	sortStrings(secretKeys)
	for _, k := range secretKeys {
		_, _ = fmt.Fprintf(h, "|secret:%s=%s", k, fp.secretRefs[k])
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// sortStrings sorts a string slice in-place (insertion sort for small slices)
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// verifyEntryExists checks if the given entry ID still exists in HA
func (r *HomeAssistantIntegrationReconciler) verifyEntryExists(
	ctx context.Context,
	haClient *haclient.Client,
	token, entryID string,
) (bool, error) {
	entries, err := haClient.ListConfigEntries(ctx, token)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.EntryID == entryID {
			return true, nil
		}
	}
	return false, nil
}

// findEntryID finds the first entry ID for the given domain
func (r *HomeAssistantIntegrationReconciler) findEntryID(
	ctx context.Context,
	haClient *haclient.Client,
	token, domain string,
) (string, error) {
	entries, err := haClient.ListConfigEntries(ctx, token)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Domain == domain {
			return e.EntryID, nil
		}
	}
	return "", fmt.Errorf("no config entry found for domain %s", domain)
}

// setReadyCondition updates status to ready and persists
func (r *HomeAssistantIntegrationReconciler) setReadyCondition(
	ctx context.Context,
	integration *hav1alpha1.HomeAssistantIntegration,
	reason, message, configHash string,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	integration.Status.ConfigHash = configHash
	integration.Status.ObservedGeneration = integration.Generation
	integration.Status.LastError = ""
	meta.SetStatusCondition(&integration.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: integration.Generation,
	})
	if err := r.Status().Update(ctx, integration); err != nil {
		log.Error(err, "Failed to update integration status")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// setFailedCondition updates status to not-ready and requeues
func (r *HomeAssistantIntegrationReconciler) setFailedCondition(
	ctx context.Context,
	integration *hav1alpha1.HomeAssistantIntegration,
	reason, message string,
	requeueAfter time.Duration,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	integration.Status.ObservedGeneration = integration.Generation
	integration.Status.LastError = message
	meta.SetStatusCondition(&integration.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: integration.Generation,
	})
	if err := r.Status().Update(ctx, integration); err != nil {
		log.Error(err, "Failed to update integration status")
		return ctrl.Result{}, err
	}
	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// emitEvent emits a Kubernetes event for the integration
func (r *HomeAssistantIntegrationReconciler) emitEvent(
	integration *hav1alpha1.HomeAssistantIntegration,
	eventType, reason, message string,
) {
	if r.Recorder != nil {
		r.Recorder.Eventf(integration, nil, eventType, reason, reason, message)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *HomeAssistantIntegrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1alpha1.HomeAssistantIntegration{}).
		Watches(
			&hav1alpha1.HomeAssistant{},
			handler.EnqueueRequestsFromMapFunc(r.findIntegrationsForHomeAssistant),
		).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findIntegrationsForSecret),
		).
		Named("homeassistantintegration").
		Complete(r)
}

// findIntegrationsForSecret returns reconcile requests for integrations that reference a Secret in their configuration
func (r *HomeAssistantIntegrationReconciler) findIntegrationsForSecret(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	secret := obj.(*corev1.Secret)
	list := &hav1alpha1.HomeAssistantIntegrationList{}
	if err := r.List(ctx, list, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, item := range list.Items {
		for _, val := range item.Spec.Configuration {
			if val.SecretKeyRef != nil && val.SecretKeyRef.Name == secret.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: item.Name, Namespace: item.Namespace},
				})
				break
			}
		}
	}
	return requests
}

// findIntegrationsForHomeAssistant returns reconcile requests for all integrations referencing the HA
func (r *HomeAssistantIntegrationReconciler) findIntegrationsForHomeAssistant(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	ha := obj.(*hav1alpha1.HomeAssistant)
	list := &hav1alpha1.HomeAssistantIntegrationList{}
	if err := r.List(ctx, list, client.InNamespace(ha.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, item := range list.Items {
		if item.Spec.HomeAssistantRef.Name == ha.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: item.Name, Namespace: item.Namespace},
			})
		}
	}
	return requests
}
