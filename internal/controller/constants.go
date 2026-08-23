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

	// lastAppliedIDAnnotationKey tracks the last ID sent to HA REST API.
	// Used to detect spec.id renames and delete the old resource from HA.
	lastAppliedIDAnnotationKey = "ha.homeassistant.io/last-applied-id"

	// nativeTLSHashAnnotationKey holds a hash of the native TLS certificate on the
	// StatefulSet pod template. When cert-manager rotates the certificate the hash
	// changes, triggering a rolling restart so Home Assistant picks up the new
	// material.
	nativeTLSHashAnnotationKey = "ha.homeassistant.io/native-tls-hash"

	// nativeTLSWSFingerprintAnnotationKey holds a hash of the native TLS Secret
	// content (tls.crt+tls.key) last successfully activated via HA's WS
	// http/config API, on the HomeAssistant object itself. ssl_certificate/
	// ssl_key in http/config are file *paths*, which never change across a
	// rotation (cert-manager/BYO always reuse the same mount path) — so
	// comparing HTTPConfigData alone can never detect that the underlying file
	// content changed. This annotation is the durable (survives operator
	// restart — constitution principle IV), CRD-schema-free signal that lets
	// reconcileHTTPConfigViaWS tell "same path, new certificate" from "nothing
	// changed" and still trigger a fresh http/config/configure.
	nativeTLSWSFingerprintAnnotationKey = "ha.homeassistant.io/native-tls-ws-fingerprint"

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

	// TLS / cert-manager integration condition types
	conditionCertManagerAvailable = "CertManagerAvailable"
	conditionTLSReady             = "TLSReady"
	conditionExposureReady        = "ExposureReady"

	// TLS / cert-manager condition reasons (PascalCase per K8s convention)
	reasonCertManagerInstalled    = "CertManagerInstalled"
	reasonCertManagerNotInstalled = "CertManagerNotInstalled"
	reasonIssuerNotReady          = "IssuerNotReady"
	reasonCertificateNotIssued    = "CertificateNotIssued"
	reasonWaitingForCertManager   = "WaitingForCertManager"
	reasonTLSReady                = "TLSReady"
	reasonUsingProvidedSecret     = "UsingProvidedSecret"
	reasonProvidedSecretInvalid   = "ProvidedSecretInvalid"
	reasonExposureReady           = "ExposureReady"

	// Native TLS via WS http/config/* condition reasons (TLSReady), added
	// alongside the reasons above for the same condition.
	reasonTLSConfigPending    = "TLSConfigPending"
	reasonTLSConfigReverted   = "TLSConfigReverted"
	reasonWSConfigUnsupported = "WSConfigUnsupported"

	// TLS / cert-manager event reasons
	eventCertManagerUnavailable = "CertManagerUnavailable"
	eventCertificateRequested   = "CertificateRequested"
	eventCertificateIssued      = "CertificateIssued"
	eventCertificateFailed      = "CertificateFailed"
	eventNativeTLSEnabled       = "NativeTLSEnabled"
	eventNativeTLSDisabled      = "NativeTLSDisabled"
	eventExposureConfigured     = "ExposureConfigured"
	eventTLSConfigReverted      = "TLSConfigReverted"

	// nativeTLSCertPath/nativeTLSKeyPath are where the native TLS Secret is
	// mounted in the HA pod — shared by the YAML injection path
	// (injectNativeTLS) and the WS http/config payload (desiredHTTPConfigData).
	nativeTLSCertPath = "/config/ssl/tls.crt"
	nativeTLSKeyPath  = "/config/ssl/tls.key"

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
