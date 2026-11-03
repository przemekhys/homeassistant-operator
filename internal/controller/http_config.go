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
	"errors"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// httpConfigPath is which channel the operator uses to deliver the http:
// configuration to a given Home Assistant instance.
type httpConfigPath int

const (
	// httpPathUndetermined: the operator could not yet reach Home Assistant to
	// find out (no token, instance not ready). Transient — never an error.
	httpPathUndetermined httpConfigPath = iota
	// httpPathYAML: Home Assistant is older than 2026.8 and has no http config
	// API — the http: section keeps going into configuration.yaml as before.
	httpPathYAML
	// httpPathAPI: Home Assistant supports the http config API — the http:
	// section is delivered through it and omitted from configuration.yaml.
	httpPathAPI
)

// httpConfigDecision is the result of probing an instance for http config API
// support, carried through one reconcile.
type httpConfigDecision struct {
	path  httpConfigPath
	resp  *haclient.HTTPConfigResponse // set only when path == httpPathAPI
	token string
}

// httpConfigRequeueAfterConfigure is how long to wait before checking that a
// just-sent pending configuration can be promoted. It is deliberately well under
// Home Assistant's 5-minute auto-revert window. If Home Assistant restarts to
// apply the change and is not back by then, the next reconcile simply defers
// (httpPathUndetermined) and retries — the window is only genuinely at risk on
// an instance that takes minutes to restart, which has larger problems.
const httpConfigRequeueAfterConfigure = 30 * time.Second

// decideHTTPConfigPath probes the instance for http config API support.
func (r *HomeAssistantConfigurationReconciler) decideHTTPConfigPath(
	ctx context.Context, ha *hav1.HomeAssistant,
) httpConfigDecision {
	log := logf.FromContext(ctx)

	token, err := getAPIToken(ctx, r.Client, ha)
	if err != nil || token == "" {
		return httpConfigDecision{path: httpPathUndetermined}
	}
	if !r.isHomeAssistantServiceReady(ctx, ha) {
		return httpConfigDecision{path: httpPathUndetermined, token: token}
	}

	client := r.httpConfigClient(ha)
	resp, err := client.GetHTTPConfig(ctx, token)
	switch {
	case err == nil:
		return httpConfigDecision{path: httpPathAPI, resp: resp, token: token}
	case errors.Is(err, haclient.ErrHTTPConfigUnsupported):
		return httpConfigDecision{path: httpPathYAML, token: token}
	default:
		log.V(1).Info("http config probe failed; will retry", "error", err)
		return httpConfigDecision{path: httpPathUndetermined, token: token}
	}
}

// httpConfigClient builds a Home Assistant client for the given instance, using
// the reconciler's NewHAClient override when set (tests) or a plain client.
func (r *HomeAssistantConfigurationReconciler) httpConfigClient(ha *hav1.HomeAssistant) *haclient.Client {
	url := r.buildHomeAssistantURL(ha)
	if r.NewHAClient != nil {
		return r.NewHAClient(url)
	}
	return haclient.NewClient(url)
}

// desiredHTTPConfig builds the configuration the operator wants Home Assistant to
// hold, by merging three layers (see specs data-model): Home Assistant's own
// reported defaults, then the keys of the current stable config the operator
// does not recognise (passed through so a newer HA's options are not reset), then
// the http: section from the resource. Trusted proxies are canonicalised so the
// comparison in httpConfigEqual does not report a spurious difference.
func desiredHTTPConfig(resp *haclient.HTTPConfigResponse, httpSection haclient.HTTPConfigData) haclient.HTTPConfigData {
	out := make(haclient.HTTPConfigData)

	// Layer 1: Home Assistant's built-in defaults.
	for k, v := range resp.Default.StrippedMetadata() {
		out[k] = v
	}
	// Layer 2: pass through keys of stable the operator does not manage.
	for k, v := range resp.Stable.StrippedMetadata() {
		if _, known := httpKnownKeys[k]; !known {
			out[k] = v
		}
	}
	// Layer 3: the resource's http: section wins.
	for k, v := range httpSection {
		out[k] = v
	}

	canonicalizeTrustedProxies(out)
	return out
}

