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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CommunityRepositoryCategory is a HACS repository category. Values match the
// hacs.json category field exactly.
// +kubebuilder:validation:Enum=integration;plugin;theme;python_script;template
type CommunityRepositoryCategory string

const (
	CategoryIntegration  CommunityRepositoryCategory = "integration"
	CategoryPlugin       CommunityRepositoryCategory = "plugin"
	CategoryTheme        CommunityRepositoryCategory = "theme"
	CategoryPythonScript CommunityRepositoryCategory = "python_script"
	CategoryTemplate     CommunityRepositoryCategory = "template"
)

// HomeAssistantCommunityRepositorySpec defines the desired state of a
// HomeAssistantCommunityRepository.
type HomeAssistantCommunityRepositorySpec struct {
	// HomeAssistantRef references the HomeAssistant instance to install this repository into.
	// +kubebuilder:validation:Required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// Category is the HACS repository category. appdaemon and netdaemon require a
	// separate runtime this operator does not deploy.
	// +kubebuilder:validation:Required
	Category CommunityRepositoryCategory `json:"category"`

	// Repository is the GitHub "owner/repo" shorthand, not a full URL.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[\w.-]+/[\w.-]+$`
	Repository string `json:"repository"`

	// Ref is the explicit tag, branch, or commit SHA to install.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[\w][\w.\-/]*$`
	Ref string `json:"ref"`
}

// CommunityRepositoryPhase is the reconciliation lifecycle phase of a
// HomeAssistantCommunityRepository.
type CommunityRepositoryPhase string

const (
	CommunityRepositoryPhasePending    CommunityRepositoryPhase = "Pending"
	CommunityRepositoryPhaseValidating CommunityRepositoryPhase = "Validating"
	CommunityRepositoryPhaseInstalling CommunityRepositoryPhase = "Installing"
	CommunityRepositoryPhaseInstalled  CommunityRepositoryPhase = "Installed"
	CommunityRepositoryPhaseFailed     CommunityRepositoryPhase = "Failed"
	CommunityRepositoryPhaseRemoving   CommunityRepositoryPhase = "Removing"
)

// HomeAssistantCommunityRepositoryStatus defines the observed state of a
// HomeAssistantCommunityRepository.
type HomeAssistantCommunityRepositoryStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase CommunityRepositoryPhase `json:"phase,omitempty"`

	// InstalledVersion is the last ref that was successfully validated and activated.
	// +optional
	InstalledVersion string `json:"installedVersion,omitempty"`

	// ResolvedTarget is the source-manifest install target used for conflict detection.
	// +optional
	ResolvedTarget string `json:"resolvedTarget,omitempty"`

	// LastError contains a human-readable error message from the last failed operation.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// InstallingSince records when the resource most recently entered Installing.
	// +optional
	InstallingSince *metav1.Time `json:"installingSince,omitempty"`

	// ObservedGeneration reflects the generation most recently observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of repository state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:storageversion
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hacr,categories=homeassistant
// +kubebuilder:printcolumn:name="HomeAssistant",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Category",type=string,JSONPath=`.spec.category`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.installedVersion`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.homeAssistantRef == oldSelf.spec.homeAssistantRef",message="spec.homeAssistantRef is immutable after creation"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.category == oldSelf.spec.category",message="spec.category is immutable after creation"

// HomeAssistantCommunityRepository installs a HACS-compatible community extension
// into an existing HomeAssistant instance without requiring HACS or its UI.
type HomeAssistantCommunityRepository struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantCommunityRepositorySpec   `json:"spec,omitempty"`
	Status HomeAssistantCommunityRepositoryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantCommunityRepositoryList contains a list of community repositories.
type HomeAssistantCommunityRepositoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantCommunityRepository `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantCommunityRepository{}, &HomeAssistantCommunityRepositoryList{})
}
