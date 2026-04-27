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

// HomeAssistantFloorSpec defines the desired state of HomeAssistantFloor
type HomeAssistantFloorSpec struct {
	// homeAssistantRef is a reference to the HomeAssistant CR this floor belongs to
	// +required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// name is the display name of the floor in Home Assistant
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`

	// level is the floor level (e.g. 0 for ground, 1 for first, -1 for basement)
	// +optional
	Level *int `json:"level,omitempty"`

	// icon is the Material Design Icon for the floor (e.g. "mdi:home-floor-1")
	// +optional
	Icon string `json:"icon,omitempty"`
}

// HomeAssistantFloorStatus defines the observed state of HomeAssistantFloor
type HomeAssistantFloorStatus struct {
	// floorID is the ID assigned by Home Assistant after creation
	// +optional
	FloorID string `json:"floorID,omitempty"`

	// observedGeneration is the most recent generation observed
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// lastError contains the error message from the last failed operation
	// Cleared when operation succeeds
	// +optional
	LastError string `json:"lastError,omitempty"`

	// conditions represent the current state of the HomeAssistantFloor resource
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hafloor;hafl
// +kubebuilder:printcolumn:name="HomeAssistant",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.homeAssistantRef == oldSelf.spec.homeAssistantRef",message="spec.homeAssistantRef is immutable after creation"

// HomeAssistantFloor is the Schema for the homeassistantfloors API
type HomeAssistantFloor struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec HomeAssistantFloorSpec `json:"spec"`

	// +optional
	Status HomeAssistantFloorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantFloorList contains a list of HomeAssistantFloor
type HomeAssistantFloorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantFloor `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantFloor{}, &HomeAssistantFloorList{})
}
