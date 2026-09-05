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

	// Additional volumes and mounts for the Home Assistant pod
	// +optional
	AdditionalVolumes *AdditionalVolumesSpec `json:"additionalVolumes,omitempty"`

	// Resources defines CPU and memory requests/limits
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Service configuration for exposing Home Assistant
	// +optional
	Service *ServiceSpec `json:"service,omitempty"`

	// Ingress configuration for external access
	// +optional
	Ingress *IngressSpec `json:"ingress,omitempty"`

	// Gateway configures operator-managed Gateway API exposure (HTTPRoute, and
	// optionally a Gateway) for Home Assistant, with optional cert-manager TLS.
	// +optional
	Gateway *GatewaySpec `json:"gateway,omitempty"`

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

	// DisableDefaultTrustedProxies opts out of the operator's automatic
	// http.trusted_proxies / http.use_x_forwarded_for defaults. When Ingress or
	// Gateway API exposure is enabled, Home Assistant rejects every request
	// through that endpoint with 400 Bad Request until it trusts the proxy
	// forwarding the request. Unless this is set to true, and unless the user
	// has already set these keys themselves in HomeAssistantConfiguration (or
	// manages http: entirely externally, e.g. via an !include tag), the
	// operator injects the RFC1918 private address ranges (10.0.0.0/8,
	// 172.16.0.0/12, 192.168.0.0/16) as sensible defaults — this cannot be a
	// reliable autodetection of the real cluster CIDR, only a conservative
	// guess, so this field exists to opt out entirely for clusters where it
	// doesn't apply (e.g. non-RFC1918 pod/service networks, or where other
	// workloads on the pod network should not be trusted to set
	// X-Forwarded-For for the actual Ingress/Gateway proxy).
	// +optional
	DisableDefaultTrustedProxies bool `json:"disableDefaultTrustedProxies,omitempty"`

	// Scheduling controls where the Home Assistant pod is eligible to run and
	// how it is treated under resource contention, using Kubernetes' own
	// well-tested scheduling primitives directly (node selector, node/pod
	// affinity and anti-affinity, tolerations, priority class) rather than a
	// project-specific abstraction. Ships on the stable spec (not
	// spec.alpha.*): the operator only passes these fields through to the
	// generated pod template unchanged, it does not implement any new
	// scheduling behavior of its own.
	// +optional
	Scheduling *SchedulingSpec `json:"scheduling,omitempty"`

	// Alpha groups experimental, unstable fields. Fields here may change or be
	// removed without a deprecation notice.
	// +optional
	Alpha *AlphaSpec `json:"alpha,omitempty"`
}

// SchedulingSpec declares Kubernetes-native pod scheduling constraints for
// the Home Assistant pod. Every field is optional and copied verbatim onto
// the generated StatefulSet's pod template; leaving all of them unset
// preserves today's freely-schedulable, default-priority behavior.
type SchedulingSpec struct {
	// NodeSelector restricts the pod to nodes matching all of these labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Affinity declares node affinity/anti-affinity and pod
	// affinity/anti-affinity rules, using Kubernetes' own Affinity semantics
	// unchanged. Both node-level placement (e.g. "prefer nodes with local
	// NVMe storage") and pod-level positioning relative to other workloads
	// (e.g. "never share a node with this other deployment") are expressed
	// through this single field, matching how corev1.Affinity itself groups
	// NodeAffinity/PodAffinity/PodAntiAffinity together.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Tolerations allows the pod to be scheduled onto nodes with matching
	// taints that would otherwise repel it.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// PriorityClassName assigns a PriorityClass to the pod, influencing
	// scheduling preemption and eviction order under resource contention.
	// Must name an existing PriorityClass — validated at admission time.
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
}

// AlphaSpec groups experimental fields that are not yet stable enough for the
// top-level spec. See spec.alpha.* lifecycle: alpha (opt-in,
// default false) -> stable default false -> stable default true -> mandatory.
type AlphaSpec struct {
	// NetworkPolicy controls whether the operator creates a NetworkPolicy
	// restricting ingress to the Home Assistant pod.
	// +optional
	NetworkPolicy *NetworkPolicyAlphaSpec `json:"networkPolicy,omitempty"`

	// Devices declares host device nodes (e.g. /dev/ttyACM0 for a Zigbee/
	// Z-Wave USB coordinator) to mount into the Home Assistant container.
	// Each entry is mounted via a hostPath volume typed as a character
	// device; the container is never granted `privileged: true` for this.
	// Declaring at least one entry changes the pod's security context, so
	// this starts in spec.alpha until it stabilizes. This does not affect
	// where the pod is scheduled — the declared device(s) must already
	// exist on whichever node the pod lands on (see node pinning, a
	// separate capability) for this to be useful.
	// +optional
	Devices []DevicePassthroughEntry `json:"devices,omitempty"`
}

