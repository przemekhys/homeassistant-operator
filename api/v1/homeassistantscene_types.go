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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SceneEntity represents a single entity (device) in a scene with its desired state
type SceneEntity struct {
	// EntityID is the Home Assistant entity identifier in format domain.object_id
	// Examples: light.living_room, switch.fan, climate.bedroom
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z_]+\.[a-z0-9_]+$`
	EntityID string `json:"entity_id"`

	// State is the desired state for this entity
	// Examples: "on", "off", numeric values
	// +kubebuilder:validation:Required
	State string `json:"state"`

	// Attributes contains additional entity-specific attributes
	// Examples: brightness, color_temp, rgb_color for lights
	// This is a flexible structure that accepts any valid Home Assistant entity attributes
	// +optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Attributes runtime.RawExtension `json:"attributes,omitempty"`
}

// HomeAssistantSceneSpec defines the desired state of HomeAssistantScene
type HomeAssistantSceneSpec struct {
	// HomeAssistantRef references the HomeAssistant CR that will use this scene
	// +kubebuilder:validation:Required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// ID is a unique identifier for the scene (used by Home Assistant)
	// If not specified, will be auto-generated from the CR name
	// Must contain only lowercase letters, digits, and underscores. Existing
	// resources with an id predating this constraint (uppercase letters or
	// hyphens) keep working until their next update, which will then be
	// rejected until id is renamed to a conforming value.
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_]*$`
	// +optional
	ID string `json:"id,omitempty"`

	// Name is a user-friendly name for the scene (displayed in Home Assistant UI)
	// If not specified, the CR name will be used
	// +kubebuilder:validation:MinLength=1
	// +optional
	Name string `json:"name,omitempty"`

	// Icon is a Material Design icon for the scene (e.g., "mdi:movie", "mdi:candle")
	// See https://mdi.bessarabov.com/ for available icons
	// +optional
	Icon string `json:"icon,omitempty"`

	// Entities define the list of devices and their states to set when scene is activated
	// At least one entity is required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Entities []SceneEntity `json:"entities"`

	// AutoReload enables automatic hot-reload when scene configuration changes
	// If false, requires manual reload or pod restart
	// +kubebuilder:default=true
	// +optional
	AutoReload *bool `json:"autoReload,omitempty"`
}

// HomeAssistantSceneStatus defines the observed state of HomeAssistantScene
type HomeAssistantSceneStatus struct {
	// Conditions represent the latest available observations of the HomeAssistantScene state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// SceneHash is the SHA256 hash of the current scene configuration
	// Used to detect changes and determine if reload is needed
	// +optional
	SceneHash string `json:"sceneHash,omitempty"`

	// LastReloadTime is the timestamp of the last successful reload
	// +optional
	LastReloadTime *metav1.Time `json:"lastReloadTime,omitempty"`

	// LastActivated is the timestamp when the scene was last activated (if available from HA)
	// +optional
	LastActivated *metav1.Time `json:"lastActivated,omitempty"`

	// LastError contains the error message from the last failed operation
	// Cleared when operation succeeds
	// +optional
	LastError string `json:"lastError,omitempty"`

	// EntityCount is the number of entities defined in the scene
	// Updated by the controller for display in kubectl output
	// +optional
	EntityCount int32 `json:"entityCount,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed HomeAssistantScene
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ObservedSynchronizationEpoch forces a fresh observation after a coordinated resume.
	// +optional
	ObservedSynchronizationEpoch int64 `json:"observedSynchronizationEpoch,omitempty"`
}

// +kubebuilder:storageversion
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hascene;hasc,categories=homeassistant
// +kubebuilder:printcolumn:name="HomeAssistant",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Entities",type=integer,JSONPath=`.status.entityCount`,description="Number of entities"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.homeAssistantRef == oldSelf.spec.homeAssistantRef",message="spec.homeAssistantRef is immutable after creation"

// HomeAssistantScene is the Schema for the homeassistantscenes API
type HomeAssistantScene struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantSceneSpec   `json:"spec,omitempty"`
	Status HomeAssistantSceneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantSceneList contains a list of HomeAssistantScene
type HomeAssistantSceneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantScene `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantScene{}, &HomeAssistantSceneList{})
}
