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
	"fmt"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	appsv1 "k8s.io/api/apps/v1"
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
)

const (
	secretsYamlKey           = "secrets.yaml"
	generatedSecretSuffix    = "-generated-secrets"
	secretsHashAnnotationKey = "ha.homeassistant.io/secrets-hash"

	// Condition reasons
	reasonSecretsGenerated      = "SecretsGenerated"
	reasonSecretNotFound        = "SecretNotFound"
	reasonInvalidConfiguration  = "InvalidConfiguration"
	reasonReconciliationFailed  = "ReconciliationFailed"
	reasonReconciliationSuccess = "ReconciliationSucceeded"
)

// HomeAssistantSecretsReconciler reconciles a HomeAssistantSecrets object
type HomeAssistantSecretsReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantsecrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantsecrets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantsecrets/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *HomeAssistantSecretsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the HomeAssistantSecrets instance
	haSecrets := &hav1alpha1.HomeAssistantSecrets{}
	if err := r.Get(ctx, req.NamespacedName, haSecrets); err != nil {
		if errors.IsNotFound(err) {
			log.Info("HomeAssistantSecrets resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get HomeAssistantSecrets")
		return ctrl.Result{}, err
	}

	// Validate HomeAssistant reference exists
	haRef := types.NamespacedName{
		Name:      haSecrets.Spec.HomeAssistantRef.Name,
		Namespace: haSecrets.Namespace,
	}
	ha := &hav1alpha1.HomeAssistant{}
	if err := r.Get(ctx, haRef, ha); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "Referenced HomeAssistant not found", "name", haRef.Name)
			haSecrets.Status.ObservedGeneration = haSecrets.Generation
			meta.SetStatusCondition(&haSecrets.Status.Conditions, metav1.Condition{
				Type:               conditionTypeReady,
				Status:             metav1.ConditionFalse,
				Reason:             reasonInvalidConfiguration,
				Message:            fmt.Sprintf("HomeAssistant %s not found", haRef.Name),
				ObservedGeneration: haSecrets.Generation,
			})
			if err := r.Status().Update(ctx, haSecrets); err != nil {
				log.Error(err, "Failed to update status")
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		log.Error(err, "Failed to get HomeAssistant")
		return ctrl.Result{}, err
	}

	// Collect secrets from all referenced sources
	secretsData, err := r.collectSecrets(ctx, haSecrets)
	if err != nil {
		log.Error(err, "Failed to collect secrets")
		haSecrets.Status.ObservedGeneration = haSecrets.Generation
		meta.SetStatusCondition(&haSecrets.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             reasonReconciliationFailed,
			Message:            fmt.Sprintf("Failed to collect secrets: %v", err),
			ObservedGeneration: haSecrets.Generation,
		})
		if statusErr := r.Status().Update(ctx, haSecrets); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
		}
		return ctrl.Result{}, err
	}

	// Generate secrets.yaml content
	secretsYaml := r.generateSecretsYaml(secretsData)

	// Calculate hash of the secrets
	hash := calculateHash(secretsYaml)

	// Create or update the generated Secret
	if err := r.reconcileGeneratedSecret(ctx, haSecrets, secretsYaml, hash); err != nil {
		log.Error(err, "Failed to reconcile generated secret")
		haSecrets.Status.ObservedGeneration = haSecrets.Generation
		meta.SetStatusCondition(&haSecrets.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             reasonReconciliationFailed,
			Message:            fmt.Sprintf("Failed to create/update generated secret: %v", err),
			ObservedGeneration: haSecrets.Generation,
		})
		if statusErr := r.Status().Update(ctx, haSecrets); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
		}
		return ctrl.Result{}, err
	}

	// Trigger pod restart if autoRestart is enabled and hash changed
	if r.isAutoRestartEnabled(haSecrets) {
		if err := r.updateStatefulSetAnnotation(ctx, haSecrets, hash); err != nil {
			log.Error(err, "Failed to update StatefulSet annotation for restart")
			// Don't fail reconciliation - secrets are updated, restart is best-effort
		}
	}

	// Update status
	now := metav1.Now()
	haSecrets.Status.SecretsHash = hash
	haSecrets.Status.LastUpdated = &now
	haSecrets.Status.ObservedGeneration = haSecrets.Generation
	meta.SetStatusCondition(&haSecrets.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             reasonSecretsGenerated,
		Message:            "Secrets successfully generated",
		ObservedGeneration: haSecrets.Generation,
	})

	if err := r.Status().Update(ctx, haSecrets); err != nil {
		log.Error(err, "Failed to update HomeAssistantSecrets status")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled HomeAssistantSecrets")
	return ctrl.Result{}, nil
}