// DevicePassthroughEntry declares one host device node to expose inside the
// Home Assistant container.
type DevicePassthroughEntry struct {
	// HostPath is the device node's path on the host, e.g. /dev/ttyACM0.
	// Must be an absolute path under /dev.
	// +kubebuilder:validation:Required
	HostPath string `json:"hostPath"`

	// ContainerPath is the path the device is mounted at inside the Home
	// Assistant container. Defaults to HostPath when omitted.
	// +optional
	ContainerPath string `json:"containerPath,omitempty"`
}

// GatewaySpec configures operator-managed Gateway API exposure for HA. Managing
// Gateway API routing resources (sibling to the HA pod) is a stable opt-in — it
// does not change the Home Assistant pod's networking or security context, so it
// lives at the top level rather than under spec.alpha.
type GatewaySpec struct {
	// Enabled turns on operator management of Gateway API routing (HTTPRoute,
	// and optionally a Gateway).
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled"`

	// Host is the hostname for the route and certificate.
	// +optional
	Host string `json:"host,omitempty"`

	// IssuerRef references an existing cert-manager Issuer/ClusterIssuer. When
	// set (and cert-manager available), the operator issues a certificate for
	// the listener.
	// +optional
	IssuerRef *IssuerReference `json:"issuerRef,omitempty"`

	// SecretName references a bring-your-own TLS Secret for the listener.
	// Takes precedence over IssuerRef.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// ParentRef references an existing Gateway/listener to attach the HTTPRoute
	// to. When empty and ManageGateway is true, the operator creates a Gateway.
	// +optional
	ParentRef *GatewayParentRef `json:"parentRef,omitempty"`

	// ManageGateway controls whether the operator also creates a Gateway
	// resource (not just the HTTPRoute). GatewayClass and the gateway controller
	// remain the platform's responsibility.
	// +kubebuilder:default=false
	// +optional
	ManageGateway bool `json:"manageGateway,omitempty"`

	// Filters are HTTP route-level behaviors (header modification, redirect, URL
	// rewrite) applied, in order, to the single HTTPRoute rule the operator
	// manages for this instance. Omitted/empty leaves the route unchanged from
	// its default shape.
	// +optional
	Filters []HTTPRouteFilter `json:"filters,omitempty"`
}

// GatewayParentRef references an existing Gateway listener.
type GatewayParentRef struct {
	// Name of the existing Gateway.
	Name string `json:"name"`

	// Namespace of the Gateway. When different from the HA namespace, the user
	// must provide a ReferenceGrant.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// SectionName is the listener name (e.g. "https").
	// +optional
	SectionName string `json:"sectionName,omitempty"`
}

// HTTPRouteFilter is one user-declared route behavior attached to the HTTP
// route exposing a HomeAssistant instance through Gateway API. Mirrors the
// field names/shape of upstream Gateway API's own HTTPRouteFilter, limited to
// the four supported types: RequestHeaderModifier, ResponseHeaderModifier,
// RequestRedirect, URLRewrite. Exactly the sub-object matching Type must be
// set; the webhook rejects any other combination.
type HTTPRouteFilter struct {
	// Type selects which of the sub-objects below applies.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=RequestHeaderModifier;ResponseHeaderModifier;RequestRedirect;URLRewrite
	Type string `json:"type"`

	// RequestHeaderModifier modifies request headers. Must be set, and only be
	// set, when Type is RequestHeaderModifier.
	// +optional
	RequestHeaderModifier *HTTPHeaderFilter `json:"requestHeaderModifier,omitempty"`

	// ResponseHeaderModifier modifies response headers. Must be set, and only
	// be set, when Type is ResponseHeaderModifier.
	// +optional
	ResponseHeaderModifier *HTTPHeaderFilter `json:"responseHeaderModifier,omitempty"`

	// RequestRedirect redirects the request. Must be set, and only be set,
	// when Type is RequestRedirect.
	// +optional
	RequestRedirect *HTTPRequestRedirectFilter `json:"requestRedirect,omitempty"`

	// URLRewrite rewrites the request path/hostname. Must be set, and only be
	// set, when Type is URLRewrite.
	// +optional
	URLRewrite *HTTPURLRewriteFilter `json:"urlRewrite,omitempty"`
}

// HTTPHeaderFilter adds, sets, or removes HTTP headers. Used for both
// RequestHeaderModifier and ResponseHeaderModifier.
type HTTPHeaderFilter struct {
	// Set overwrites headers already present.
	// +optional
	Set []HTTPHeader `json:"set,omitempty"`

	// Add appends headers, keeping any existing value.
	// +optional
	Add []HTTPHeader `json:"add,omitempty"`

	// Remove lists header names to strip.
	// +optional
	Remove []string `json:"remove,omitempty"`
}

// HTTPHeader is a single HTTP header name/value pair.
type HTTPHeader struct {
	// Name of the header.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9!#$%&'*+\-.^_\x60|~]+$`
	Name string `json:"name"`

	// Value of the header.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Value string `json:"value"`
}

