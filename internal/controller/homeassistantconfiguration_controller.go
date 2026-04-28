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
	"reflect"
	"time"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	configurationYamlKey     = "configuration.yaml"
	generatedConfigmapSuffix = "-configuration"
	// recorderDBSecretSuffix is the suffix for the K8s Secret that holds the recorder
	// database URL when spec.recorder.databaseSecretRef is used. The Secret is mounted
	// into the HA pod at /config/recorder_db_url.yaml so that credentials are never
	// placed in a ConfigMap.
	recorderDBSecretSuffix = "-recorder-db"
	// configHashAnnotationKey moved to constants.go (shared with homeassistant_controller.go)

	// Condition reasons for HomeAssistantConfiguration
	reasonConfigurationGenerated = "ConfigurationGenerated"
	reasonConfigurationNotFound  = "ConfigurationNotFound"
	reasonInvalidConfig          = "InvalidConfiguration"
	// Note: reloadMethodRestart, reloadMethodHotReload, reloadMethodNone,
	// and defaultHomeAssistantPort are defined in constants.go
)

// Critical sections that always require pod restart
var criticalSections = map[string]bool{
	"homeassistant": true,
	"http":          true,
}

// Sections that can be hot-reloaded
var reloadableSections = map[string]bool{
	"automation":     true,
	"script":         true,
	"scene":          true,
	"group":          true,
	"input_boolean":  true,
	"input_number":   true,
	"input_select":   true,
	"input_text":     true,
	"input_datetime": true,
	"timer":          true,
	"counter":        true,
	"logger":         true,
	// "template" and "zone" require dedicated reload services (template.reload,
	// zone.reload) — not covered by ReloadCoreConfig. Implement proper dispatch
	// before adding them here to avoid incorrect lastReloadMethod=hot-reload.
}

// Keys under homeassistant: that require restart
var criticalHomeAssistantKeys = map[string]bool{
	"name":         true,
	"latitude":     true,
	"longitude":    true,
	"elevation":    true,
	"time_zone":    true,
	"unit_system":  true,
	"currency":     true,
	"country":      true,
	"language":     true,
	"internal_url": true,
	"external_url": true,
}

// Keys under homeassistant: that can be hot-reloaded
var reloadableHomeAssistantKeys = map[string]bool{
	"customize":        true,
	"customize_domain": true,
	"customize_glob":   true,
}

// Keys under http: that require restart
var criticalHttpKeys = map[string]bool{
	"server_port":     true,
	"ssl_certificate": true,
	"ssl_key":         true,
	"ssl_profile":     true,
	"ip_ban_enabled":  true,
}

// Keys under http: that can be hot-reloaded
var reloadableHttpKeys = map[string]bool{
	"cors_allowed_origins": true,
	"use_x_forwarded_for":  true,
	"trusted_proxies":      true,
}

