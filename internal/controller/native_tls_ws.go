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

import (
	"context"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// nativeTLSPollInterval bounds how often the operator polls HA while a WS
// http/config rotation is in flight (configure sent, waiting for restart +
// health-check, or waiting for promote to land) — same cadence as the
// existing cert-manager issuance poll in reconcileTLS.
const nativeTLSPollInterval = 15 * time.Second

// bootstrapPending reports whether ha has bootstrap configured and it has not
// finished yet, i.e. the API token genuinely may not exist yet through no
// fault of the http/config WS mechanism.
func bootstrapPending(ha *hav1.HomeAssistant) bool {
	if ha.Spec.Bootstrap == nil || !ha.Spec.Bootstrap.Enabled {
		return false
	}
	return ha.Status.Bootstrap == nil || !ha.Status.Bootstrap.Completed
}

// desiredHTTPConfigData builds the full desired http: payload for the WS
// http/config/configure command: whatever non-TLS fields the user set in
// spec.configuration (parsed out of configYAML by parseHTTPSectionFields),
// plus native TLS cert/key paths (only when native TLS is enabled and
// sslReady — the Secret actually exists) and the same trusted-proxies
// defaults injectTrustedProxies would apply via YAML. The payload is always
// complete (never a partial patch — HTTP_STORAGE_SCHEMA on the HA side is not
// a patch either, see contracts/ha-http-config-api.md).
//
// Returns (nil, false) when configYAML's http: section cannot be safely
// parsed — the caller must treat this reconcile like WS being unsupported,
// never send a guessed/incomplete payload.
//
// Scope note: unlike injectTrustedProxies, this does not attempt to detect
// "user already customized these keys via HA's UI" — once WS manages http:
// for a HomeAssistant, the operator asserts its own trusted-proxies defaults
// the same way it already asserts ssl_certificate/ssl_key, consistent with
// spec.disableDefaultTrustedProxies remaining the one supported opt-out.
func desiredHTTPConfigData(
	ha *hav1.HomeAssistant, configYAML string, nativeEnabled, sslReady bool,
) (*haclient.HTTPConfigData, bool) {
	data, ok := parseHTTPSectionFields(configYAML)
	if !ok {
		return nil, false
	}

	if nativeEnabled && sslReady {
		data.SSLCertificate = nativeTLSCertPath
		data.SSLKey = nativeTLSKeyPath
	}

	exposed := ha != nil &&
		((ha.Spec.Ingress != nil && ha.Spec.Ingress.Enabled) ||
			(ha.Spec.Gateway != nil && ha.Spec.Gateway.Enabled))
	if exposed && !ha.Spec.DisableDefaultTrustedProxies {
		data.UseXForwardedFor = true
		data.TrustedProxies = defaultTrustedProxyRanges
	}

	return data, true
}

// httpConfigNeedsConfigure reports whether a fresh http/config/configure is
// needed, given no Pending is currently in flight (callers must check
// current.Pending != nil separately — an in-flight Pending is inspected on
// its own terms regardless of this function). Two independent triggers:
//   - stable's fields (paths, trusted-proxies) no longer match desired, or
//   - fingerprintConfirmed is false: ssl_certificate/ssl_key are file *paths*
//     that never change across a rotation (the mount path is always the
//     same), so a matching Stable does NOT mean the current certificate
//     content was ever actually applied — only a confirmed fingerprint match
//     (see nativeTLSWSFingerprintAnnotationKey) proves that.
func httpConfigNeedsConfigure(
	stable *haclient.HTTPConfigData, desired *haclient.HTTPConfigData, fingerprintConfirmed bool,
) bool {
	if !httpConfigDataEqual(stable, desired) {
		return true
	}
	return !fingerprintConfirmed
}

// httpConfigDataEqual compares every field the operator manages or forwards
// (TLS, trusted proxies, and the other http: fields parsed from
// spec.configuration), ignoring HA-populated bookkeeping fields
// (CreatedAt/Error/ErrorMessage) and ServerPort — that field is tied to
// spec.service.port, which the operator already controls at the Kubernetes
// Service level, so it deliberately does not also expose it for editing via
// this WS path.
func httpConfigDataEqual(a, b *haclient.HTTPConfigData) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.SSLCertificate == b.SSLCertificate &&
		a.SSLKey == b.SSLKey &&
		a.SSLPeerCertificate == b.SSLPeerCertificate &&
		a.SSLProfile == b.SSLProfile &&
		a.UseXForwardedFor == b.UseXForwardedFor &&
		reflect.DeepEqual(a.TrustedProxies, b.TrustedProxies) &&
		reflect.DeepEqual(a.ServerHost, b.ServerHost) &&
		reflect.DeepEqual(a.CORSAllowedOrigins, b.CORSAllowedOrigins) &&
		a.LoginAttemptsThreshold == b.LoginAttemptsThreshold &&
		// *bool: compare by value (nil vs non-nil, and the pointed-to value),
		// not by pointer identity — plain == on *bool would only ever be true
		// for two nils or the exact same pointer.
		reflect.DeepEqual(a.IPBanEnabled, b.IPBanEnabled) &&
		reflect.DeepEqual(a.UseXFrameOptions, b.UseXFrameOptions)
}

