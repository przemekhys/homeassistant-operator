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
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

// getHomeAssistant retrieves a HomeAssistant CR by namespaced name
// This is a common helper used across multiple controllers
func getHomeAssistant(
	ctx context.Context,
	c client.Client,
	haRef types.NamespacedName,
) (*hav1.HomeAssistant, error) {
	ha := &hav1.HomeAssistant{}
	if err := c.Get(ctx, haRef, ha); err != nil {
		return nil, err
	}
	return ha, nil
}

// isServiceReadyFromEndpointSlices checks if a Service has ready endpoints using EndpointSlice API.
// Returns true if at least one ready endpoint address is found, false otherwise.
// This replaces deprecated corev1.Endpoints usage.
func isServiceReadyFromEndpointSlices(
	ctx context.Context,
	c client.Client,
	serviceName string,
	namespace string,
) bool {
	log := logf.FromContext(ctx)

	// List EndpointSlices for this service
	// EndpointSlices are labeled with kubernetes.io/service-name
	endpointSliceList := &discoveryv1.EndpointSliceList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{
			discoveryv1.LabelServiceName: serviceName,
		},
	}

	if err := c.List(ctx, endpointSliceList, listOpts...); err != nil {
		log.V(1).Info("Failed to list EndpointSlices", "error", err)
		return false
	}

	if len(endpointSliceList.Items) == 0 {
		log.V(1).Info("No EndpointSlices found for service")
		return false
	}

	// Check if any endpoint is ready
	for _, endpointSlice := range endpointSliceList.Items {
		for _, endpoint := range endpointSlice.Endpoints {
			// Check if endpoint is ready (conditions.ready == true)
			if endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready {
				// Check if endpoint has addresses
				if len(endpoint.Addresses) > 0 {
					log.V(1).Info("Service has ready endpoints",
						"endpointSlice", endpointSlice.Name,
						"addresses", len(endpoint.Addresses))
					return true
				}
			}
		}
	}

	log.V(1).Info("Service has no ready endpoints")
	return false
}

// calculateConfigHash computes SHA256 hash of the given content
// Used by multiple controllers to detect changes in ConfigMaps
func calculateConfigHash(content string) string {
	hash := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", hash)
}