// HTTPRequestRedirectFilter redirects the request to a different
// scheme/hostname/path/port, optionally with a specific status code.
type HTTPRequestRedirectFilter struct {
	// Scheme replaces the request scheme (e.g. "https").
	// +optional
	Scheme *string `json:"scheme,omitempty"`

	// Hostname replaces the request hostname.
	// +optional
	Hostname *string `json:"hostname,omitempty"`

	// Path replaces the request path.
	// +optional
	Path *HTTPPathModifier `json:"path,omitempty"`

	// Port replaces the request port.
	// +optional
	Port *int32 `json:"port,omitempty"`

	// StatusCode is the redirect status code.
	// +kubebuilder:validation:Enum=301;302;303;307;308
	// +optional
	StatusCode *int `json:"statusCode,omitempty"`
}

// HTTPURLRewriteFilter rewrites the request hostname/path before it reaches
// Home Assistant.
type HTTPURLRewriteFilter struct {
	// Hostname replaces the request hostname.
	// +optional
	Hostname *string `json:"hostname,omitempty"`

	// Path replaces the request path.
	// +optional
	Path *HTTPPathModifier `json:"path,omitempty"`
}

// HTTPPathModifier describes a path replacement for HTTPRequestRedirectFilter
// or HTTPURLRewriteFilter.
type HTTPPathModifier struct {
	// Type selects which of the fields below applies.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=ReplaceFullPath;ReplacePrefixMatch
	Type string `json:"type"`

	// ReplaceFullPath is the whole replacement path. Must be set, and only be
	// set, when Type is ReplaceFullPath.
	// +optional
	ReplaceFullPath *string `json:"replaceFullPath,omitempty"`

	// ReplacePrefixMatch is the replacement for the matched path prefix. Must
	// be set, and only be set, when Type is ReplacePrefixMatch.
	// +optional
	ReplacePrefixMatch *string `json:"replacePrefixMatch,omitempty"`
}

// IssuerReference references a cert-manager Issuer or ClusterIssuer. The
// operator only references issuers — it never creates application issuers.
type IssuerReference struct {
	// Name of the Issuer/ClusterIssuer.
	Name string `json:"name"`

	// Kind of the issuer.
	// +kubebuilder:validation:Enum=Issuer;ClusterIssuer
	// +kubebuilder:default=Issuer
	// +optional
	Kind string `json:"kind,omitempty"`

	// Group of the issuer API.
	// +kubebuilder:default="cert-manager.io"
	// +optional
	Group string `json:"group,omitempty"`
}

// NetworkPolicyAlphaSpec configures the (alpha) NetworkPolicy created for the
// Home Assistant pod.
type NetworkPolicyAlphaSpec struct {
	// Enabled controls whether the operator creates a NetworkPolicy for the
	// Home Assistant pod, restricting ingress to the operator's namespace and
	// the Home Assistant namespace on the Service port. Egress is left
	// unrestricted (Home Assistant needs broad, unpredictable egress to IoT
	// devices, cloud APIs, and MQTT brokers).
	//
	// NetworkPolicy operates on pod IPs — it does not restrict traffic
	// arriving via the host network interface. Combining this with
	// spec.hostNetwork: true gives only partial isolation.
	//
	// Deliberately without omitempty: this field represents explicit user
	// intent, and the spec.alpha lifecycle plans to flip its default to true
	// in a later phase — omitempty would let an explicit false be dropped and
	// silently re-defaulted to true by the API server once that happens.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled"`
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

// AdditionalVolumesSpec defines additional volumes to mount in the Home Assistant pod.
type AdditionalVolumesSpec struct {
	// Volumes to attach to each Home Assistant pod
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// VolumeMounts to attach to each Home Assistant container
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
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

	// SecretName containing the TLS certificate. When set, it is used as-is
	// (bring-your-own) and takes precedence over IssuerRef.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// IssuerRef references an existing cert-manager Issuer/ClusterIssuer. When
	// set and cert-manager is available, the operator creates a Certificate for
	// the Ingress TLS Secret. Ignored when SecretName is provided.
	// +optional
	IssuerRef *IssuerReference `json:"issuerRef,omitempty"`
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

	// SelfUnbanCount is the total number of ban-recovery pod restarts. Kept for
	// backwards compatibility; prefer BanRestartWindowCount for limit enforcement.
	// +optional
	SelfUnbanCount int32 `json:"selfUnbanCount,omitempty"`

	// LastSelfUnban is the timestamp of the most recent ban-recovery pod restart.
	// +optional
	LastSelfUnban *metav1.Time `json:"lastSelfUnban,omitempty"`

	// BanRestartWindowStart is the start of the current ban-recovery sliding window.
	// Nil means no window is active (no ban seen or window has expired).
	// +optional
	BanRestartWindowStart *metav1.Time `json:"banRestartWindowStart,omitempty"`

	// BanRestartWindowCount is the number of ban-recovery pod restarts within the
	// current sliding window. When it reaches banRestartMaxCount the operator stops
	// restarting and sets condition BanRecoveryFailed=True.
	// +optional
	BanRestartWindowCount int32 `json:"banRestartWindowCount,omitempty"`
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

// +kubebuilder:storageversion
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