// nativeTLSVolume builds the pod Volume/VolumeMount for the native TLS Secret
// and its rotation hash, when native TLS is enabled and the Secret exists.
// Extracted out of buildStatefulSet purely to keep that function's
// cyclomatic complexity in check — the hash is "" (no rolling restart)
// whenever nativeTLSManagedByWS reports HA's WS http/config/* API already
// owns rotation for this instance.
func (r *HomeAssistantReconciler) nativeTLSVolume(
	ctx context.Context, ha *hav1.HomeAssistant,
) (corev1.Volume, corev1.VolumeMount, string, bool) {
	n := nativeTLS(ha)
	if n == nil || !n.Enabled {
		return corev1.Volume{}, corev1.VolumeMount{}, "", false
	}
	tlsSecretName := nativeTLSSecretName(ha)
	tlsSecret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Name: tlsSecretName, Namespace: ha.Namespace}, tlsSecret); err != nil {
		return corev1.Volume{}, corev1.VolumeMount{}, "", false
	}

	hash := ""
	if !nativeTLSManagedByWS(ha) {
		hash = calculateConfigHash(string(tlsSecret.Data["tls.crt"]))
	}
	vol := corev1.Volume{
		Name:         "ha-native-tls",
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: tlsSecretName}},
	}
	mount := corev1.VolumeMount{Name: "ha-native-tls", MountPath: "/config/ssl", ReadOnly: true}
	return vol, mount, hash, true
}

// nativeTLSManagedByWS reports whether the last-observed TLSReady reason
// indicates HA's WS http/config/* API is managing native TLS rotation for
// this instance — in that case, reconcileStatefulSet must NOT also compute a
// cert-hash annotation that would trigger a K8s-level pod restart: HA
// already restarts itself internally as part of http/config/configure, and a
// second, uncoordinated restart would race with it for no benefit.
//
// This is read from status (never a value the operator holds in memory
// across reconciles): when TLSReady has never
// been set by reconcileHTTPConfigViaWS yet (fresh HA), it falls back to the
// pre-existing restart-triggering behavior, exactly as before this feature.
func nativeTLSManagedByWS(ha *hav1.HomeAssistant) bool {
	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if cond == nil {
		return false
	}
	switch cond.Reason {
	case reasonTLSConfigPending, reasonTLSConfigReverted, reasonTLSReady:
		// reasonTLSReady is only produced for native TLS by
		// reconcileHTTPConfigViaWS (this file) since this feature landed —
		// the previous direct cert-manager/BYO writers of this reason were
		// replaced by delegation to it (see tls_helpers.go).
		return true
	default:
		// reasonWSConfigUnsupported, reasonCertificateNotIssued, etc.
		return false
	}
}

