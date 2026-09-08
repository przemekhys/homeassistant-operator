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

package controller

import "time"

// Shared constants used across multiple controllers

const (
	// Annotations
	// configHashAnnotationKey - Used by both HomeAssistant and
	// HomeAssistantConfiguration controllers
	configHashAnnotationKey = "ha.homeassistant.io/config-hash"

	// userAnnotationsAnnotationKey and userLabelsAnnotationKey - Used by HomeAssistant controller
	userAnnotationsAnnotationKey = "ha.homeassistant.io/user-annotations"
	userLabelsAnnotationKey      = "ha.homeassistant.io/user-labels"

	// lastAppliedIDAnnotationKey tracks the last ID sent to HA REST API.
	// Used to detect spec.id renames and delete the old resource from HA.
	lastAppliedIDAnnotationKey = "ha.homeassistant.io/last-applied-id"

	// Reload method names for status tracking
	// Used by Configuration and Automation controllers
	reloadMethodRestart   = "restart"
	reloadMethodHotReload = "hot-reload"
	reloadMethodNone      = "none"

	// Home Assistant defaults
	// Used across multiple controllers
	defaultHomeAssistantPort = 8123
	apiTokenSecretSuffix     = "-api-token"

	// Error messages shared across controllers
	errMsgTokenNotAvailable = "API token not found - bootstrap may not be configured"

	// Condition reasons for ReloadReady
	reasonTokenNotAvailable = "TokenNotAvailable"

	// HTTP config delivery (HomeAssistantConfiguration): condition, reasons, events.
	conditionHTTPConfigReady      = "HTTPConfigReady"
	reasonHTTPConfigApplied       = "Applied"
	reasonHTTPConfigManagedInYAML = "ManagedInYaml"
	reasonHTTPConfigRejected      = "Rejected"
	reasonHTTPConfigForeign       = "ForeignPendingChange"
	reasonHTTPConfigWaiting       = "WaitingForHomeAssistant"
	reasonHTTPConfigUnreadable    = "UnreadableSection"
	eventHTTPConfigRejected       = "HTTPConfigRejected"
	eventHTTPConfigForeignChange  = "HTTPConfigForeignChange"

	// TLS / cert-manager integration condition types
	conditionCertManagerAvailable = "CertManagerAvailable"
	conditionExposureReady        = "ExposureReady"

	// conditionTLSReady is retained only for the transitional cleanup step
	// (reconcileNativeTLSRemoval), which strips this now-obsolete condition from
	// HomeAssistant objects that had the removed spec.alpha.tls (native TLS)
	// feature enabled. Safe to delete together with that step in a later minor.
	conditionTLSReady = "TLSReady"

	// TLS / cert-manager condition reasons (PascalCase per K8s convention)
	reasonCertManagerInstalled    = "CertManagerInstalled"
	reasonCertManagerNotInstalled = "CertManagerNotInstalled"
	reasonExposureReady           = "ExposureReady"

	// TLS / cert-manager event reasons
	eventCertManagerUnavailable = "CertManagerUnavailable"
	eventCertificateRequested   = "CertificateRequested"
	eventExposureConfigured     = "ExposureConfigured"

	// eventNativeTLSRemoved is emitted once, by the transitional cleanup step,
	// on a HomeAssistant that had the removed native TLS feature enabled. Safe to
	// delete together with reconcileNativeTLSRemoval in a later minor.
	eventNativeTLSRemoved = "NativeTLSRemoved"

	// certManagerGroup is the cert-manager API group used for detection and
	// Certificate resources.
	certManagerGroup   = "cert-manager.io"
	certManagerVersion = "v1"
	certManagerKind    = "Certificate"

	// certManagerDetectionTTL bounds how often the operator re-checks whether
	// cert-manager CRDs are installed (a cache optimization; recoverable by
	// reconcile per constitution principle IV).
	certManagerDetectionTTL = 60 * time.Second
)