// buildHomeAssistantURL builds the internal cluster URL for Home Assistant service
// Used by automation and scene controllers for hot-reload
func buildHomeAssistantURL(ha *hav1.HomeAssistant) string {
	// Use internal Kubernetes service DNS name
	// Format: <service-name>.<namespace>.svc.cluster.local:<port>
	serviceName := ha.Name
	port := defaultHomeAssistantPort
	if ha.Spec.Service != nil && ha.Spec.Service.Port != 0 {
		port = int(ha.Spec.Service.Port)
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", serviceName, ha.Namespace, port)
}

// getAPIToken retrieves the Home Assistant API token from Secret.
// Used by multiple controllers (backup, area, automation, configuration, floor,
// integration, label, scene, script) to retrieve the Home Assistant API token.
func getAPIToken(
	ctx context.Context,
	c client.Client,
	ha *hav1.HomeAssistant,
) (string, error) {
	// Keep the legacy token active until explicit migration verifies a replacement.
	migrating := dedicatedOperatorAccount(ha) && ha.Status.Bootstrap != nil &&
		ha.Status.Bootstrap.APITokenSecretName != "" &&
		ha.Status.Bootstrap.APITokenSecretName != ha.Spec.Bootstrap.OperatorAccount.TokenSecretName &&
		(ha.Status.Bootstrap.OperatorAccount == nil || ha.Status.Bootstrap.OperatorAccount.Phase != "Verified")
	if dedicatedOperatorAccount(ha) && !migrating {
		secret := &corev1.Secret{}
		err := c.Get(
			ctx,
			types.NamespacedName{Name: ha.Spec.Bootstrap.OperatorAccount.TokenSecretName, Namespace: ha.Namespace},
			secret,
		)
		if err != nil {
			reason := reasonHomeAssistantUnavailable
			if errors.IsNotFound(err) {
				reason = reasonCredentialMissing
			}
			if errors.IsForbidden(err) || errors.IsUnauthorized(err) {
				reason = reasonSecretAccessDenied
			}
			return "", &operatorCredentialError{reason: reason}
		}
		if err := validateOperatorSecret(ha, secret); err != nil {
			return "", err
		}
		if secret.Annotations[operatorStageAnnotation] != "Verified" ||
			secret.Annotations[operatorUserAnnotation] == "" {
			return "", &operatorCredentialError{reason: reasonCredentialMalformed}
		}
		token := string(secret.Data[apiTokenSecretKeyName])
		if strings.TrimSpace(token) == "" || strings.TrimSpace(token) != token ||
			strings.ContainsAny(token, "\r\n\t ") {
			return "", &operatorCredentialError{reason: reasonCredentialMalformed}
		}
		return token, nil
	}
	log := logf.FromContext(ctx)

	// Derive token secret name: prefer status field set by bootstrap controller,
	// fall back to the conventional default so callers work before bootstrap completes.
	tokenSecretName := ha.Name + "-homeassistant-api-token"
	if ha.Status.Bootstrap != nil && ha.Status.Bootstrap.APITokenSecretName != "" {
		tokenSecretName = ha.Status.Bootstrap.APITokenSecretName
	} else {
		log.V(1).
			Info("Bootstrap API token secret name not set, using default token secret name", "secret", tokenSecretName)
	}

	if ha.Spec.Bootstrap != nil && ha.Spec.Bootstrap.OperatorAccount != nil &&
		ha.Spec.Bootstrap.OperatorAccount.Mode == "legacy" && ha.Spec.Bootstrap.APITokenSecretName != "" {
		// Explicit rollback selects the retained legacy Secret even when status
		// still describes the previously verified dedicated credential.
		tokenSecretName = ha.Spec.Bootstrap.APITokenSecretName
	}
	tokenSecret := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{Name: tokenSecretName, Namespace: ha.Namespace}, tokenSecret)

	if err != nil {
		if errors.IsNotFound(err) {
			log.V(1).Info("API token secret not found, cannot perform hot-reload", "secret", tokenSecretName)
			return "", err
		}
		return "", err
	}

	tokenBytes, ok := tokenSecret.Data["token"]
	if !ok {
		log.Error(fmt.Errorf("missing token key"), "API token secret missing key", "secret", tokenSecretName)
		return "", fmt.Errorf("API token secret missing key 'token'")
	}

	return string(tokenBytes), nil
}

func dedicatedOperatorAccount(ha *hav1.HomeAssistant) bool {
	return ha.Spec.Bootstrap != nil && ha.Spec.Bootstrap.OperatorAccount != nil &&
		ha.Spec.Bootstrap.OperatorAccount.Mode == "dedicated"
}

// operatorCredentialError deliberately contains no API response or credential data.
type operatorCredentialError struct{ reason string }

func (e *operatorCredentialError) Error() string { return "operator credential: " + e.reason }

func validateOperatorSecret(ha *hav1.HomeAssistant, secret *corev1.Secret) error {
	switch secret.Annotations[operatorStageAnnotation] {
	case "Provisioning", "Creating", "Issuing", "Recovering", "Verified":
	default:
		return &operatorCredentialError{reason: reasonCredentialMalformed}
	}
	if len(secret.Data[apiTokenSecretKeyName]) > 0 && secret.Annotations[operatorUserAnnotation] == "" {
		return &operatorCredentialError{reason: reasonCredentialMalformed}
	}
	owner := metav1.GetControllerOf(secret)
	if secret.Namespace != ha.Namespace || owner == nil || owner.UID != ha.UID || owner.Name != ha.Name ||
		owner.Kind != "HomeAssistant" ||
		owner.APIVersion != hav1.GroupVersion.String() ||
		secret.Type != corev1.SecretTypeOpaque ||
		secret.Labels["app.kubernetes.io/managed-by"] != "homeassistant-operator" ||
		secret.Labels[operatorPurposeLabel] != "operator-api-token" ||
		len(secret.Data) > 1 {
		return &operatorCredentialError{reason: reasonCredentialMalformed}
	}
	return nil
}