// nativeTLSSecretFingerprint hashes the native TLS Secret's actual
// certificate material (tls.crt+tls.key) — the signal that a rotation
// happened, independent of the (always-static) mount path HA sees in
// http/config's ssl_certificate/ssl_key fields.
func nativeTLSSecretFingerprint(secret *corev1.Secret) string {
	return calculateConfigHash(string(secret.Data["tls.crt"]) + string(secret.Data["tls.key"]))
}

// recordNativeTLSFingerprint persists fingerprint as the confirmed,
// successfully-activated native TLS content on the HomeAssistant object
// itself (an annotation — no CRD schema change, still durable per
// constitution principle IV). Retries on conflict like
// updateHAStatusWithRetry, but against the main object (annotations live in
// ObjectMeta, not status).
func (r *HomeAssistantReconciler) recordNativeTLSFingerprint(
	ctx context.Context, ha *hav1.HomeAssistant, fingerprint string,
) error {
	key := client.ObjectKeyFromObject(ha)
	attempted := false
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if attempted {
			if err := r.Get(ctx, key, ha); err != nil {
				return err
			}
		}
		attempted = true
		if ha.Annotations[nativeTLSWSFingerprintAnnotationKey] == fingerprint {
			return nil
		}
		if ha.Annotations == nil {
			ha.Annotations = map[string]string{}
		}
		ha.Annotations[nativeTLSWSFingerprintAnnotationKey] = fingerprint
		return r.Update(ctx, ha)
	})
}

// nativeTLSHealthy reports whether HA responds over HTTPS with the
// currently-mounted native TLS certificate — the only reliable way to tell
// "HA is still restarting" from "HA restarted but the new cert doesn't work"
// (promote must never be optimistic).
func nativeTLSHealthy(
	ctx context.Context, r *HomeAssistantReconciler, ha *hav1.HomeAssistant,
) bool {
	cl := newHAClientForHA(ctx, r.Client, ha, r.NewHAClient)
	return cl.CheckHealth(ctx) == nil
}

// generatedHTTPConfigYAML best-effort reads the generated configuration.yaml
// ConfigMap content (owned by HomeAssistantConfigurationReconciler, a
// different controller — read here the same way the config-hash restart-sync
// mechanism already reads it). A missing ConfigMap (e.g. brand new HA, not
// reconciled by that controller yet) is not an error: it just means no
// user-configured http: fields exist yet.
func generatedHTTPConfigYAML(ctx context.Context, cl client.Reader, ha *hav1.HomeAssistant) string {
	cm := &corev1.ConfigMap{}
	name := ha.Name + generatedConfigmapSuffix
	if err := cl.Get(ctx, client.ObjectKey{Name: name, Namespace: ha.Namespace}, cm); err != nil {
		return ""
	}
	return cm.Data[configurationYamlKey]
}