// HomeAssistantConfigurationReconciler reconciles a HomeAssistantConfiguration object
type HomeAssistantConfigurationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantconfigurations,verbs=get;list;watch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantconfigurations,verbs=create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantconfigurations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantconfigurations/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *HomeAssistantConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the HomeAssistantConfiguration instance
	config := &hav1alpha1.HomeAssistantConfiguration{}
	if err := r.Get(ctx, req.NamespacedName, config); err != nil {
		if errors.IsNotFound(err) {
			log.Info("HomeAssistantConfiguration resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get HomeAssistantConfiguration")
		return ctrl.Result{}, err
	}

	// Validate HomeAssistant reference exists
	haRef := types.NamespacedName{
		Name:      config.Spec.HomeAssistantRef.Name,
		Namespace: config.Namespace,
	}
	ha, err := r.validateHomeAssistantRef(ctx, haRef, config)
	if err != nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Compute the canonical configuration content (auto-includes + location + recorder secret
	// injection) once so that hash, ConfigMap data, sync detection, and needsRestart all use
	// identical bytes. Using buildEffectiveConfig here would exclude the recorder section,
	// causing syncConfigMapFromCRD to see a permanent mismatch and trigger spurious reloads.
	canonicalContent, err := r.buildConfigContent(ctx, config, ha)
	if err != nil {
		log.Error(err, "Failed to build canonical configuration content")
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             "ReconciliationFailed",
			Message:            fmt.Sprintf("Failed to build configuration content: %v", err),
			ObservedGeneration: config.Generation,
		})
		if statusErr := r.Status().Update(ctx, config); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	configHash := calculateConfigHash(canonicalContent)

	// Capture old configuration BEFORE updating ConfigMap
	// This is critical for needsRestart() to work correctly in auto mode
	var oldConfig string
	configMapName := config.Spec.HomeAssistantRef.Name + generatedConfigmapSuffix
	existingConfigMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: config.Namespace,
	}, existingConfigMap); err == nil {
		oldConfig = existingConfigMap.Data[configurationYamlKey]
	}

	// Sync ConfigMap back to CRD state if it was modified externally (operator exclusivity).
	// Only when the spec has NOT changed: if spec changed, reconcileGeneratedConfigMap is the
	// sole writer. Running both would cause a cache-stale conflict (syncConfigMapFromCRD writes
	// version N→N+1; reconcileGeneratedConfigMap reads stale cache at N and gets a conflict),
	// and on retry oldConfig == transformedConfig triggers the restart fallback instead
	// of the correct hot-reload decision.
	syncedContent := false
	if config.Status.ConfigHash == configHash {
		var syncErr error
		syncedContent, syncErr = r.syncConfigMapFromCRD(ctx, config, canonicalContent)
		if syncErr != nil {
			log.Error(syncErr, "Failed to sync ConfigMap from CRD")
		}
	}

	// Create or update the ConfigMap
	if err := r.reconcileGeneratedConfigMap(ctx, config, canonicalContent); err != nil {
		log.Error(err, "Failed to reconcile generated ConfigMap")
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             "ReconciliationFailed",
			Message:            fmt.Sprintf("Failed to create/update ConfigMap: %v", err),
			ObservedGeneration: config.Generation,
		})
		if statusErr := r.Status().Update(ctx, config); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
		}
		return ctrl.Result{}, err
	}

	// Perform configuration reload if hash changed or if ConfigMap was restored from external edit
	if config.Status.ConfigHash != configHash || syncedContent {
		if err := r.performConfigReload(ctx, config, ha, configHash, oldConfig, canonicalContent); err != nil {
			log.Error(err, "Failed to reload configuration")
			meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
				Type:               conditionTypeReady,
				Status:             metav1.ConditionFalse,
				Reason:             "ReloadFailed",
				Message:            err.Error(),
				ObservedGeneration: config.Generation,
			})
			_ = r.Status().Update(ctx, config)
			return ctrl.Result{}, err
		}
	}

	// Update status
	config.Status.ConfigHash = configHash
	config.Status.ObservedGeneration = config.Generation
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             reasonConfigurationGenerated,
		Message:            "Configuration successfully generated as ConfigMap",
		ObservedGeneration: config.Generation,
	})

	if err := r.Status().Update(ctx, config); err != nil {
		log.Error(err, "Failed to update HomeAssistantConfiguration status")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled HomeAssistantConfiguration")
	return ctrl.Result{}, nil
}

// reconcileGeneratedConfigMap creates or updates the ConfigMap containing
// configuration.yaml
func (r *HomeAssistantConfigurationReconciler) reconcileGeneratedConfigMap(
	ctx context.Context,
	config *hav1alpha1.HomeAssistantConfiguration,
	canonicalContent string,
) error {
	log := logf.FromContext(ctx)

	configMapName := config.Spec.HomeAssistantRef.Name + generatedConfigmapSuffix

	// Check if ConfigMap already exists
	existingConfigMap := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: config.Namespace}, existingConfigMap)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create new ConfigMap WITHOUT hash annotation
			// The hash annotation is ONLY set by performConfigReload() when restart is needed
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: config.Namespace,
					Labels: map[string]string{
						labelAppName:      "homeassistant",
						labelAppInstance:  config.Spec.HomeAssistantRef.Name,
						labelAppManagedBy: "homeassistant-operator",
					},
					// NO hash annotation on initial creation
				},
				Data: map[string]string{
					configurationYamlKey: canonicalContent,
				},
			}

			// Set HomeAssistantConfiguration as the owner
			if err := controllerutil.SetControllerReference(config, configMap, r.Scheme); err != nil {
				return err
			}

			log.Info("Creating new generated ConfigMap (no hash annotation initially)", "name", configMapName)
			if err := r.Create(ctx, configMap); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	// Update existing ConfigMap content if changed
	// IMPORTANT: We do NOT update the hash annotation here.
	// The hash annotation is ONLY updated by performConfigReload() when restart strategy is used.
	// For hot-reload, we update content but preserve the old annotation to avoid triggering pod restart.

	// Verify ownership before updating - check if owned by a DIFFERENT resource
	if len(existingConfigMap.OwnerReferences) > 0 {
		owner := existingConfigMap.OwnerReferences[0]
		// Check if owned by a different HomeAssistantConfiguration (by name, not UID)
		// This protects against accidentally modifying ConfigMaps from other resources
		if owner.Kind == "HomeAssistantConfiguration" && owner.Name != config.Name {
			log.Info("ConfigMap exists but is owned by different HomeAssistantConfiguration; skipping update",
				"name", configMapName,
				"owner", owner.Name)
			return nil
		}
		// If owner name matches but UID different (e.g., CR was deleted and recreated),
		// update the owner reference to point to current CR
		if owner.Name == config.Name && owner.UID != config.UID {
			log.Info("ConfigMap owned by old instance of same CR, updating owner reference",
				"name", configMapName)
			if err := controllerutil.SetControllerReference(config, existingConfigMap, r.Scheme); err != nil {
				return err
			}
			if err := r.Update(ctx, existingConfigMap); err != nil {
				return err
			}
		}
	} else {
		// ConfigMap exists but has no owner - adopt it by setting owner reference
		log.Info("Adopting existing ConfigMap (no owner reference)", "name", configMapName)
		if err := controllerutil.SetControllerReference(config, existingConfigMap, r.Scheme); err != nil {
			return err
		}
		if err := r.Update(ctx, existingConfigMap); err != nil {
			return err
		}
	}

	existingData := existingConfigMap.Data[configurationYamlKey]
	if existingData != canonicalContent {
		log.Info("Updating generated ConfigMap content (hash annotation preserved for hot-reload)", "name", configMapName)
		existingConfigMap.Data[configurationYamlKey] = canonicalContent
		if err := r.Update(ctx, existingConfigMap); err != nil {
			return err
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *HomeAssistantConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1alpha1.HomeAssistantConfiguration{}).
		Owns(&corev1.ConfigMap{}).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.findHomeAssistantConfigurationForConfigMap),
		).
		Named("homeassistantconfiguration").
		Complete(r)
}

