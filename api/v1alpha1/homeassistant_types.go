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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HomeAssistantSpec defines the desired state of HomeAssistant.
type HomeAssistantSpec struct {
	// Version is the Home Assistant version/tag to deploy (e.g., "2024.1.0", "stable", "latest")
	// +kubebuilder:default="stable"
	// +optional
	Version string `json:"version,omitempty"`

	// Image allows overriding the default Home Assistant image
	// +kubebuilder:default="ghcr.io/home-assistant/home-assistant"
	// +optional
	Image string `json:"image,omitempty"`

	// Storage configuration for Home Assistant data
	// +optional
	Storage *StorageSpec `json:"storage,omitempty"`

	// Resources defines CPU and memory requests/limits
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Service configuration for exposing Home Assistant
	// +optional
	Service *ServiceSpec `json:"service,omitempty"`

	// Ingress configuration for external access
	// +optional
	Ingress *IngressSpec `json:"ingress,omitempty"`

	// Timezone for the Home Assistant instance (e.g., "Europe/Warsaw")
	// +kubebuilder:default="UTC"
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// SecretsFrom references a Secret containing secrets.yaml
	// The Secret should have a key "secrets.yaml" with the HA secrets
	// +optional
	SecretsFrom *SecretReference `json:"secretsFrom,omitempty"`

	// HostNetwork enables host networking for the Home Assistant pod.
	// When true, the pod uses the host's network namespace, enabling discovery
	// of IoT devices via mDNS, SSDP, and DHCP on the local network.
	// +optional
	HostNetwork *bool `json:"hostNetwork,omitempty"`

	// Bootstrap configures automatic onboarding and API token creation
	// When enabled, the operator will automatically complete the Home Assistant
	// onboarding process and create a long-lived access token for API access
	// +optional
	Bootstrap *BootstrapSpec `json:"bootstrap,omitempty"`

	// Backup configures automatic backups using Home Assistant's built-in backup system.
	// Requires bootstrap with API token enabled.
	// +optional
	Backup *BackupSpec `json:"backup,omitempty"`
}

// BackupSpec configures Home Assistant's built-in backup system via WebSocket API.
type BackupSpec struct {
	// Enabled controls whether automatic backups are configured in HA
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Recurrence defines how often to create a backup
	// +kubebuilder:validation:Enum=daily;mon;tue;wed;thu;fri;sat;sun;never
	// +kubebuilder:default="daily"
	// +optional
	Recurrence string `json:"recurrence,omitempty"`

	// Time is the time of day to create the backup in HH:MM:SS 24-hour format (e.g. "03:00:00").
	// If empty, Home Assistant picks automatically.
	// +kubebuilder:validation:Pattern="^([01]\\d|2[0-3]):[0-5]\\d:[0-5]\\d$"
	// +optional
	Time string `json:"time,omitempty"`

	// RetentionCopies is the number of backup copies to keep. Nil means unlimited.
	// +kubebuilder:validation:Minimum=1
	// +optional
	RetentionCopies *int32 `json:"retentionCopies,omitempty"`

	// RetentionDays is the number of days to keep backups. Nil means unlimited.
	// +kubebuilder:validation:Minimum=1
	// +optional
	RetentionDays *int32 `json:"retentionDays,omitempty"`

	// IncludeDatabase controls whether the database is included in the backup.
	// +kubebuilder:default=true
	// +optional
	IncludeDatabase *bool `json:"includeDatabase,omitempty"`

	// AgentIDs is the list of backup agent IDs to use
	// (e.g. "backup.local", "google_drive.my_drive").
	// Defaults to ["backup.local"] if not specified.
	// +optional
	AgentIDs []string `json:"agentIDs,omitempty"`
}

// BootstrapSpec configures automatic Home Assistant onboarding and API token creation
type BootstrapSpec struct {
	// Enabled controls whether automatic bootstrap is performed
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Credentials references a Secret containing username and password for the admin user
	// The Secret must have "username" and "password" keys
	Credentials *BootstrapCredentials `json:"credentials,omitempty"`

	// CreateAPIToken controls whether a long-lived access token is created after onboarding
	// The token is valid for 10 years and stored in a Secret
	// +kubebuilder:default=true
	// +optional
	CreateAPIToken bool `json:"createApiToken,omitempty"`

	// APITokenSecretName is the name of the Secret where the API token will be stored
	// The Secret will have a "token" key containing the long-lived access token
	// If not specified, defaults to "{homeassistant-name}-homeassistant-api-token"
	// +optional
	APITokenSecretName string `json:"apiTokenSecretName,omitempty"`

	// OwnerName is the display name for the owner user created during onboarding
	// +kubebuilder:default="Admin"
	// +optional
	OwnerName string `json:"ownerName,omitempty"`

	// Language is the language code for Home Assistant (e.g., "en", "pl")
	// +kubebuilder:default="en"
	// +optional
	Language string `json:"language,omitempty"`

	// Location configures the location settings during onboarding
	// If not specified, location configuration step is skipped
	// +optional
	Location *LocationConfig `json:"location,omitempty"`

	// Analytics controls whether to enable analytics during onboarding
	// If not specified, analytics is disabled by default
	// +kubebuilder:default=false
	// +optional
	Analytics bool `json:"analytics,omitempty"`
}