// collectSecrets gathers all secrets from the referenced Secret resources.
func (r *HomeAssistantSecretsReconciler) collectSecrets(
	ctx context.Context,
	haSecrets *hav1alpha1.HomeAssistantSecrets,
) (map[string]string, error) {
	log := logf.FromContext(ctx)
	secretsData := make(map[string]string)

	for _, secretRef := range haSecrets.Spec.SecretRefs {
		secret := &corev1.Secret{}
		secretName := types.NamespacedName{
			Name:      secretRef.Name,
			Namespace: haSecrets.Namespace,
		}

		if err := r.Get(ctx, secretName, secret); err != nil {
			if errors.IsNotFound(err) {
				log.Error(err, "Referenced secret not found", "secret", secretRef.Name)
				return nil, fmt.Errorf("secret %s not found", secretRef.Name)
			}
			return nil, err
		}

		// If specific keys are specified, only include those
		if len(secretRef.Keys) > 0 {
			for _, key := range secretRef.Keys {
				if value, ok := secret.Data[key]; ok {
					secretsData[key] = string(value)
				} else {
					log.Info("Key not found in secret, skipping", "secret", secretRef.Name, "key", key)
				}
			}
		} else {
			// Include all keys from the secret
			for key, value := range secret.Data {
				secretsData[key] = string(value)
			}
		}
	}

	return secretsData, nil
}

// generateSecretsYaml creates a YAML representation of the secrets
func (r *HomeAssistantSecretsReconciler) generateSecretsYaml(secretsData map[string]string) string {
	if len(secretsData) == 0 {
		return "# No secrets configured\n"
	}

	// Sort keys for consistent output
	keys := make([]string, 0, len(secretsData))
	for k := range secretsData {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build ordered YAML using yaml.Node to preserve key order
	mapNode := &yaml.Node{
		Kind: yaml.MappingNode,
	}
	for _, key := range keys {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
		valueNode := &yaml.Node{Kind: yaml.ScalarNode, Value: secretsData[key]}
		mapNode.Content = append(mapNode.Content, keyNode, valueNode)
	}

	yamlBytes, err := yaml.Marshal(mapNode)
	if err != nil {
		// Fallback to empty if marshal fails (shouldn't happen with string map)
		return "# Error generating secrets\n"
	}

	header := "# Auto-generated by HomeAssistantSecrets controller\n" +
		"# DO NOT EDIT MANUALLY - changes will be overwritten\n\n"
	return header + string(yamlBytes)
}

// reconcileGeneratedSecret creates or updates the generated Secret
// containing secrets.yaml.
func (r *HomeAssistantSecretsReconciler) reconcileGeneratedSecret(
	ctx context.Context,
	haSecrets *hav1alpha1.HomeAssistantSecrets,
	secretsYaml,
	hash string,
) error {
	log := logf.FromContext(ctx)

	secretName := haSecrets.Spec.HomeAssistantRef.Name + generatedSecretSuffix
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: haSecrets.Namespace,
			Labels: map[string]string{
				labelAppName:      "homeassistant",
				labelAppInstance:  haSecrets.Spec.HomeAssistantRef.Name,
				labelAppManagedBy: "homeassistant-operator",
			},
			Annotations: map[string]string{
				secretsHashAnnotationKey: hash,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			secretsYamlKey: []byte(secretsYaml),
		},
	}

	// Set HomeAssistantSecrets as the owner
	if err := controllerutil.SetControllerReference(haSecrets, secret, r.Scheme); err != nil {
		return err
	}

	// Check if secret already exists
	existingSecret := &corev1.Secret{}
	secretKey := types.NamespacedName{Name: secretName, Namespace: haSecrets.Namespace}
	err := r.Get(ctx, secretKey, existingSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create new secret
			log.Info("Creating new generated secret", "name", secretName)
			if err := r.Create(ctx, secret); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	// Update existing secret if hash changed
	if existingSecret.Annotations[secretsHashAnnotationKey] != hash {
		oldHash := existingSecret.Annotations[secretsHashAnnotationKey]
		log.Info("Updating generated secret (hash changed)",
			"name", secretName,
			"oldHash", oldHash,
			"newHash", hash)
		existingSecret.Data = secret.Data
		existingSecret.Annotations[secretsHashAnnotationKey] = hash
		if err := r.Update(ctx, existingSecret); err != nil {
			return err
		}
	}

	return nil
}

// calculateHash computes SHA256 hash of the given content
func calculateHash(content string) string {
	hash := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", hash)
}

// SetupWithManager sets up the controller with the Manager.
func (r *HomeAssistantSecretsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapFn := handler.EnqueueRequestsFromMapFunc(
		r.findHomeAssistantSecretsForSecret,
	)
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1alpha1.HomeAssistantSecrets{}).
		Owns(&corev1.Secret{}).
		Watches(
			&corev1.Secret{},
			mapFn,
		).
		Named("homeassistantsecrets").
		Complete(r)
}