// needsRestart analyzes configuration changes and determines if restart is required
// Returns true if restart is needed, false if hot-reload is safe
func needsRestart(oldConfig, newConfig string) (bool, error) {
	// Parse both configs
	var oldYAML, newYAML map[string]interface{}

	if err := yaml.Unmarshal([]byte(oldConfig), &oldYAML); err != nil {
		return true, fmt.Errorf("failed to parse old config: %w", err) // Safe default: restart on parse error
	}

	if err := yaml.Unmarshal([]byte(newConfig), &newYAML); err != nil {
		return true, fmt.Errorf("failed to parse new config: %w", err) // Safe default: restart on parse error
	}

	// Initialize maps if nil
	if oldYAML == nil {
		oldYAML = make(map[string]interface{})
	}
	if newYAML == nil {
		newYAML = make(map[string]interface{})
	}

	// Check for new top-level sections (adding integrations)
	for key := range newYAML {
		if _, existed := oldYAML[key]; !existed {
			// New section added
			if reloadableSections[key] {
				continue // Can be hot-reloaded
			}
			// Unknown or critical section added - requires restart
			return true, nil
		}
	}

	// Check for removed sections
	for key := range oldYAML {
		if _, exists := newYAML[key]; !exists {
			// Section removed - always requires restart
			return true, nil
		}
	}

	// Check critical sections for changes
	for section := range criticalSections {
		if changed, critical := sectionChanged(oldYAML, newYAML, section); changed {
			if critical {
				return true, nil
			}
		}
	}

	// All changes are either in reloadable sections or non-critical changes
	return false, nil
}

// sectionChanged checks if a specific section changed and if it's critical
func sectionChanged(old, new map[string]interface{}, section string) (changed bool, critical bool) {
	oldSection, oldExists := old[section]
	newSection, newExists := new[section]

	if !oldExists && !newExists {
		return false, false
	}

	if !oldExists || !newExists {
		return true, true // Section added or removed
	}

	// Special handling for homeassistant section
	if section == "homeassistant" {
		return homeassistantSectionChanged(oldSection, newSection)
	}

	// Special handling for http section
	if section == "http" {
		return httpSectionChanged(oldSection, newSection)
	}

	// For other sections, just check if reloadable
	oldStr := fmt.Sprintf("%v", oldSection)
	newStr := fmt.Sprintf("%v", newSection)

	if oldStr != newStr {
		return true, criticalSections[section]
	}

	return false, false
}

