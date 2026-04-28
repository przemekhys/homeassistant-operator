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

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

// getHomeAssistant retrieves a HomeAssistant CR by namespaced name
// This is a common helper used across multiple controllers
func getHomeAssistant(
	ctx context.Context,
	c client.Client,
	haRef types.NamespacedName,
) (*hav1alpha1.HomeAssistant, error) {
	ha := &hav1alpha1.HomeAssistant{}
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
func buildHomeAssistantURL(ha *hav1alpha1.HomeAssistant) string {
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
	ha *hav1alpha1.HomeAssistant,
) (string, error) {
	log := logf.FromContext(ctx)

	// Derive token secret name: prefer status field set by bootstrap controller,
	// fall back to the conventional default so callers work before bootstrap completes.
	tokenSecretName := ha.Name + "-homeassistant-api-token"
	if ha.Status.Bootstrap != nil && ha.Status.Bootstrap.APITokenSecretName != "" {
		tokenSecretName = ha.Status.Bootstrap.APITokenSecretName
	} else {
		log.V(1).Info("Bootstrap API token secret name not set, using default token secret name", "secret", tokenSecretName)
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
