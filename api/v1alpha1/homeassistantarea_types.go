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

// HomeAssistantAreaSpec defines the desired state of HomeAssistantArea
type HomeAssistantAreaSpec struct {
	// homeAssistantRef is a reference to the HomeAssistant CR this area belongs to
	// +required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// name is the display name of the area in Home Assistant
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`

	// floorName is the name of the HomeAssistantFloor CR to assign this area to (resolved at reconcile time)
	// +optional
	FloorName string `json:"floorName,omitempty"`

	// icon is the Material Design Icon for the area (e.g. "mdi:sofa")
	// +optional
	Icon string `json:"icon,omitempty"`

	// labels is a list of HomeAssistantLabel CR names to assign to this area (resolved at reconcile time)
	// +optional
	Labels []string `json:"labels,omitempty"`
}

// HomeAssistantAreaStatus defines the observed state of HomeAssistantArea
type HomeAssistantAreaStatus struct {
	// areaID is the ID assigned by Home Assistant after creation
	// +optional
	AreaID string `json:"areaID,omitempty"`

	// observedGeneration is the most recent generation observed
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// lastError contains the error message from the last failed operation
	// Cleared when operation succeeds
	// +optional
	LastError string `json:"lastError,omitempty"`

	// conditions represent the current state of the HomeAssistantArea resource
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=homeassistantareas,shortName=haarea;haar
// +kubebuilder:printcolumn:name="HomeAssistant",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Floor",type=string,JSONPath=`.spec.floorName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.homeAssistantRef == oldSelf.spec.homeAssistantRef",message="spec.homeAssistantRef is immutable after creation"

// HomeAssistantArea is the Schema for the homeassistantareas API
type HomeAssistantArea struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec HomeAssistantAreaSpec `json:"spec"`

	// +optional
	Status HomeAssistantAreaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantAreaList contains a list of HomeAssistantArea
type HomeAssistantAreaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantArea `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantArea{}, &HomeAssistantAreaList{})
}