// homeassistantSectionChanged checks homeassistant: section changes
func homeassistantSectionChanged(old, new interface{}) (changed bool, critical bool) {
	oldMap, oldOk := old.(map[string]interface{})
	newMap, newOk := new.(map[string]interface{})

	if !oldOk || !newOk {
		return true, true
	}

	// Check for critical key changes
	for key := range criticalHomeAssistantKeys {
		oldVal, oldHas := oldMap[key]
		newVal, newHas := newMap[key]

		if oldHas != newHas || fmt.Sprintf("%v", oldVal) != fmt.Sprintf("%v", newVal) {
			return true, true // Critical change
		}
	}

	// Check for reloadable key changes
	for key := range reloadableHomeAssistantKeys {
		oldVal, oldHas := oldMap[key]
		newVal, newHas := newMap[key]

		if oldHas != newHas || fmt.Sprintf("%v", oldVal) != fmt.Sprintf("%v", newVal) {
			return true, false // Reloadable change
		}
	}

	// Check if logger settings changed (reloadable)
	if !reflect.DeepEqual(oldMap["logger"], newMap["logger"]) {
		return true, false // Logger is reloadable
	}

	// Check if automations changed (reloadable)
	if !reflect.DeepEqual(oldMap["automation"], newMap["automation"]) {
		return true, false // Automations are reloadable
	}

	return false, false
}

// httpSectionChanged checks http: section changes
func httpSectionChanged(old, new interface{}) (changed bool, critical bool) {
	oldMap, oldOk := old.(map[string]interface{})
	newMap, newOk := new.(map[string]interface{})

	if !oldOk || !newOk {
		return true, true
	}

	// Check for critical HTTP key changes
	for key := range criticalHttpKeys {
		oldVal, oldHas := oldMap[key]
		newVal, newHas := newMap[key]

		if oldHas != newHas || fmt.Sprintf("%v", oldVal) != fmt.Sprintf("%v", newVal) {
			return true, true // Critical change
		}
	}

	// Check for reloadable HTTP key changes
	for key := range reloadableHttpKeys {
		oldVal, oldHas := oldMap[key]
		newVal, newHas := newMap[key]

		if oldHas != newHas || fmt.Sprintf("%v", oldVal) != fmt.Sprintf("%v", newVal) {
			return true, false // Reloadable change
		}
	}

	return false, false
}

// buildHomeAssistantURL constructs the URL for Home Assistant service
func (r *HomeAssistantConfigurationReconciler) buildHomeAssistantURL(ha *hav1alpha1.HomeAssistant) string {
	// Service name matches the HomeAssistant CR name (not ha.Name + "-homeassistant")
	// See homeassistant_controller.go:578 for Service creation
	serviceName := ha.Name
	port := int32(defaultHomeAssistantPort)
	if ha.Spec.Service != nil && ha.Spec.Service.Port != 0 {
		port = ha.Spec.Service.Port
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", serviceName, ha.Namespace, port)
}

// performHotReload attempts to hot-reload the configuration via HA REST API
// Retries with fixed interval to handle kubelet ConfigMap sync delay
// Kubelet typically syncs ConfigMap volumes every 60s (syncFrequency), so we need
// to wait for the file to be synced to the pod before hot-reload will work correctly
func (r *HomeAssistantConfigurationReconciler) performHotReload(ctx context.Context, haURL, token string) error {
	log := logf.FromContext(ctx)

	haClient := haclient.NewClient(haURL)

	log.Info("Waiting for kubelet to sync ConfigMap to pod")

	// Poll until CheckConfig succeeds (indicates file is synced)
	// Kubelet syncFrequency is typically 60s, so we need generous timeout
	const maxRetries = 20 // 20 * 5s = 100s max wait
	const retryInterval = 5 * time.Second

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			log.V(1).Info("Config not ready yet (waiting for kubelet sync)",
				"attempt", attempt+1,
				"waitTime", fmt.Sprintf("%ds", attempt*5),
				"nextRetryIn", retryInterval)
			time.Sleep(retryInterval)
		}

		// First, validate the configuration
		// CheckConfig reads /config/configuration.yaml from pod
		// It will fail if:
		// - Kubelet hasn't synced yet → old/invalid config
		// - Config has syntax errors
		if err := haClient.CheckConfig(ctx, token); err != nil {
			lastErr = err
			log.V(1).Info("Configuration validation failed, will retry", "attempt", attempt+1, "error", err.Error())
			continue
		}

		// Config is valid and readable - kubelet must have synced
		waitTime := time.Duration(attempt) * retryInterval
		log.Info("Config synced and validated, proceeding with hot-reload", "waitTime", waitTime)

		// Reload the core configuration
		if err := haClient.ReloadCoreConfig(ctx, token); err != nil {
			lastErr = err
			log.V(1).Info("Failed to reload configuration, will retry", "attempt", attempt+1, "error", err.Error())
			continue
		}

		log.Info("Configuration hot-reload successful", "attempts", attempt+1, "totalWaitTime", waitTime)
		return nil
	}

	log.Error(lastErr,
		"Configuration hot-reload failed after retries - timeout waiting for config sync",
		"maxRetries", maxRetries)
	return fmt.Errorf("timeout waiting for config sync: %w", lastErr)
}

