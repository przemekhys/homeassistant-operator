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

// ScriptMode defines how the script should behave when called again while already running
// +kubebuilder:validation:Enum=single;restart;queued;parallel
type ScriptMode string

const (
	// ScriptModeSingle - Do not start a new run, issue a warning
	ScriptModeSingle ScriptMode = "single"
	// ScriptModeRestart - Stop previous runs and start a new one
	ScriptModeRestart ScriptMode = "restart"
	// ScriptModeQueued - Queue runs in order, sequential execution guaranteed
	ScriptModeQueued ScriptMode = "queued"
	// ScriptModeParallel - Launch independent concurrent runs
	ScriptModeParallel ScriptMode = "parallel"
)

// ScriptAction defines an action to be executed by the script
// This is a flexible structure that accepts any valid Home Assistant action configuration
type ScriptAction struct {
	// Raw YAML action configuration as runtime.RawExtension
	// This allows any valid Home Assistant action (service calls, delays, conditions, etc.)
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	runtime.RawExtension `json:",inline"`
}

// ScriptField defines an input parameter for the script
// This is a flexible structure that accepts any valid Home Assistant field configuration
type ScriptField struct {
	// Raw YAML field configuration as runtime.RawExtension
	// This allows any valid Home Assistant field type (selector, name, description, etc.)
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	runtime.RawExtension `json:",inline"`
}

// HomeAssistantScriptSpec defines the desired state of HomeAssistantScript
type HomeAssistantScriptSpec struct {
	// HomeAssistantRef references the HomeAssistant CR that will use this script
	// +kubebuilder:validation:Required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// ID is a unique identifier for the script (used by Home Assistant)
	// If not specified, will be auto-generated from the CR name
	// Must contain only lowercase letters, digits, and underscores
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_]*$`
	// +optional
	ID string `json:"id,omitempty"`

	// Alias is a user-friendly name for the script
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Alias string `json:"alias"`

	// Description provides details about what the script does
	// +optional
	Description string `json:"description,omitempty"`

	// Icon is a Material Design icon for the script (e.g., "mdi:script", "mdi:play")
	// See https://mdi.bessarabov.com/ for available icons
	// +optional
	Icon string `json:"icon,omitempty"`

	// Sequence defines the list of actions to execute when the script is called
	// At least one action is required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Sequence []ScriptAction `json:"sequence"`

	// Fields define input parameters for the script
	// These allow the script to accept arguments when called
	// +optional
	Fields map[string]ScriptField `json:"fields,omitempty"`

	// Mode defines how the script should behave when called again while already running
	// +kubebuilder:default=single
	// +optional
	Mode ScriptMode `json:"mode,omitempty"`

	// Max defines the maximum number of concurrent or queued runs (for queued/parallel modes)
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=10
	// +optional
	Max *int32 `json:"max,omitempty"`

	// MaxExceeded defines the log severity level when max is exceeded (silent, info, warning, error)
	// +kubebuilder:validation:Enum=silent;info;warning;error
	// +kubebuilder:default=warning
	// +optional
	MaxExceeded string `json:"maxExceeded,omitempty"`

	// AutoReload enables automatic hot-reload when script changes
	// If false, requires manual reload or pod restart
	// +kubebuilder:default=true
	// +optional
	AutoReload *bool `json:"autoReload,omitempty"`
}

// HomeAssistantScriptStatus defines the observed state of HomeAssistantScript
type HomeAssistantScriptStatus struct {
	// Conditions represent the latest available observations of the HomeAssistantScript state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ScriptHash is the SHA256 hash of the current script configuration
	// Used to detect changes and determine if reload is needed
	// +optional
	ScriptHash string `json:"scriptHash,omitempty"`

	// LastReloadTime is the timestamp of the last successful reload
	// +optional
	LastReloadTime *metav1.Time `json:"lastReloadTime,omitempty"`

	// LastRunTime is the timestamp when the script was last executed (if available from HA)
	// +optional
	LastRunTime *metav1.Time `json:"lastRunTime,omitempty"`

	// LastError contains the error message from the last failed operation
	// Cleared when operation succeeds
	// +optional
	LastError string `json:"lastError,omitempty"`

	// LastReloadMethod indicates how the last reload was performed
	// Possible values: "hot-reload" (success), "failed" (all retries exhausted), "none" (skipped/initial)
	// +optional
	LastReloadMethod string `json:"lastReloadMethod,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed HomeAssistantScript
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ObservedSynchronizationEpoch forces a fresh observation after a coordinated resume.
	// +optional
	ObservedSynchronizationEpoch int64 `json:"observedSynchronizationEpoch,omitempty"`
}

// +kubebuilder:storageversion
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hascript;hascp,categories=homeassistant
// +kubebuilder:printcolumn:name="HomeAssistant",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Alias",type=string,JSONPath=`.spec.alias`
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.homeAssistantRef == oldSelf.spec.homeAssistantRef",message="spec.homeAssistantRef is immutable after creation"

// HomeAssistantScript is the Schema for the homeassistantscripts API
type HomeAssistantScript struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantScriptSpec   `json:"spec,omitempty"`
	Status HomeAssistantScriptStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantScriptList contains a list of HomeAssistantScript
type HomeAssistantScriptList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantScript `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantScript{}, &HomeAssistantScriptList{})
}