// httpConfigEqual reports whether the desired configuration matches what Home
// Assistant already holds, comparing meaningful fields only (metadata stripped)
// and after canonicalising trusted proxies on both sides.
func httpConfigEqual(desired, other haclient.HTTPConfigData) bool {
	a := desired.StrippedMetadata()
	b := other.StrippedMetadata()
	canonicalizeTrustedProxies(a)
	canonicalizeTrustedProxies(b)
	return reflect.DeepEqual(normalizeForCompare(a), normalizeForCompare(b))
}

// normalizeForCompare coerces YAML/JSON numeric and list representations so that
// e.g. int(8123) from a decoded resource and float64(8123) from a decoded JSON
// response compare equal.
func normalizeForCompare(d haclient.HTTPConfigData) map[string]string {
	out := make(map[string]string, len(d))
	for k, v := range d {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}

// reconcileHTTPConfig is the last step of the configuration reconcile. It never
// returns an error that aborts the reconcile: a transient failure talking to
// Home Assistant must not freeze the ConfigMap or the rest of the status.
func (r *HomeAssistantConfigurationReconciler) reconcileHTTPConfig(
	ctx context.Context,
	config *hav1.HomeAssistantConfiguration,
	ha *hav1.HomeAssistant,
	decision httpConfigDecision,
	httpSection haclient.HTTPConfigData,
	httpReadable bool,
) ctrl.Result {
	log := logf.FromContext(ctx)
	client := coordinatedHAClient(ctx, ha, r.Coordination, r.Client, func(string) *haclient.Client {
		return r.httpConfigClient(ha)
	})

	switch decision.path {
	case httpPathUndetermined:
		r.setHTTPConfigStatus(ctx, config, "", metav1.ConditionUnknown,
			reasonHTTPConfigWaiting, "Waiting for Home Assistant to become reachable")
		return ctrl.Result{RequeueAfter: 30 * time.Second}

	case httpPathYAML:
		r.setHTTPConfigStatus(ctx, config, hav1.HTTPConfigSourceYAML, metav1.ConditionTrue,
			reasonHTTPConfigManagedInYAML,
			"Home Assistant has no http config API; managed through configuration.yaml")
		return ctrl.Result{}
	}

	// httpPathAPI from here on.
	if !httpReadable {
		// The http: section is an external include the operator cannot read, so
		// it cannot be delivered through the API. It stays in configuration.yaml,
		// where this Home Assistant version ignores it — the http settings are
		// not in effect until the user inlines the keys.
		r.setHTTPConfigStatus(ctx, config, hav1.HTTPConfigSourceYAML, metav1.ConditionFalse,
			reasonHTTPConfigUnreadable,
			"http: is an external include the operator cannot deliver via the API; "+
				"Home Assistant is ignoring it. Inline the keys into spec.configuration "+
				"so the operator can apply them.")
		return ctrl.Result{}
	}

	resp := decision.resp
	desired := desiredHTTPConfig(resp, httpSection)

	// A pending configuration is in play.
	if resp.Pending != nil {
		if msg := resp.PendingError(); msg != "" {
			// Home Assistant rejected a configuration. Clear it so a corrected
			// one can be sent next reconcile, and tell the user why.
			if _, err := client.ConfigureHTTPConfig(ctx, decision.token, nil); err != nil {
				log.V(1).Info("failed to clear rejected pending http config", "error", err)
			}
			r.recordHTTPConfigEvent(config, corev1.EventTypeWarning, eventHTTPConfigRejected,
				"Home Assistant rejected the http configuration: "+msg)
			r.setHTTPConfigStatus(ctx, config, hav1.HTTPConfigSourceAPI, metav1.ConditionFalse,
				reasonHTTPConfigRejected, "Home Assistant rejected the http configuration: "+msg)
			return ctrl.Result{RequeueAfter: httpConfigRequeueAfterConfigure}
		}
		if httpConfigEqual(desired, resp.Pending) {
			if err := client.PromoteHTTPConfig(ctx, decision.token); err != nil {
				log.V(1).Info("failed to promote pending http config", "error", err)
				r.setHTTPConfigStatus(ctx, config, hav1.HTTPConfigSourceAPI, metav1.ConditionFalse,
					reasonHTTPConfigRejected, "Failed to confirm http configuration: "+err.Error())
				return ctrl.Result{RequeueAfter: httpConfigRequeueAfterConfigure}
			}
			r.setHTTPConfigStatus(ctx, config, hav1.HTTPConfigSourceAPI, metav1.ConditionTrue,
				reasonHTTPConfigApplied, "http configuration applied via the Home Assistant API")
			return ctrl.Result{}
		}
		// A pending change the operator did not send — leave it to the user's
		// confirmation (or Home Assistant's auto-revert). Do not promote it.
		r.recordHTTPConfigEvent(config, corev1.EventTypeWarning, eventHTTPConfigForeignChange,
			"An http configuration change was made in the Home Assistant UI; the operator "+
				"will not confirm it. The resource is the source of truth for http settings.")
		r.setHTTPConfigStatus(ctx, config, hav1.HTTPConfigSourceAPI, metav1.ConditionFalse,
			reasonHTTPConfigForeign,
			"An unconfirmed http configuration change from outside the operator is in effect")
		return ctrl.Result{RequeueAfter: httpConfigRequeueAfterConfigure}
	}

	// No pending change.
	if httpConfigEqual(desired, resp.Stable) {
		r.setHTTPConfigStatus(ctx, config, hav1.HTTPConfigSourceAPI, metav1.ConditionTrue,
			reasonHTTPConfigApplied, "http configuration applied via the Home Assistant API")
		return ctrl.Result{}
	}

	// Send the desired configuration; Home Assistant applies it as pending and
	// restarts itself if needed. Promotion happens on the next reconcile once the
	// pending matches.
	restart, err := client.ConfigureHTTPConfig(ctx, decision.token, desired)
	if err != nil {
		log.V(1).Info("failed to configure http config", "error", err)
		r.setHTTPConfigStatus(ctx, config, hav1.HTTPConfigSourceAPI, metav1.ConditionFalse,
			reasonHTTPConfigRejected, "Failed to send http configuration: "+err.Error())
		return ctrl.Result{RequeueAfter: httpConfigRequeueAfterConfigure}
	}
	log.Info("sent http configuration to Home Assistant", "willRestart", restart)
	r.setHTTPConfigStatus(ctx, config, hav1.HTTPConfigSourceAPI, metav1.ConditionUnknown,
		reasonHTTPConfigWaiting, "http configuration sent; waiting to confirm")
	return ctrl.Result{RequeueAfter: httpConfigRequeueAfterConfigure}
}

// setHTTPConfigStatus mutates the source field and the HTTPConfigReady condition
// on the in-memory object. The caller persists status once, after this step.
func (r *HomeAssistantConfigurationReconciler) setHTTPConfigStatus(
	_ context.Context,
	config *hav1.HomeAssistantConfiguration,
	source hav1.HTTPConfigSource,
	status metav1.ConditionStatus,
	reason, message string,
) {
	if source != "" {
		config.Status.HTTPConfigSource = source
	}
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               conditionHTTPConfigReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: config.Generation,
	})
}

func (r *HomeAssistantConfigurationReconciler) recordHTTPConfigEvent(
	config *hav1.HomeAssistantConfiguration, eventType, reason, message string,
) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(config, nil, eventType, reason, reason, "%s", message)
}