// reconcileHTTPConfigViaWS drives the entire http: configuration (TLS when
// native TLS is enabled, plus every other http: field parsed out of
// spec.configuration) through HA's WS http/config/* API for every
// HomeAssistant reconcile — not gated on native TLS, since HA's
// YAML-to-storage migration silently drops the *whole* http: section, for
// every instance, regardless of TLS. It is the sole writer of TLSReady (only
// touched when native TLS is actually requested — an instance that never
// asked for TLS has nothing meaningful to report there), avoiding any risk
// of two reconcilers fighting over the same condition.
//
// Every reconcile re-derives its decision from a fresh GetHTTPConfig call
// (never from state remembered across calls): a dropped WS connection, an
// operator restart, or an HA upgrade mid-rotation are all safely picked back
// up by simply observing the current HA-side state again.
func (r *HomeAssistantReconciler) reconcileHTTPConfigViaWS(
	ctx context.Context, ha *hav1.HomeAssistant,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	nativeEnabled := nativeTLS(ha) != nil && nativeTLS(ha).Enabled
	maybeSetCondition := func(status metav1.ConditionStatus, reason, message string) error {
		if !nativeEnabled {
			return nil
		}
		return r.setNativeTLSCondition(ctx, ha, status, reason, message)
	}

	var fingerprint string
	sslReady := false
	if nativeEnabled {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{Name: nativeTLSSecretName(ha), Namespace: ha.Namespace}, secret); err == nil {
			fingerprint = nativeTLSSecretFingerprint(secret)
			sslReady = true
		}
		// Secret not found: native TLS material not provisioned yet (e.g.
		// still waiting on cert-manager) — proceed without SSL fields; the
		// non-TLS fields below are still worth managing via WS.
	}

	desired, ok := desiredHTTPConfigData(ha, generatedHTTPConfigYAML(ctx, r.Client, ha), nativeEnabled, sslReady)
	if !ok {
		// Unparseable http: (e.g. tagged-scalar !include) — never guess,
		// behave exactly like WS being unsupported for this reconcile.
		return ctrl.Result{}, maybeSetCondition(metav1.ConditionTrue, reasonWSConfigUnsupported,
			"configuration.yaml's http: section cannot be safely read (e.g. !include); using it as-is via YAML")
	}

	token, tokenErr := getAPIToken(ctx, r.Client, ha)
	if tokenErr != nil {
		if bootstrapPending(ha) {
			// Bootstrap is configured and still running (no token yet is
			// expected here, not an error condition). Leave TLSReady exactly
			// as-is instead of stamping WSConfigUnsupported=True: that reason
			// still reads as "no active rotation" to nativeTLSActive, but it
			// would flip haScheme()/nativeTLSActive to https for THIS
			// reconciler's own HA clients (notably reconcileBootstrap's
			// health-check) the moment cert-manager issues a certificate —
			// long before the pod has actually picked up the YAML-injected
			// TLS material and restarted onto HTTPS. That poisons every
			// subsequent reconcile's bootstrap client (status conditions
			// persist across calls) and stalls bootstrap for as long as the
			// pod needs to restart and come back up, found via a live CI
			// timing comparison. Just requeue and re-check next pass.
			return ctrl.Result{RequeueAfter: nativeTLSPollInterval}, nil
		}
		// Bootstrap absent/disabled/already completed: a still-missing token
		// is unexpected, so fall back to the YAML path exactly like
		// reasonWSConfigUnsupported (fix: this used to reuse reasonTLSReady,
		// which nativeTLSManagedByWS treats as "WS already owns this" —
		// applyNativeTLS would then wrongly skip YAML injection, leaving HA
		// on plain HTTP with no TLS configured through either mechanism).
		return ctrl.Result{}, maybeSetCondition(metav1.ConditionTrue, reasonWSConfigUnsupported,
			"HA API token not yet available to attempt http/config; using configuration.yaml for now")
	}

	cl := newHAClientForHA(ctx, r.Client, ha, r.NewHAClient)
	httpCfg, err := cl.GetHTTPConfig(ctx, token)
	if err != nil {
		if haclient.IsUnknownCommand(err) || haclient.IsNotRunning(err) {
			return ctrl.Result{}, maybeSetCondition(metav1.ConditionTrue, reasonWSConfigUnsupported,
				"HA does not support the http/config WebSocket API yet (older core version, or not yet running); "+
					"falling back to configuration.yaml")
		}
		log.V(1).Info("http/config: GetHTTPConfig failed, will retry", "error", err)
		return ctrl.Result{RequeueAfter: nativeTLSPollInterval}, nil
	}

	// An in-flight Pending is inspected on its own terms first — regardless of
	// fingerprint bookkeeping, resending a configure while one is already
	// pending would just restart HA again for nothing.
	if httpCfg.Pending != nil {
		if httpCfg.Pending.Error != "" {
			// A failed/reverted trial is kept populated with
			// Error/ErrorMessage by HA, never reset to nil. This is the only
			// correct revert signal (never Pending == nil).
			if !nativeEnabled {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, r.reportNativeTLSReverted(ctx, ha, httpCfg.Pending.ErrorMessage)
		}

		// Pending with no error yet: HA may still be restarting.
		if !nativeTLSHealthy(ctx, r, ha) {
			if err := maybeSetCondition(metav1.ConditionFalse, reasonTLSConfigPending,
				"Waiting for HA to restart and become reachable with the new http: configuration"); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: nativeTLSPollInterval}, nil
		}

		if err := cl.PromoteHTTPConfig(ctx, token); err != nil {
			log.V(1).Info("http/config: PromoteHTTPConfig failed, will retry", "error", err)
			return ctrl.Result{RequeueAfter: nativeTLSPollInterval}, nil
		}
		if nativeEnabled && sslReady {
			if err := r.recordNativeTLSFingerprint(ctx, ha, fingerprint); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, maybeSetCondition(metav1.ConditionTrue, reasonTLSReady,
			"Native TLS configuration confirmed active via http/config/promote")
	}

	// The fingerprint check only matters when native TLS is actually in play
	// (it exists purely to catch "same path, rotated content" — see
	// httpConfigNeedsConfigure); vacuously true otherwise so a CORS/IP-ban-only
	// change on a non-TLS instance is judged on field equality alone.
	fingerprintConfirmed := !nativeEnabled || !sslReady ||
		ha.Annotations[nativeTLSWSFingerprintAnnotationKey] == fingerprint
	if httpConfigNeedsConfigure(&httpCfg.Stable, desired, fingerprintConfirmed) {
		if err := cl.ConfigureHTTPConfig(ctx, token, desired); err != nil {
			log.V(1).Info("http/config: ConfigureHTTPConfig failed, will retry", "error", err)
			return ctrl.Result{RequeueAfter: nativeTLSPollInterval}, nil
		}
		if err := maybeSetCondition(metav1.ConditionFalse, reasonTLSConfigPending,
			"New configuration sent via http/config/configure; waiting for HA to restart and confirm"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: nativeTLSPollInterval}, nil
	}

	// Stable already matches desired AND (when native TLS is in play) the
	// currently-mounted certificate content was already confirmed active:
	// steady state.
	return ctrl.Result{}, maybeSetCondition(metav1.ConditionTrue, reasonTLSReady,
		"Configuration active (http/config)")
}