// updateStatefulSetConfigAnnotation was removed in Faza 1 refactor.
// StatefulSet annotation updates are now handled by HomeAssistant Controller,
// which reads the hash from ConfigMap annotation and syncs to StatefulSet.
// See rozwiazanie-architektury.md for details.

// performConfigReload executes reload based on strategy
// oldConfig parameter contains configuration content captured BEFORE ConfigMap update
func (r *HomeAssistantConfigurationReconciler) performConfigReload(
	ctx context.Context,
	config *hav1alpha1.HomeAssistantConfiguration,
	ha *hav1alpha1.HomeAssistant,
	newHash string,
	oldConfig string,
	canonicalContent string,
) error {
	log := logf.FromContext(ctx)

	// Check if autoReload is enabled (default: true)
	if config.Spec.AutoReload != nil && !*config.Spec.AutoReload {
		log.V(1).Info("AutoReload disabled, skipping reload")
		return nil
	}

	// Check if hash actually changed
	if config.Status.ConfigHash == newHash {
		log.V(1).Info("Configuration hash unchanged, no reload needed")
		return nil
	}

	// Get API token for hot-reload attempts
	token, tokenErr := getAPIToken(ctx, r.Client, ha)

	// Build Home Assistant URL
	haURL := r.buildHomeAssistantURL(ha)

	// Determine effective strategy
	strategy := string(config.Spec.ReloadStrategy)
	if strategy == "" || strategy == string(hav1alpha1.ConfigurationReloadStrategyAuto) {
		// Auto: decide based on config changes
		// Use oldConfig passed from Reconcile (captured before ConfigMap update)
		// This ensures needsRestart compares actual old vs new, not new vs new

		// Retry safety: if ConfigMap was already updated in a previous reconcile attempt
		// that failed before saving status, oldConfig == newConfig and we cannot determine
		// what changed. Default to restart (safe) to avoid choosing hot-reload for changes
		// that require a full restart (e.g. adding a new integration like prometheus:).
		transformedConfig := canonicalContent
		if oldConfig == transformedConfig {
			log.Info("ConfigMap already synced (retry after partial failure), defaulting to restart")
			strategy = reloadMethodRestart
		} else {
			restartNeeded, parseErr := needsRestart(oldConfig, transformedConfig)
			if parseErr != nil {
				log.Error(parseErr, "Failed to analyze config changes, defaulting to restart")
				restartNeeded = true
			}

			if restartNeeded || tokenErr != nil {
				strategy = reloadMethodRestart
			} else {
				strategy = reloadMethodHotReload
			}
		}
	}

	// Execute strategy
	now := metav1.Now()
	config.Status.LastReloadTime = &now

	if strategy == string(hav1alpha1.ConfigurationReloadStrategyRestart) || strategy == reloadMethodRestart {
		// Update ConfigMap annotation hash to trigger pod restart via HomeAssistant Controller
		if err := r.updateConfigMapHashAnnotation(ctx, config, newHash); err != nil {
			return fmt.Errorf("failed to update ConfigMap hash for restart: %w", err)
		}

		config.Status.LastReloadMethod = reloadMethodRestart
		config.Status.LastError = ""
		log.Info(
			"Configuration reload: restart "+
				"(updated ConfigMap hash to trigger StatefulSet rolling restart)",
			"hash", newHash)
		return nil
	}

	// Try hot-reload (strategy is hot-reload or auto decided to try it)
	if tokenErr != nil {
		// If user explicitly requested hot-reload strategy, fail instead of falling back
		if strategy == string(hav1alpha1.ConfigurationReloadStrategyHotReload) {
			config.Status.LastError = fmt.Sprintf("Hot-reload strategy requested but no API token available: %v", tokenErr)
			return fmt.Errorf("hot-reload strategy requires API token but none available: %w", tokenErr)
		}

		// Auto strategy can fallback to restart
		log.Error(tokenErr, "No API token available, falling back to restart")
		config.Status.LastError = fmt.Sprintf("No API token: %v", tokenErr)

		// Update ConfigMap annotation to trigger restart via HomeAssistant Controller
		if err := r.updateConfigMapHashAnnotation(ctx, config, newHash); err != nil {
			return fmt.Errorf("failed to update ConfigMap hash for restart fallback: %w", err)
		}

		config.Status.LastReloadMethod = reloadMethodRestart
		log.Info("Configuration reload: restart (fallback - no API token)", "hash", newHash)
		return nil
	}

	// Check if Home Assistant Service is ready before attempting hot-reload
	if !r.isHomeAssistantServiceReady(ctx, ha) {
		log.Info("Home Assistant Service not ready yet, cannot perform hot-reload")

		// If user explicitly requested hot-reload strategy, fail instead of falling back
		if strategy == string(hav1alpha1.ConfigurationReloadStrategyHotReload) {
			config.Status.LastError = "Hot-reload strategy requested but Service not ready (pod not ready)"
			return fmt.Errorf("hot-reload strategy requires ready Service but pod is not ready yet")
		}

		// Auto strategy can fallback to restart
		log.Info("Service not ready, falling back to restart")
		config.Status.LastError = "Service not ready - falling back to restart"

		// Update ConfigMap annotation to trigger restart via HomeAssistant Controller
		if err := r.updateConfigMapHashAnnotation(ctx, config, newHash); err != nil {
			return fmt.Errorf("failed to update ConfigMap hash for restart fallback: %w", err)
		}

		config.Status.LastReloadMethod = reloadMethodRestart
		log.Info("Configuration reload: restart (fallback - Service not ready)", "hash", newHash)
		return nil
	}

	// Attempt hot-reload
	if err := r.performHotReload(ctx, haURL, token); err != nil {
		// If user explicitly requested hot-reload strategy, fail instead of falling back
		if strategy == string(hav1alpha1.ConfigurationReloadStrategyHotReload) {
			config.Status.LastError = fmt.Sprintf("Hot-reload failed: %v", err)
			return fmt.Errorf("hot-reload strategy failed: %w", err)
		}

		// Auto strategy can fallback to restart
		log.Error(err, "Hot-reload failed, falling back to restart")
		config.Status.LastError = fmt.Sprintf("Hot-reload failed: %v", err)

		// Update ConfigMap annotation to trigger restart via HomeAssistant Controller
		if updateErr := r.updateConfigMapHashAnnotation(ctx, config, newHash); updateErr != nil {
			return fmt.Errorf("failed to update ConfigMap hash for restart fallback: %w", updateErr)
		}

		config.Status.LastReloadMethod = reloadMethodRestart
		log.Info("Configuration reload: restart (fallback - hot-reload failed)", "hash", newHash)
		return nil
	}

	// Hot-reload succeeded - do NOT update ConfigMap hash annotation
	// This prevents unnecessary pod restart
	config.Status.LastReloadMethod = reloadMethodHotReload
	config.Status.LastError = ""
	log.Info("Configuration reload: hot-reload (no pod restart)", "hash", newHash)
	return nil
}

