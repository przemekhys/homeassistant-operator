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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HomeAssistantIntegrationSpec defines the desired state of HomeAssistantIntegration
type HomeAssistantIntegrationSpec struct {
	// HomeAssistantRef references the HomeAssistant instance to configure
	// +kubebuilder:validation:Required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// Domain is the integration name in Home Assistant (e.g. "mqtt", "esphome", "recorder")
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Domain string `json:"domain"`

	// Configuration contains fields submitted to the Config Flow (single-step flows only).
	// Keys are field names from the data_schema; values are plain text or Secret references.
	// +optional
	Configuration map[string]IntegrationValue `json:"configuration,omitempty"`
}

// IntegrationValue holds a plain text value, a JSON value, or a reference to a Kubernetes Secret key.
// Exactly one of Value, JSONValue, or SecretKeyRef must be set.
// +kubebuilder:validation:XValidation:rule="has(self.value) || has(self.jsonValue) || has(self.secretKeyRef)",message="at least one of value, jsonValue, or secretKeyRef must be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.value) && has(self.jsonValue)) && !(has(self.value) && has(self.secretKeyRef)) && !(has(self.jsonValue) && has(self.secretKeyRef))",message="only one of value, jsonValue, or secretKeyRef may be set"
type IntegrationValue struct {
	// Value is a plain text configuration value sent as a string to the Config Flow API.
	// +optional
	Value *string `json:"value,omitempty"`

	// JSONValue is a JSON-encoded value that will be parsed and sent as a native JSON
	// object to the Config Flow API. Use this for fields that expect a dictionary or
	// array (e.g. location: '{"latitude": 54.17, "longitude": 18.55}').
	// +optional
	JSONValue *string `json:"jsonValue,omitempty"`

	// SecretKeyRef references a key in a Kubernetes Secret
	// +optional
	SecretKeyRef *IntegrationSecretKeyRef `json:"secretKeyRef,omitempty"`
}

// IntegrationSecretKeyRef references a specific key within a Kubernetes Secret
type IntegrationSecretKeyRef struct {
	// Name of the Secret
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Key within the Secret
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// HomeAssistantIntegrationStatus defines the observed state of HomeAssistantIntegration
type HomeAssistantIntegrationStatus struct {
	// EntryID is the Home Assistant config entry ID created or adopted by the Config Flow
	// +optional
	EntryID string `json:"entryID,omitempty"`

	// ConfigHash is the SHA256 hash of the resolved configuration values.
	// Used to detect spec changes and trigger reconfiguration (delete + re-create).
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// Conditions represent the latest available observations of the integration state
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastError contains the error message from the last failed operation
	// Cleared when operation succeeds
	// +optional
	LastError string `json:"lastError,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed CR
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=haint;haintegration,categories=homeassistant
// +kubebuilder:printcolumn:name="HomeAssistant",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.domain`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.homeAssistantRef == oldSelf.spec.homeAssistantRef",message="spec.homeAssistantRef is immutable after creation"

// HomeAssistantIntegration manages a Home Assistant integration (config entry) via the Config Flow API.
// It supports create, adopt, reconfigure (delete+re-create on spec change), and cleanup via finalizer.
// Only single-step Config Flows are supported (e.g. mqtt, recorder, esphome).
type HomeAssistantIntegration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantIntegrationSpec   `json:"spec,omitempty"`
	Status HomeAssistantIntegrationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantIntegrationList contains a list of HomeAssistantIntegration
type HomeAssistantIntegrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantIntegration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantIntegration{}, &HomeAssistantIntegrationList{})
}
