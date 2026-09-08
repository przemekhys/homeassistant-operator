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

// HomeAssistantReference references the HomeAssistant instance a community repository
// targets. Declared locally in v1alpha1 (rather than reusing api/v1's type) so this
// experimental API group-version has no coupling to the stable v1 types.
type HomeAssistantReference struct {
	// Name of the HomeAssistant resource
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// CommunityRepositoryCategory is a HACS repository category. Values match HACS's own
// hacs.json "category" field exactly.
// +kubebuilder:validation:Enum=integration;plugin;theme;python_script;template
type CommunityRepositoryCategory string

const (
	CategoryIntegration  CommunityRepositoryCategory = "integration"
	CategoryPlugin       CommunityRepositoryCategory = "plugin"
	CategoryTheme        CommunityRepositoryCategory = "theme"
	CategoryPythonScript CommunityRepositoryCategory = "python_script"
	CategoryTemplate     CommunityRepositoryCategory = "template"
)

// HomeAssistantCommunityRepositorySpec defines the desired state of HomeAssistantCommunityRepository
type HomeAssistantCommunityRepositorySpec struct {
	// HomeAssistantRef references the HomeAssistant instance to install this repository into
	// +kubebuilder:validation:Required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// Category is the HACS repository category. appdaemon and netdaemon are intentionally
	// not accepted here: they require a separate runtime this operator does not deploy.
	// +kubebuilder:validation:Required
	Category CommunityRepositoryCategory `json:"category"`

	// Repository is the GitHub "owner/repo" shorthand (not a full URL).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[\w.-]+/[\w.-]+$`
	Repository string `json:"repository"`

	// Ref is the tag, branch, or commit SHA to install. Pinned and explicit — this
	// operator never tracks a "latest" release automatically. Restricted to
	// characters valid in a git ref: word characters, dot, dash and slash, so
	// branch names such as release/v1 are accepted. URL-reserved characters that
	// could alter the codeload request path are not.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[\w][\w.\-/]*$`
	Ref string `json:"ref"`
}

// CommunityRepositoryPhase is the reconciliation lifecycle phase of a
// HomeAssistantCommunityRepository.
type CommunityRepositoryPhase string

const (
	PhasePending    CommunityRepositoryPhase = "Pending"
	PhaseValidating CommunityRepositoryPhase = "Validating"
	PhaseInstalling CommunityRepositoryPhase = "Installing"
	PhaseInstalled  CommunityRepositoryPhase = "Installed"
	PhaseFailed     CommunityRepositoryPhase = "Failed"
	PhaseRemoving   CommunityRepositoryPhase = "Removing"
)

// HomeAssistantCommunityRepositoryStatus defines the observed state of HomeAssistantCommunityRepository
type HomeAssistantCommunityRepositoryStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase CommunityRepositoryPhase `json:"phase,omitempty"`

	// InstalledVersion is the last ref that was successfully validated and activated.
	// It does NOT change until a newly requested ref is fully confirmed, so a failed
	// update never reports a broken version as installed.
	// +optional
	InstalledVersion string `json:"installedVersion,omitempty"`

	// ResolvedTarget is the install target computed from the source repository's own
	// manifest (the integration domain, or the theme/script/template/plugin file name).
	// Together with the referenced HomeAssistant name and Category it forms the
	// conflict-detection key, so the same target may be installed into two
	// different instances.
	// +optional
	ResolvedTarget string `json:"resolvedTarget,omitempty"`

	// LastError contains a human-readable error message from the last failed operation.
	// Cleared when the operation succeeds.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// InstallingSince records when the resource most recently entered the
	// Installing phase. Used to bound the activation retry window; cleared when
	// the resource leaves Installing (Installed or Failed).
	// +optional
	InstallingSince *metav1.Time `json:"installingSince,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed CR
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the repository state
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
// (integration, plugin, theme, python_script, or template) into an existing
// HomeAssistant instance, without requiring HACS or its UI to be present. This is an
// EXPERIMENTAL, alpha-quality resource: it carries no API stability guarantee between
// releases.
type HomeAssistantCommunityRepository struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantCommunityRepositorySpec   `json:"spec,omitempty"`
	Status HomeAssistantCommunityRepositoryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantCommunityRepositoryList contains a list of HomeAssistantCommunityRepository
type HomeAssistantCommunityRepositoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantCommunityRepository `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantCommunityRepository{}, &HomeAssistantCommunityRepositoryList{})
}