// updateConfigMapHashAnnotation updates the hash annotation on ConfigMap to trigger pod restart
// This should ONLY be called when restart strategy is used, not during hot-reload
func (r *HomeAssistantConfigurationReconciler) updateConfigMapHashAnnotation(
	ctx context.Context,
	config *hav1alpha1.HomeAssistantConfiguration,
	newHash string,
) error {
	log := logf.FromContext(ctx)

	configMapName := config.Spec.HomeAssistantRef.Name + generatedConfigmapSuffix
	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: config.Namespace}, configMap); err != nil {
		return fmt.Errorf("failed to get ConfigMap for hash update: %w", err)
	}

	if configMap.Annotations == nil {
		configMap.Annotations = make(map[string]string)
	}

	oldHash := configMap.Annotations[configHashAnnotationKey]
	if oldHash == newHash {
		// Hash already matches, no update needed
		return nil
	}

	configMap.Annotations[configHashAnnotationKey] = newHash
	if err := r.Update(ctx, configMap); err != nil {
		return fmt.Errorf("failed to update ConfigMap hash annotation: %w", err)
	}

	log.Info("Updated ConfigMap hash annotation to trigger pod restart",
		"configMapName", configMapName,
		"oldHash", oldHash,
		"newHash", newHash)
	return nil
}