// LocationConfig defines location settings for Home Assistant onboarding
type LocationConfig struct {
	// Name is the location name (e.g., "Home", "Warsaw")
	// +optional
	Name string `json:"name,omitempty"`

	// Latitude in decimal degrees (e.g., "52.2297")
	// +optional
	// +kubebuilder:validation:Pattern=`^-?([0-8]?[0-9](\.[0-9]+)?|90(\.0+)?)$`
	Latitude string `json:"latitude,omitempty"`

	// Longitude in decimal degrees (e.g., "21.0122")
	// +optional
	// +kubebuilder:validation:Pattern=`^-?(1[0-7][0-9](\.[0-9]+)?|[0-9]{1,2}(\.[0-9]+)?|180(\.0+)?)$`
	Longitude string `json:"longitude,omitempty"`

	// Elevation in meters
	// +optional
	Elevation *int `json:"elevation,omitempty"`

	// UnitSystem defines the unit system ("metric" or "us_customary")
	// +kubebuilder:default="metric"
	// +kubebuilder:validation:Enum=metric;us_customary
	// +optional
	UnitSystem string `json:"unitSystem,omitempty"`

	// Currency is the ISO 4217 currency code (e.g., "USD", "EUR", "PLN")
	// +optional
	Currency string `json:"currency,omitempty"`

	// TimeZone is the IANA timezone (e.g., "Europe/Warsaw", "America/New_York")
	// If not specified, uses spec.timezone
	// +optional
	TimeZone string `json:"timeZone,omitempty"`
}

// BootstrapCredentials references a Secret containing admin credentials
type BootstrapCredentials struct {
	// SecretRef references a Secret containing username and password
	SecretRef *CredentialsSecretRef `json:"secretRef,omitempty"`
}

// CredentialsSecretRef references a Secret containing username and password credentials
type CredentialsSecretRef struct {
	// Name of the Secret
	Name string `json:"name"`

	// UsernameKey is the key in the Secret containing the username
	// +kubebuilder:default="username"
	// +optional
	UsernameKey string `json:"usernameKey,omitempty"`

	// PasswordKey is the key in the Secret containing the password
	// +kubebuilder:default="password"
	// +optional
	PasswordKey string `json:"passwordKey,omitempty"`
}

// SecretReference references a Secret for sensitive data
type SecretReference struct {
	// Name of the Secret
	Name string `json:"name"`
}

// StorageSpec defines storage configuration for Home Assistant.
type StorageSpec struct {
	// Size of the persistent volume (e.g., "5Gi", "10Gi")
	// +kubebuilder:default="5Gi"
	// +optional
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClassName for the PVC. If empty, uses cluster default.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// AccessMode for the PVC
	// +kubebuilder:default="ReadWriteOnce"
	// +optional
	AccessMode corev1.PersistentVolumeAccessMode `json:"accessMode,omitempty"`

	// RetainPVC controls whether the PVC survives deletion of the HomeAssistant CR.
	// When true, no ownerReference is set on the PVC — it will not be garbage-collected
	// when the CR is deleted (e.g. by FluxCD reconciliation), preventing accidental data loss.
	// When false (default), the PVC is owned by the CR and deleted together with it.
	// +kubebuilder:default=false
	// +optional
	RetainPVC bool `json:"retainPVC,omitempty"`

	// InitContainer configures the init container that pre-creates required YAML files
	// (automations.yaml, scenes.yaml, scripts.yaml) on the PVC before Home Assistant starts.
	// This prevents HA from entering recovery mode when the !include directives are present
	// but the files do not yet exist.
	// +optional
	InitContainer *InitContainerSpec `json:"initContainer,omitempty"`
}

// InitContainerSpec configures the image used for the config-init init container.
type InitContainerSpec struct {
	// Repository is the container image repository (e.g. "docker.io/library")
	// +kubebuilder:default="docker.io/library"
	// +optional
	Repository string `json:"repository,omitempty"`

	// Image is the container image name (e.g. "busybox")
	// +kubebuilder:default="busybox"
	// +optional
	Image string `json:"image,omitempty"`

	// Tag is the container image tag (e.g. "1.36", "latest")
	// +kubebuilder:default="1.36"
	// +optional
	Tag string `json:"tag,omitempty"`
}

