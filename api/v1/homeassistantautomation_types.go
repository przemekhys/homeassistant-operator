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

// AutomationMode defines how the automation should behave when triggered again while already running
// +kubebuilder:validation:Enum=single;restart;queued;parallel
type AutomationMode string

const (
	// AutomationModeSingle - Do not start a new run, issue a warning
	AutomationModeSingle AutomationMode = "single"
	// AutomationModeRestart - Stop previous runs and start a new one
	AutomationModeRestart AutomationMode = "restart"
	// AutomationModeQueued - Queue runs in order, sequential execution guaranteed
	AutomationModeQueued AutomationMode = "queued"
	// AutomationModeParallel - Launch independent concurrent runs
	AutomationModeParallel AutomationMode = "parallel"
)

// AutomationTrigger defines an event that will trigger the automation
// This is a flexible structure that accepts any valid Home Assistant trigger configuration
type AutomationTrigger struct {
	// Raw YAML trigger configuration as runtime.RawExtension
	// This allows any valid Home Assistant trigger type (platform, event, state, time, etc.)
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	runtime.RawExtension `json:",inline"`
}

// AutomationCondition defines a condition that must be met for the automation to execute
// This is a flexible structure that accepts any valid Home Assistant condition configuration
type AutomationCondition struct {
	// Raw YAML condition configuration as runtime.RawExtension
	// This allows any valid Home Assistant condition type (state, numeric_state, time, etc.)
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	runtime.RawExtension `json:",inline"`
}

// AutomationAction defines an action to be executed by the automation
// This is a flexible structure that accepts any valid Home Assistant action configuration
type AutomationAction struct {
	// Raw YAML action configuration as runtime.RawExtension
	// This allows any valid Home Assistant action (service calls, delays, conditions, etc.)
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	runtime.RawExtension `json:",inline"`
}

// HomeAssistantAutomationSpec defines the desired state of HomeAssistantAutomation
type HomeAssistantAutomationSpec struct {
	// HomeAssistantRef references the HomeAssistant CR that will use this automation
	// +kubebuilder:validation:Required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// ID is a unique identifier for the automation (used by Home Assistant)
	// If not specified, will be auto-generated from the CR name
	// Must contain only lowercase letters, digits, and underscores. Existing
	// resources with an id predating this constraint (uppercase letters or
	// hyphens) keep working until their next update, which will then be
	// rejected until id is renamed to a conforming value.
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_]*$`
	// +optional
	ID string `json:"id,omitempty"`

	// Alias is a user-friendly name for the automation
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Alias string `json:"alias"`

	// Description provides details about what the automation does
	// +optional
	Description string `json:"description,omitempty"`

	// Triggers define the events that will trigger this automation
	// At least one trigger is required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Triggers []AutomationTrigger `json:"triggers"`

	// Conditions define requirements that must be met for the automation to execute
	// All conditions must evaluate to true for the automation to run
	// +optional
	Conditions []AutomationCondition `json:"conditions,omitempty"`

	// Actions define the sequence of tasks to execute when triggered
	// At least one action is required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Actions []AutomationAction `json:"actions"`

	// Mode defines how the automation should behave when triggered again while already running
	// +kubebuilder:default=single
	// +optional
	Mode AutomationMode `json:"mode,omitempty"`

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

	// InitialState defines whether the automation should be enabled at startup
	// If not specified, the last state is restored
	// +optional
	InitialState *bool `json:"initialState,omitempty"`

	// AutoReload enables automatic hot-reload when automation changes
	// If false, requires manual reload or pod restart
	// +kubebuilder:default=true
	// +optional
	AutoReload *bool `json:"autoReload,omitempty"`

	// Enabled controls whether this automation is active
	// Can be used to temporarily disable without deleting the CR
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// HomeAssistantAutomationStatus defines the observed state of HomeAssistantAutomation
type HomeAssistantAutomationStatus struct {
	// Conditions represent the latest available observations of the HomeAssistantAutomation state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// AutomationHash is the SHA256 hash of the current automation configuration
	// Used to detect changes and determine if reload is needed
	// +optional
	AutomationHash string `json:"automationHash,omitempty"`

	// LastReloadTime is the timestamp of the last successful reload
	// +optional
	LastReloadTime *metav1.Time `json:"lastReloadTime,omitempty"`

	// LastTriggeredTime is the timestamp when the automation was last triggered (if available from HA)
	// +optional
	LastTriggeredTime *metav1.Time `json:"lastTriggeredTime,omitempty"`

	// LastError contains the error message from the last failed operation
	// Cleared when operation succeeds
	// +optional
	LastError string `json:"lastError,omitempty"`

	// LastReloadMethod indicates how the last reload was performed
	// Possible values: "hot-reload", "restart", "none"
	// +optional
	LastReloadMethod string `json:"lastReloadMethod,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed HomeAssistantAutomation
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ObservedSynchronizationEpoch forces a fresh observation after a coordinated resume.
	// +optional
	ObservedSynchronizationEpoch int64 `json:"observedSynchronizationEpoch,omitempty"`
}

// +kubebuilder:storageversion
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=haauto;haautomation,categories=homeassistant
// +kubebuilder:printcolumn:name="HomeAssistant",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Alias",type=string,JSONPath=`.spec.alias`
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.homeAssistantRef == oldSelf.spec.homeAssistantRef",message="spec.homeAssistantRef is immutable after creation"

// HomeAssistantAutomation is the Schema for the homeassistantautomations API
type HomeAssistantAutomation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantAutomationSpec   `json:"spec,omitempty"`
	Status HomeAssistantAutomationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantAutomationList contains a list of HomeAssistantAutomation
type HomeAssistantAutomationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantAutomation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantAutomation{}, &HomeAssistantAutomationList{})
}