// syncConfigMapFromCRD ensures ConfigMap matches CRD state (operator exclusivity).
// Returns true if the ConfigMap content was actually updated (so the caller can
// trigger a reload even when the spec hash has not changed).
func (r *HomeAssistantConfigurationReconciler) syncConfigMapFromCRD(
	ctx context.Context,
	config *hav1alpha1.HomeAssistantConfiguration,
	canonicalContent string,
) (bool, error) {
	log := logf.FromContext(ctx)

	configMapName := config.Spec.HomeAssistantRef.Name + generatedConfigmapSuffix
	existingConfigMap := &corev1.ConfigMap{}

	err := r.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: config.Namespace,
	}, existingConfigMap)

	if err != nil {
		if errors.IsNotFound(err) {
			// ConfigMap doesn't exist yet - will be created by reconcileGeneratedConfigMap
			return false, nil
		}
		return false, err
	}

	// Check if ConfigMap is owned by this HomeAssistantConfiguration
	isOwned := false
	for _, ownerRef := range existingConfigMap.OwnerReferences {
		if ownerRef.UID == config.UID {
			isOwned = true
			break
		}
	}

	if !isOwned {
		// ConfigMap exists but is not owned by this CRD - don't touch it
		log.Info("ConfigMap exists but is not owned by this HomeAssistantConfiguration, skipping sync",
			"configMapName", configMapName)
		return false, nil
	}

	// Check if ConfigMap was modified externally (content mismatch)
	// NOTE: We only check content, NOT hash annotation.
	// Hash annotation is managed by performConfigReload() and should not be synced here.
	currentContent := existingConfigMap.Data[configurationYamlKey]

	if currentContent == canonicalContent {
		// ConfigMap content is in sync with CRD
		return false, nil
	}

	// ConfigMap was modified externally - restore from CRD
	// NOTE: We only restore the content (Data), NOT the hash annotation.
	// The hash annotation is ONLY updated during restart strategy in performConfigReload()
	// to explicitly trigger pod restart.
	log.Info("ConfigMap was modified externally, restoring from CRD state",
		"configMapName", configMapName,
		"currentContent", currentContent[:min(50, len(currentContent))],
		"expectedContent", canonicalContent[:min(50, len(canonicalContent))])

	existingConfigMap.Data[configurationYamlKey] = canonicalContent
	// DO NOT update annotation hash here - it's managed by performConfigReload() only

	if err := r.Update(ctx, existingConfigMap); err != nil {
		log.Error(err, "Failed to restore ConfigMap from CRD state")
		return false, err
	}

	log.Info("Successfully restored ConfigMap to CRD state", "configMapName", configMapName)
	return true, nil
}

// findHomeAssistantConfigurationForConfigMap finds the HomeAssistantConfiguration
// that owns a given ConfigMap
func (r *HomeAssistantConfigurationReconciler) findHomeAssistantConfigurationForConfigMap(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	configMap := obj.(*corev1.ConfigMap)

	// Check if this ConfigMap is owned by a HomeAssistantConfiguration
	for _, ownerRef := range configMap.OwnerReferences {
		if ownerRef.Kind == "HomeAssistantConfiguration" {
			// Reconcile the owning HomeAssistantConfiguration
			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      ownerRef.Name,
						Namespace: configMap.Namespace,
					},
				},
			}
		}
	}

	return []reconcile.Request{}
}

// validateHomeAssistantRef validates that referenced HomeAssistant exists
// and sets appropriate status condition if not found
func (r *HomeAssistantConfigurationReconciler) validateHomeAssistantRef(
	ctx context.Context,
	haRef types.NamespacedName,
	config *hav1alpha1.HomeAssistantConfiguration,
) (*hav1alpha1.HomeAssistant, error) {
	log := logf.FromContext(ctx)

	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "Referenced HomeAssistant not found",
				"name", haRef.Name)
			meta.SetStatusCondition(&config.Status.Conditions,
				metav1.Condition{
					Type:   conditionTypeReady,
					Status: metav1.ConditionFalse,
					Reason: reasonInvalidConfig,
					Message: fmt.Sprintf(
						"HomeAssistant %s not found",
						haRef.Name,
					),
					ObservedGeneration: config.Generation,
				})
			if err := r.Status().Update(ctx, config); err != nil {
				log.Error(err, "Failed to update status")
			}
			return nil, err
		}
		log.Error(err, "Failed to get HomeAssistant")
		return nil, err
	}
	return ha, nil
}

// isHomeAssistantServiceReady checks if the HomeAssistant Service has ready endpoints
// Returns true if service endpoints are available, false otherwise
// Uses the shared EndpointSlice-based helper to avoid deprecated Endpoints API
func (r *HomeAssistantConfigurationReconciler) isHomeAssistantServiceReady(
	ctx context.Context,
	ha *hav1alpha1.HomeAssistant,
) bool {
	return isServiceReadyFromEndpointSlices(ctx, r.Client, ha.Name, ha.Namespace)
}

