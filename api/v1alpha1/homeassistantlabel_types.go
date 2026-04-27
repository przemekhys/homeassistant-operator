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

// HomeAssistantLabelSpec defines the desired state of HomeAssistantLabel
type HomeAssistantLabelSpec struct {
	// homeAssistantRef is a reference to the HomeAssistant CR this label belongs to
	// +required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// name is the display name of the label in Home Assistant
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`

	// icon is the Material Design Icon for the label (e.g. "mdi:tag")
	// +optional
	Icon string `json:"icon,omitempty"`

	// color is the label color (e.g. "red", "blue", "green")
	// +optional
	Color string `json:"color,omitempty"`
}

// HomeAssistantLabelStatus defines the observed state of HomeAssistantLabel
type HomeAssistantLabelStatus struct {
	// labelID is the ID assigned by Home Assistant after creation
	// +optional
	LabelID string `json:"labelID,omitempty"`

	// observedGeneration is the most recent generation observed
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// lastError contains the error message from the last failed operation
	// Cleared when operation succeeds
	// +optional
	LastError string `json:"lastError,omitempty"`

	// conditions represent the current state of the HomeAssistantLabel resource
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=halabel;halb
// +kubebuilder:printcolumn:name="HomeAssistant",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.homeAssistantRef == oldSelf.spec.homeAssistantRef",message="spec.homeAssistantRef is immutable after creation"

// HomeAssistantLabel is the Schema for the homeassistantlabels API
type HomeAssistantLabel struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec HomeAssistantLabelSpec `json:"spec"`

	// +optional
	Status HomeAssistantLabelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantLabelList contains a list of HomeAssistantLabel
type HomeAssistantLabelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantLabel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantLabel{}, &HomeAssistantLabelList{})
}