// setNativeTLSCondition sets TLSReady, returning nil on no-op (no status
// write when nothing changed, matching updateHAStatusWithRetry's contract).
func (r *HomeAssistantReconciler) setNativeTLSCondition(
	ctx context.Context, ha *hav1.HomeAssistant, status metav1.ConditionStatus, reason, message string,
) error {
	return r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
		return meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
			Type:               conditionTLSReady,
			Status:             status,
			ObservedGeneration: h.Generation,
			Reason:             reason,
			Message:            message,
		})
	})
}

// reportNativeTLSReverted records that HA auto-reverted a rejected rotation.
// The Status stays True (the OLD, still-active certificate is fine) with
// reason TLSConfigReverted — this is a warning about the last rotation
// attempt, not a statement that HA is currently unavailable. The Event is
// only emitted on the actual transition into this reason (via
// updateHAStatusWithRetry's changed-detection), never repeated every
// reconcile while steady in this state (no promote-loop).
func (r *HomeAssistantReconciler) reportNativeTLSReverted(
	ctx context.Context, ha *hav1.HomeAssistant, errMessage string,
) error {
	message := "Native TLS rotation was rejected by HA and auto-reverted to the previous certificate"
	if errMessage != "" {
		message += ": " + errMessage
	}

	var changed bool
	if err := r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
		changed = meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
			Type:               conditionTLSReady,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: h.Generation,
			Reason:             reasonTLSConfigReverted,
			Message:            message,
		})
		return changed
	}); err != nil {
		return err
	}
	if changed {
		r.Recorder.Eventf(ha, nil, corev1.EventTypeWarning, eventTLSConfigReverted, eventTLSConfigReverted, "%s", message)
	}
	return nil
}