// ServiceSpec defines how Home Assistant is exposed within the cluster.
type ServiceSpec struct {
	// Type of Kubernetes Service (ClusterIP, NodePort, LoadBalancer)
	// +kubebuilder:default="ClusterIP"
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`

	// Port for the Home Assistant web UI
	// +kubebuilder:default=8123
	// +optional
	Port int32 `json:"port,omitempty"`

	// NodePort for NodePort service type (optional, auto-assigned if not set)
	// +optional
	NodePort int32 `json:"nodePort,omitempty"`
}

// IngressSpec defines external access configuration.
type IngressSpec struct {
	// Enabled controls whether an Ingress resource is created
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Host is the hostname for the Ingress (e.g., "ha.example.com")
	// +optional
	Host string `json:"host,omitempty"`

	// IngressClassName specifies the Ingress controller to use
	// +optional
	IngressClassName *string `json:"ingressClassName,omitempty"`

	// TLS configuration
	// +optional
	TLS *IngressTLSSpec `json:"tls,omitempty"`

	// Annotations to add to the Ingress resource
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// IngressTLSSpec defines TLS configuration for Ingress.
type IngressTLSSpec struct {
	// Enabled controls whether TLS is enabled
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SecretName containing the TLS certificate. If empty, cert-manager annotation should be used.
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// HomeAssistantStatus defines the observed state of HomeAssistant.
type HomeAssistantStatus struct {
	// Phase represents the current lifecycle phase of the HomeAssistant instance
	// +optional
	Phase HomeAssistantPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations of the HomeAssistant state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Version is the currently deployed Home Assistant version
	// +optional
	Version string `json:"version,omitempty"`

	// URL is the access URL for Home Assistant (if Ingress is enabled)
	// +optional
	URL string `json:"url,omitempty"`

	// Ready indicates if the Home Assistant instance is ready to serve traffic
	// +optional
	Ready bool `json:"ready,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed HomeAssistant
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// BootstrapStatus contains the status of the automatic bootstrap process
	// +optional
	Bootstrap *BootstrapStatus `json:"bootstrap,omitempty"`

	// SelfUnbanCount is the number of times the operator has removed its own IP
	// from HA's ip_bans.yaml and restarted the pod to clear in-memory bans.
	// +optional
	SelfUnbanCount int32 `json:"selfUnbanCount,omitempty"`

	// LastSelfUnban is the timestamp of the most recent self-unban operation.
	// +optional
	LastSelfUnban *metav1.Time `json:"lastSelfUnban,omitempty"`
}

// BootstrapStatus contains the status of the automatic bootstrap process
type BootstrapStatus struct {
	// Completed indicates whether the bootstrap process has finished successfully
	// +optional
	Completed bool `json:"completed,omitempty"`

	// APITokenReady indicates whether the API token has been created and stored
	// +optional
	APITokenReady bool `json:"apiTokenReady,omitempty"`

	// APITokenSecretName is the name of the Secret containing the API token
	// +optional
	APITokenSecretName string `json:"apiTokenSecretName,omitempty"`

	// LastAttempt is the timestamp of the last bootstrap attempt
	// +optional
	LastAttempt *metav1.Time `json:"lastAttempt,omitempty"`

	// Message provides additional information about the bootstrap status
	// +optional
	Message string `json:"message,omitempty"`

	// OnboardingDoneFirstSeen is the timestamp when /api/onboarding first returned 404.
	// Used to implement confirmation delay without relying on condition LastTransitionTime
	// (which does not update when only the Reason changes).
	// +optional
	OnboardingDoneFirstSeen *metav1.Time `json:"onboardingDoneFirstSeen,omitempty"`

	// LoginRecoveryAttempts tracks how many times login recovery was attempted.
	// Reset to zero when onboarding is confirmed fresh or bootstrap succeeds.
	// +optional
	LoginRecoveryAttempts int `json:"loginRecoveryAttempts,omitempty"`
}

// HomeAssistantPhase represents the current phase of the HomeAssistant instance.
// +kubebuilder:validation:Enum=Pending;Running;Failed;Unknown
type HomeAssistantPhase string

const (
	PhasePending HomeAssistantPhase = "Pending"
	PhaseRunning HomeAssistantPhase = "Running"
	PhaseFailed  HomeAssistantPhase = "Failed"
	PhaseUnknown HomeAssistantPhase = "Unknown"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ha;has
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HomeAssistant is the Schema for the homeassistants API.
type HomeAssistant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantSpec   `json:"spec,omitempty"`
	Status HomeAssistantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HomeAssistantList contains a list of HomeAssistant.
type HomeAssistantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistant `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistant{}, &HomeAssistantList{})
}