// findHomeAssistantSecretsForSecret finds all HomeAssistantSecrets
// that reference a given Secret.
func (r *HomeAssistantSecretsReconciler) findHomeAssistantSecretsForSecret(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	secret := obj.(*corev1.Secret)

	// List all HomeAssistantSecrets in the same namespace
	haSecretsList := &hav1alpha1.HomeAssistantSecretsList{}
	if err := r.List(
		ctx, haSecretsList,
		client.InNamespace(secret.Namespace),
	); err != nil {
		return []reconcile.Request{}
	}

	var requests []reconcile.Request
	for _, haSecrets := range haSecretsList.Items {
		// Check if this HomeAssistantSecrets references the changed
		// Secret
		for _, secretRef := range haSecrets.Spec.SecretRefs {
			if secretRef.Name == secret.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      haSecrets.Name,
						Namespace: haSecrets.Namespace,
					},
				})
				break
			}
		}
	}

	return requests
}

// isAutoRestartEnabled returns true if autoRestart is enabled (default: true)
func (r *HomeAssistantSecretsReconciler) isAutoRestartEnabled(haSecrets *hav1alpha1.HomeAssistantSecrets) bool {
	if haSecrets.Spec.AutoRestart == nil {
		return true // default is enabled
	}
	return *haSecrets.Spec.AutoRestart
}

// updateStatefulSetAnnotation updates the secrets hash annotation on
// the StatefulSet to trigger a rolling restart when secrets change.
func (r *HomeAssistantSecretsReconciler) updateStatefulSetAnnotation(
	ctx context.Context,
	haSecrets *hav1alpha1.HomeAssistantSecrets,
	hash string,
) error {
	log := logf.FromContext(ctx)

	// StatefulSet has the same name as the HomeAssistant CR
	stsName := types.NamespacedName{
		Name:      haSecrets.Spec.HomeAssistantRef.Name,
		Namespace: haSecrets.Namespace,
	}

	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, stsName, sts); err != nil {
		if errors.IsNotFound(err) {
			log.Info("StatefulSet not found, skipping annotation update", "name", stsName.Name)
			return nil
		}
		return err
	}

	// Check if annotation needs update
	currentHash := ""
	if sts.Spec.Template.Annotations != nil {
		currentHash = sts.Spec.Template.Annotations[secretsHashAnnotationKey]
	}

	if currentHash == hash {
		log.V(1).Info("StatefulSet annotation already up to date", "hash", hash)
		return nil
	}

	// Update pod template annotation to trigger rolling restart
	if sts.Spec.Template.Annotations == nil {
		sts.Spec.Template.Annotations = make(map[string]string)
	}
	sts.Spec.Template.Annotations[secretsHashAnnotationKey] = hash

	log.Info("Updating StatefulSet annotation to trigger pod restart",
		"statefulset", stsName.Name,
		"oldHash", currentHash,
		"newHash", hash)

	if err := r.Update(ctx, sts); err != nil {
		return fmt.Errorf("failed to update StatefulSet: %w", err)
	}

	return nil
}
