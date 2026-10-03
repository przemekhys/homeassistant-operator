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

// ConfigMapKeyReference identifies one dashboard definition in a ConfigMap in
// the same namespace as the HomeAssistantDashboard.
type ConfigMapKeyReference struct {
	// Name is the name of the ConfigMap.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the ConfigMap data key containing the complete dashboard YAML document.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// HomeAssistantDashboardSpec defines the desired state of a storage-mode
// Home Assistant Lovelace dashboard.
type HomeAssistantDashboardSpec struct {
	// HomeAssistantRef references the HomeAssistant instance that owns this dashboard.
	// +kubebuilder:validation:Required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// Title is the title displayed for the dashboard.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Title string `json:"title"`

	// URLPath is the unique Home Assistant path for a named dashboard. It is
	// omitted when DefaultDashboard explicitly selects the default dashboard.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+(?:-[a-z0-9]+)*$`
	// +optional
	URLPath string `json:"urlPath,omitempty"`

	// DefaultDashboard explicitly selects Home Assistant's default storage dashboard.
	// +kubebuilder:default=false
	// +optional
	DefaultDashboard bool `json:"defaultDashboard,omitempty"`

	// Icon is an optional Material Design Icons icon displayed in the sidebar.
	// +optional
	Icon string `json:"icon,omitempty"`

	// ShowInSidebar controls whether Home Assistant displays this dashboard in its sidebar.
	// +kubebuilder:default=true
	// +optional
	ShowInSidebar *bool `json:"showInSidebar,omitempty"`

	// RequireAdmin restricts the dashboard to Home Assistant administrators.
	// +kubebuilder:default=false
	// +optional
	RequireAdmin *bool `json:"requireAdmin,omitempty"`

	// Inline is the complete YAML dashboard definition.
	// Exactly one of Inline and ConfigMapKeyRef is required.
	// +optional
	Inline string `json:"inline,omitempty"`

	// ConfigMapKeyRef selects the ConfigMap key containing the complete YAML
	// dashboard definition. The ConfigMap must be in this resource's namespace.
	// Exactly one of Inline and ConfigMapKeyRef is required.
	// +optional
	ConfigMapKeyRef *ConfigMapKeyReference `json:"configMapKeyRef,omitempty"`
}

// HomeAssistantDashboardStatus defines the observed state of HomeAssistantDashboard.
type HomeAssistantDashboardStatus struct {
	// DashboardID is Home Assistant's identifier for the dashboard.
	// +optional
	DashboardID string `json:"dashboardID,omitempty"`

	// URLPath is the effective Home Assistant dashboard path, including lovelace
	// for the default dashboard.
	// +optional
	URLPath string `json:"urlPath,omitempty"`

	// SourceHash is the hash of the dashboard definition and metadata last saved to Home Assistant.
	// +optional
	SourceHash string `json:"sourceHash,omitempty"`

	// LastError contains the most recent reconciliation error and is cleared after success.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// ObservedGeneration reflects the generation most recently reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the dashboard state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hadashboard,categories=homeassistant
// +kubebuilder:printcolumn:name="HomeAssistant",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Path",type=string,JSONPath=`.spec.urlPath`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.spec.title`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.homeAssistantRef == oldSelf.spec.homeAssistantRef",message="spec.homeAssistantRef is immutable after creation"
// +kubebuilder:validation:XValidation:rule="has(self.spec.inline) != has(self.spec.configMapKeyRef)",message="exactly one of spec.inline or spec.configMapKeyRef must be specified"
// +kubebuilder:validation:XValidation:rule="self.spec.defaultDashboard ? !has(self.spec.urlPath) : has(self.spec.urlPath)",message="spec.defaultDashboard requires omitting spec.urlPath, and named dashboards require spec.urlPath"
// +kubebuilder:validation:XValidation:rule="self.spec.defaultDashboard || self.spec.urlPath != 'lovelace'",message="spec.urlPath lovelace requires spec.defaultDashboard"

// HomeAssistantDashboard is the Schema for a storage-mode Home Assistant Lovelace dashboard.
type HomeAssistantDashboard struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantDashboardSpec   `json:"spec,omitempty"`
	Status HomeAssistantDashboardStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantDashboardList contains a list of HomeAssistantDashboard.
type HomeAssistantDashboardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantDashboard `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantDashboard{}, &HomeAssistantDashboardList{})
}
