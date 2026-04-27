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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SecretKeyReference defines a reference to a specific key in a Kubernetes Secret.
type SecretKeyReference struct {
	// Name of the Secret
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Keys to extract from the Secret. If empty, all keys will be included.
	// +kubebuilder:validation:Optional
	Keys []string `json:"keys,omitempty"`
}

// HomeAssistantSecretsSpec defines the desired state of HomeAssistantSecrets.
type HomeAssistantSecretsSpec struct {
	// HomeAssistantRef references the HomeAssistant CR that will use these secrets
	// +kubebuilder:validation:Required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// SecretRefs is a list of references to Kubernetes Secrets.
	// Keys from these Secrets will be merged into the generated secrets.yaml
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	SecretRefs []SecretKeyReference `json:"secretRefs"`

	// AutoRestart controls whether the Home Assistant pod should be automatically
	// restarted when secrets change. When enabled, the controller updates an annotation
	// on the StatefulSet to trigger a rolling restart.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	AutoRestart *bool `json:"autoRestart,omitempty"`
}

// HomeAssistantReference references a HomeAssistant CR.
type HomeAssistantReference struct {
	// Name of the HomeAssistant resource
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// HomeAssistantSecretsStatus defines the observed state of HomeAssistantSecrets.
type HomeAssistantSecretsStatus struct {
	// Conditions represent the latest available observations of the HomeAssistantSecrets state
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// SecretsHash is the SHA256 hash of the generated secrets.yaml content.
	// Used to detect changes and trigger pod restarts.
	// +kubebuilder:validation:Optional
	SecretsHash string `json:"secretsHash,omitempty"`

	// LastUpdated is the timestamp when the secrets were last updated
	// +kubebuilder:validation:Optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed HomeAssistantSecrets
	// +kubebuilder:validation:Optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hasecrets;hasec
// +kubebuilder:printcolumn:name="HomeAssistant",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Secrets",type=string,JSONPath=`.spec.secretRefs[*].name`,description="Referenced secrets"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.homeAssistantRef == oldSelf.spec.homeAssistantRef",message="spec.homeAssistantRef is immutable after creation"

// HomeAssistantSecrets is the Schema for the homeassistantsecrets API.
type HomeAssistantSecrets struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantSecretsSpec   `json:"spec,omitempty"`
	Status HomeAssistantSecretsStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantSecretsList contains a list of HomeAssistantSecrets.
type HomeAssistantSecretsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantSecrets `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantSecrets{}, &HomeAssistantSecretsList{})
}