// resolveRecorderDB returns the database URL for the recorder together with a flag
// indicating whether the value came from a K8s Secret reference (DatabaseSecretRef).
// When fromSecretRef is true the caller must store the URL in a separate K8s Secret
// (see reconcileRecorderDBSecret) instead of embedding it in the ConfigMap.
// Returns ("", false, nil) when neither Database nor DatabaseSecretRef is set.
func (r *HomeAssistantConfigurationReconciler) resolveRecorderDB(
	ctx context.Context,
	config *hav1alpha1.HomeAssistantConfiguration,
) (url string, fromSecretRef bool, err error) {
	rec := config.Spec.Recorder
	if rec == nil {
		return "", false, nil
	}
	if rec.DatabaseSecretRef != nil {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      rec.DatabaseSecretRef.Name,
			Namespace: config.Namespace,
		}, secret); err != nil {
			return "", false, fmt.Errorf("database secret %q: %w", rec.DatabaseSecretRef.Name, err)
		}
		key := rec.DatabaseSecretRef.Key
		if key == "" {
			key = "value"
		}
		val, ok := secret.Data[key]
		if !ok {
			return "", false, fmt.Errorf("database secret %q missing key %q", rec.DatabaseSecretRef.Name, key)
		}
		return string(val), true, nil
	}
	return rec.Database, false, nil
}

// reconcileRecorderDBSecret creates or updates the K8s Secret that holds the recorder
// database URL. The Secret is owned by the HAConfig CR and mounted by the HA pod at
// /config/recorder_db_url.yaml so that the URL is never embedded in the ConfigMap.
func (r *HomeAssistantConfigurationReconciler) reconcileRecorderDBSecret(
	ctx context.Context,
	config *hav1alpha1.HomeAssistantConfiguration,
	dbURL string,
) error {
	secretName := config.Spec.HomeAssistantRef.Name + recorderDBSecretSuffix
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: config.Namespace,
		},
		Data: map[string][]byte{
			"recorder_db_url.yaml": []byte(dbURL),
		},
	}
	if err := controllerutil.SetControllerReference(config, desired, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference on recorder-db secret: %w", err)
	}

	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: config.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if string(existing.Data["recorder_db_url.yaml"]) == dbURL {
		return nil
	}
	existing.Data = desired.Data
	return r.Update(ctx, existing)
}

// cleanupRecorderDBSecret deletes the recorder-db Secret if it exists.
// Called when databaseSecretRef is removed or the recorder is disabled so the
// orphaned Secret does not linger.
func (r *HomeAssistantConfigurationReconciler) cleanupRecorderDBSecret(
	ctx context.Context,
	config *hav1alpha1.HomeAssistantConfiguration,
) error {
	secret := &corev1.Secret{}
	secretName := config.Spec.HomeAssistantRef.Name + recorderDBSecretSuffix
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: config.Namespace}, secret)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, secret)
}

// buildConfigContent builds the final configuration.yaml content for the ConfigMap.
// It applies auto-includes, location injection, and recorder section injection.
// When spec.recorder.databaseSecretRef is set, the actual URL is stored in the
// <ha-name>-recorder-db K8s Secret (never in the ConfigMap) and the ConfigMap
// receives "!include recorder_db_url.yaml" instead.
func (r *HomeAssistantConfigurationReconciler) buildConfigContent(
	ctx context.Context,
	config *hav1alpha1.HomeAssistantConfiguration,
	ha *hav1alpha1.HomeAssistant,
) (string, error) {
	content := buildEffectiveConfig(config.Spec.Configuration, ha)
	rec := config.Spec.Recorder
	if rec == nil || (rec.Enabled != nil && !*rec.Enabled) {
		// No recorder injection — clean up any leftover recorder-db Secret.
		if err := r.cleanupRecorderDBSecret(ctx, config); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to clean up recorder-db secret (best-effort)")
		}
		return content, nil
	}

	dbURL, fromSecretRef, err := r.resolveRecorderDB(ctx, config)
	if err != nil {
		return "", err
	}

	if fromSecretRef {
		if err := r.reconcileRecorderDBSecret(ctx, config, dbURL); err != nil {
			return "", fmt.Errorf("reconcile recorder-db secret: %w", err)
		}
	} else {
		// Plain database string or empty — remove any leftover Secret from a previous
		// databaseSecretRef configuration.
		if err := r.cleanupRecorderDBSecret(ctx, config); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to clean up recorder-db secret (best-effort)")
		}
	}

	return injectRecorder(content, rec, dbURL, fromSecretRef)
}
