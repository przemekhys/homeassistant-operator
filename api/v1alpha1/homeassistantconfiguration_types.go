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

// ConfigurationReloadStrategy defines how configuration changes should be handled
// +kubebuilder:validation:Enum=auto;hot-reload;restart
type ConfigurationReloadStrategy string

const (
	// ConfigurationReloadStrategyAuto - automatically choose best strategy based on config changes
	ConfigurationReloadStrategyAuto ConfigurationReloadStrategy = "auto"
	// ConfigurationReloadStrategyHotReload - attempt to hot-reload via HA REST API
	ConfigurationReloadStrategyHotReload ConfigurationReloadStrategy = "hot-reload"
	// ConfigurationReloadStrategyRestart - force full pod restart
	ConfigurationReloadStrategyRestart ConfigurationReloadStrategy = "restart"
)

// HTTPConfig defines HTTP component configuration
type HTTPConfig struct {
	// CorsDomains is a list of allowed CORS origins
	// +optional
	CORSDomains []string `json:"corsDomains,omitempty"`

	// TrustProxy enables trust in X-Forwarded-For header
	// +optional
	TrustProxy *bool `json:"trustProxy,omitempty"`

	// UseXForwardedFor enables usage of X-Forwarded-For header
	// +optional
	UseXForwardedFor *bool `json:"useXForwardedFor,omitempty"`
}

// LoggerConfig defines logging component configuration
type LoggerConfig struct {
	// DefaultLevel is the default logging level (DEBUG, INFO, WARNING, ERROR, CRITICAL)
	// +kubebuilder:default="INFO"
	// +optional
	DefaultLevel string `json:"defaultLevel,omitempty"`

	// Logs is a map of component names to their logging levels
	// Example: {"homeassistant.core": "DEBUG", "homeassistant.components.mqtt": "DEBUG"}
	// +optional
	Logs map[string]string `json:"logs,omitempty"`
}

// RecorderConfig defines recorder (database) component configuration
type RecorderConfig struct {
	// Enabled controls if the recorder is enabled
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Database URL for the recorder (e.g., "postgresql://user:pass@host/db")
	// If not specified, uses SQLite with default path.
	// Mutually exclusive with DatabaseSecretRef; DatabaseSecretRef takes precedence.
	// +optional
	Database string `json:"database,omitempty"`

	// DatabaseSecretRef references a Secret containing the database URL.
	// The resolved value is written as plain text into configuration.yaml,
	// avoiding the !secret tag which is stripped by the YAML round-trip.
	// Takes precedence over Database if both are set.
	// The Secret must be in the same namespace as the HomeAssistant CR.
	// +optional
	DatabaseSecretRef *SecretKeySelector `json:"databaseSecretRef,omitempty"`

	// PurgeKeepDays specifies how many days of history to keep
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=30
	// +optional
	PurgeKeepDays *int32 `json:"purgeKeepDays,omitempty"`
}

// MQTTConfig defines MQTT component configuration
type MQTTConfig struct {
	// Broker is the MQTT broker address (e.g., "mqtt://localhost:1883")
	// +kubebuilder:validation:Required
	Broker string `json:"broker"`

	// Username for MQTT authentication
	// +optional
	Username string `json:"username,omitempty"`

	// PasswordRef references a Secret containing the MQTT password
	// The Secret should have a "password" key
	// +optional
	PasswordRef *SecretKeySelector `json:"passwordRef,omitempty"`

	// ClientID for MQTT connection
	// +optional
	ClientID string `json:"clientID,omitempty"`

	// KeepAlive defines MQTT keep-alive interval in seconds
	// +kubebuilder:default=60
	// +optional
	KeepAlive *int32 `json:"keepAlive,omitempty"`
}

// SecretKeySelector selects a Secret and an optional key
type SecretKeySelector struct {
	// Name of the Secret
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Key in the Secret (if not specified, defaults to "password" or "value")
	// +optional
	Key string `json:"key,omitempty"`
}

// HomeAssistantConfigurationSpec defines the desired state of HomeAssistantConfiguration
type HomeAssistantConfigurationSpec struct {
	// HomeAssistantRef references the HomeAssistant CR that will use this configuration
	// +kubebuilder:validation:Required
	HomeAssistantRef HomeAssistantReference `json:"homeAssistantRef"`

	// Configuration contains the full configuration.yaml content as a string
	// This is the raw YAML configuration for Home Assistant
	// +kubebuilder:validation:Required
	Configuration string `json:"configuration"`

	// ReloadStrategy defines how configuration changes should be applied
	// +kubebuilder:default=auto
	// +optional
	ReloadStrategy ConfigurationReloadStrategy `json:"reloadStrategy,omitempty"`

	// AutoReload enables automatic reloading/restart when configuration changes
	// +kubebuilder:default=true
	// +optional
	AutoReload *bool `json:"autoReload,omitempty"`

	// HTTP component configuration (optional typed section)
	// +optional
	HTTP *HTTPConfig `json:"http,omitempty"`

	// Logger component configuration (optional typed section)
	// +optional
	Logger *LoggerConfig `json:"logger,omitempty"`

	// Recorder component configuration (optional typed section)
	// +optional
	Recorder *RecorderConfig `json:"recorder,omitempty"`

	// MQTT component configuration (optional typed section)
	// +optional
	MQTT *MQTTConfig `json:"mqtt,omitempty"`
}

// HomeAssistantConfigurationStatus defines the observed state of HomeAssistantConfiguration
type HomeAssistantConfigurationStatus struct {
	// Conditions represent the latest available observations of the HomeAssistantConfiguration state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ConfigHash is the SHA256 hash of the current configuration
	// Used to detect changes and determine if reload is needed
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// LastReloadTime is the timestamp of the last successful reload/restart
	// +optional
	LastReloadTime *metav1.Time `json:"lastReloadTime,omitempty"`

	// LastReloadMethod indicates how the last reload was performed (hot-reload or restart)
	// +optional
	LastReloadMethod string `json:"lastReloadMethod,omitempty"`

	// LastError contains the error message from the last failed reload attempt
	// Cleared when reload succeeds
	// +optional
	LastError string `json:"lastError,omitempty"`

	// Generation tracks the generation of the spec that the status reflects
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=haconfig;hacfg
// +kubebuilder:printcolumn:name="HomeAssistant",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Strategy",type=string,JSONPath=`.spec.reloadStrategy`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.homeAssistantRef == oldSelf.spec.homeAssistantRef",message="spec.homeAssistantRef is immutable after creation"

// HomeAssistantConfiguration is the Schema for the homeassistantconfigurations API.
type HomeAssistantConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantConfigurationSpec   `json:"spec,omitempty"`
	Status HomeAssistantConfigurationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantConfigurationList contains a list of HomeAssistantConfiguration.
type HomeAssistantConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantConfiguration{}, &HomeAssistantConfigurationList{})
}
