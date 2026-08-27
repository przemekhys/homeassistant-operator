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
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// certificateGVK is the cert-manager Certificate GroupVersionKind the operator
// creates as unstructured (avoiding a heavyweight cert-manager module import).
var certificateGVK = schema.GroupVersionKind{
	Group:   certManagerGroup,
	Version: certManagerVersion,
	Kind:    certManagerKind,
}

// nativeTLS returns the native TLS alpha spec, or nil when not configured.
func nativeTLS(ha *hav1.HomeAssistant) *hav1.NativeTLSAlphaSpec {
	if ha.Spec.Alpha != nil && ha.Spec.Alpha.TLS != nil {
		return ha.Spec.Alpha.TLS.Native
	}
	return nil
}

// nativeTLSCertificateName is the operator-managed Certificate/Secret name for
// native TLS.
func nativeTLSCertificateName(ha *hav1.HomeAssistant) string {
	return ha.Name + "-native-tls"
}

// serviceFQDN returns the in-cluster DNS name of the Home Assistant Service, used
// as a certificate SAN so the operator can verify HA over HTTPS.
func serviceFQDN(ha *hav1.HomeAssistant) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", ha.Name, ha.Namespace)
}

// nativeTLSSecretName returns the Secret holding the native TLS material: the
// bring-your-own Secret when set, otherwise the operator-managed one.
func nativeTLSSecretName(ha *hav1.HomeAssistant) string {
	if n := nativeTLS(ha); n != nil && n.SecretName != "" {
		return n.SecretName
	}
	return nativeTLSCertificateName(ha)
}

// nativeTLSActive reports whether native TLS is enabled AND the certificate is
// ready (TLSReady=True). This single predicate gates the pod mount, the config
// injection and the operator→HA scheme so they flip together, avoiding a window
// where HA serves HTTPS but the operator still speaks HTTP (or vice versa).
func nativeTLSActive(ha *hav1.HomeAssistant) bool {
	n := nativeTLS(ha)
	return n != nil && n.Enabled && meta.IsStatusConditionTrue(ha.Status.Conditions, conditionTLSReady)
}

// haScheme returns "https" when native TLS is active, else "http".
func haScheme(ha *hav1.HomeAssistant) string {
	if nativeTLSActive(ha) {
		return "https"
	}
	return "http"
}

// loadNativeTLSCA reads the CA bundle (ca.crt) from the native TLS Secret so the
// operator can trust Home Assistant over HTTPS. Returns nil when the Secret or key
// is absent (e.g. a public issuer where system roots suffice).
func loadNativeTLSCA(ctx context.Context, c client.Client, ha *hav1.HomeAssistant) []byte {
	s := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Name: nativeTLSSecretName(ha), Namespace: ha.Namespace}, s); err != nil {
		return nil
	}
	return s.Data["ca.crt"]
}

// newHAClientForHA builds a Home Assistant API client for ha, honoring the native
// TLS scheme and CA trust. override (when non-nil, e.g. in tests) supplies the
// underlying client for a given base URL. InsecureSkipVerify is never used.
func newHAClientForHA(
	ctx context.Context, c client.Client, ha *hav1.HomeAssistant, override func(string) *haclient.Client,
) *haclient.Client {
	return newHAClientForHAScheme(ctx, c, ha, override, nativeTLSActive(ha))
}

// newHAClientForHAScheme is newHAClientForHA with the https/http decision
// supplied explicitly (useHTTPS) instead of derived from
// nativeTLSActive(ha)/TLSReady. Needed by nativeTLSHealthy: it runs while
// TLSReady is still False (a http/config/configure rotation is in flight),
// so gating its own scheme on TLSReady would be circular — the health check
// that must succeed BEFORE TLSReady can flip to True would then never be
// allowed to speak HTTPS in the first place.
func newHAClientForHAScheme(
	ctx context.Context, c client.Client, ha *hav1.HomeAssistant, override func(string) *haclient.Client,
	useHTTPS bool,
) *haclient.Client {
	return buildHAClient(ctx, c, ha, override, buildHomeAssistantURLWithScheme(ha, schemeName(useHTTPS)), useHTTPS)
}

// haPodIP returns the IP of the HA pod (<ha-name>-0), or "" when the pod or
// its IP isn't known yet (caller treats that like any other unreachable
// address — the resulting dial failure is handled the same way as today).
func haPodIP(ctx context.Context, c client.Client, ha *hav1.HomeAssistant) string {
	pod := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKey{Name: ha.Name + "-0", Namespace: ha.Namespace}, pod); err != nil {
		return ""
	}
	return pod.Status.PodIP
}

// newHAPodClientForHA is newHAClientForHAScheme addressed directly at the HA
// pod's IP on its fixed container port (defaultPort) instead of through the
// Service ClusterIP. reconcileHTTPConfigViaWS and nativeTLSHealthy use this
// instead of newHAClientForHA/newHAClientForHAScheme: HA can restart its own
// process in response to http/config/configure ("restart": true per the
// contract), and while it is live-trialing a pending native-TLS config the
// resulting protocol change makes the pod (correctly, by design — see
// buildStatefulSet's ReadinessProbe comment) fail its Service readiness
// probe. That removes it from the Service's endpoints, and a Service with
// zero ready endpoints has its ClusterIP actively refuse new connections
// (kube-proxy's standard behavior) — unreachable exactly when this code most
// needs to reach HA to confirm and promote that same pending config. Talking
// to the pod's own IP sidesteps the Service entirely, since observing and
// fixing that state is this code's whole job. Not used by any other
// controller: everything else legitimately wants "is HA reachable as a
// normal, ready backend", which the Service already answers correctly.
func newHAPodClientForHA(
	ctx context.Context, c client.Client, ha *hav1.HomeAssistant, override func(string) *haclient.Client,
	useHTTPS bool,
) *haclient.Client {
	url := fmt.Sprintf("%s://%s:%d", schemeName(useHTTPS), haPodIP(ctx, c, ha), defaultPort)
	return buildHAClient(ctx, c, ha, override, url, useHTTPS)
}

// schemeName converts useHTTPS to the "http"/"https" URL scheme literal.
func schemeName(useHTTPS bool) string {
	if useHTTPS {
		return "https"
	}
	return "http"
}

// buildHAClient is the shared client-construction tail of
// newHAClientForHAScheme/newHAPodClientForHA: apply override (or the real
// haclient constructor) to url, then attach CA trust when useHTTPS.
// InsecureSkipVerify is never used.
func buildHAClient(
	ctx context.Context, c client.Client, ha *hav1.HomeAssistant, override func(string) *haclient.Client,
	url string, useHTTPS bool,
) *haclient.Client {
	var cl *haclient.Client
	if override != nil {
		cl = override(url)
	} else {
		cl = haclient.NewClient(url)
	}
	if useHTTPS {
		if ca := loadNativeTLSCA(ctx, c, ha); len(ca) > 0 {
			cl = cl.WithRootCAs(ca)
		}
	}
	return cl
}

// issuerRefMap normalizes an IssuerReference into the map cert-manager expects,
// applying the same defaults as the CRD (kind=Issuer, group=cert-manager.io).
// Returns nil for a nil reference (guards against an incoherent spec that the
// webhook would normally reject).
func issuerRefMap(ref *hav1.IssuerReference) map[string]interface{} {
	if ref == nil {
		return nil
	}
	kind := ref.Kind
	if kind == "" {
		kind = "Issuer"
	}
	group := ref.Group
	if group == "" {
		group = certManagerGroup
	}
	return map[string]interface{}{"name": ref.Name, "kind": kind, "group": group}
}

// desiredNativeCertificateSpec builds the cert-manager Certificate spec for the
// native TLS mode. The Service FQDN is always included as a SAN (R4).
func desiredNativeCertificateSpec(ha *hav1.HomeAssistant) map[string]interface{} {
	n := nativeTLS(ha)
	dnsNames := []interface{}{serviceFQDN(ha)}
	for _, d := range n.DNSNames {
		if d != serviceFQDN(ha) {
			dnsNames = append(dnsNames, d)
		}
	}
	return map[string]interface{}{
		"secretName": nativeTLSCertificateName(ha),
		"dnsNames":   dnsNames,
		"issuerRef":  issuerRefMap(n.IssuerRef),
	}
}

// certificateReady reports whether a cert-manager Certificate has a Ready=True
// status condition.
func certificateReady(cert *unstructured.Unstructured) bool {
	conds, found, err := unstructured.NestedSlice(cert.Object, "status", "conditions")
	if !found || err != nil {
		return false
	}
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == "Ready" && m["status"] == string(metav1.ConditionTrue) {
			return true
		}
	}
	return false
}

// ensureCertificate creates or updates a cert-manager Certificate (as
// unstructured) with the given name, SANs and issuer, and reports whether it has
// been issued (Ready). It is idempotent (get-or-create + update-on-drift) and
// sets an owner reference so the Certificate is garbage-collected with the HA.
func (r *HomeAssistantReconciler) ensureCertificate(
	ctx context.Context, ha *hav1.HomeAssistant, name string, dnsNames []string, ref *hav1.IssuerReference,
) (bool, error) {
	dns := make([]interface{}, 0, len(dnsNames))
	for _, d := range dnsNames {
		dns = append(dns, d)
	}
	desired := map[string]interface{}{
		"secretName": name,
		"dnsNames":   dns,
		"issuerRef":  issuerRefMap(ref),
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(certificateGVK)
	err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: ha.Namespace}, existing)
	switch {
	case apierrors.IsNotFound(err):
		cert := &unstructured.Unstructured{Object: map[string]interface{}{}}
		cert.SetGroupVersionKind(certificateGVK)
		cert.SetName(name)
		cert.SetNamespace(ha.Namespace)
		cert.Object["spec"] = desired
		if err := controllerutil.SetControllerReference(ha, cert, r.Scheme); err != nil {
			return false, err
		}
		if err := r.Create(ctx, cert); err != nil {
			return false, err
		}
		r.Recorder.Eventf(ha, nil, corev1.EventTypeNormal, eventCertificateRequested, eventCertificateRequested,
			"Requested certificate %q via cert-manager", name)
		return false, nil
	case err != nil:
		return false, err
	}

	// Update only the operator-managed fields on drift, preserving cert-manager
	// defaults (e.g. usages, privateKey) that live alongside them in the spec.
	spec, _, _ := unstructured.NestedMap(existing.Object, "spec")
	if spec == nil {
		spec = map[string]interface{}{}
	}
	changed := false
	for _, key := range []string{"secretName", "dnsNames", "issuerRef"} {
		if !reflect.DeepEqual(spec[key], desired[key]) {
			spec[key] = desired[key]
			changed = true
		}
	}
	if changed {
		existing.Object["spec"] = spec
		if err := r.Update(ctx, existing); err != nil {
			return false, err
		}
		// existing.status reflects the pre-change certificate and cert-manager has
		// not yet reacted to the spec update — reporting it ready here would be a
		// stale false positive. Report not-ready; the next reconcile re-fetches.
		return false, nil
	}
	return certificateReady(existing), nil
}

// deleteCertificate removes an operator-managed Certificate by name if present.
// Best-effort: NotFound is ignored, and a missing cert-manager CRD (NoMatchError)
// means there is nothing to delete.
func (r *HomeAssistantReconciler) deleteCertificate(ctx context.Context, ha *hav1.HomeAssistant, name string) error {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	cert.SetName(name)
	cert.SetNamespace(ha.Namespace)
	if err := r.Delete(ctx, cert); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		return err
	}
	return nil
}

// ensureNativeCertificate provisions the native TLS certificate (with the Service
// FQDN SAN) and reports whether it is issued.
func (r *HomeAssistantReconciler) ensureNativeCertificate(ctx context.Context, ha *hav1.HomeAssistant) (bool, error) {
	spec := desiredNativeCertificateSpec(ha)
	dnsNames := make([]string, 0)
	if raw, ok := spec["dnsNames"].([]interface{}); ok {
		for _, d := range raw {
			if s, ok := d.(string); ok {
				dnsNames = append(dnsNames, s)
			}
		}
	}
	return r.ensureCertificate(ctx, ha, nativeTLSCertificateName(ha), dnsNames, nativeTLS(ha).IssuerRef)
}

// deleteNativeCertificate removes the operator-managed native TLS Certificate.
func (r *HomeAssistantReconciler) deleteNativeCertificate(ctx context.Context, ha *hav1.HomeAssistant) error {
	return r.deleteCertificate(ctx, ha, nativeTLSCertificateName(ha))
}

// certManagerAvailable reports whether cert-manager CRDs are installed on the
// cluster, without importing cert-manager types or requiring RBAC on its
// resources. The result is cached for certManagerDetectionTTL to avoid hitting
// discovery on every reconcile; the cache is a pure optimization and its loss is
// safely recovered by the next reconcile (constitution principle IV).
//
// Because the controller-runtime RESTMapper refreshes on a miss, a cert-manager
// installed after the operator started is picked up once the TTL elapses — this
// is what lets an enabled TLS mode become active without restarting the operator.
func (r *HomeAssistantReconciler) certManagerAvailable(_ context.Context) (bool, error) {
	r.certMgrMu.Lock()
	defer r.certMgrMu.Unlock()

	if !r.certMgrCheckedAt.IsZero() && time.Since(r.certMgrCheckedAt) < certManagerDetectionTTL {
		return r.certMgrAvailable, nil
	}

	_, err := r.RESTMapper().RESTMapping(
		schema.GroupKind{Group: certManagerGroup, Kind: certManagerKind},
		certManagerVersion,
	)
	switch {
	case err == nil:
		r.certMgrAvailable = true
	case meta.IsNoMatchError(err):
		r.certMgrAvailable = false
	default:
		// Transient discovery error — do not cache, let the caller retry.
		return false, err
	}
	r.certMgrCheckedAt = time.Now()
	return r.certMgrAvailable, nil
}

// certManagerRequired reports whether the HomeAssistant spec requests any TLS
// mode that needs cert-manager to issue a certificate. Bring-your-own Secret
// modes (SecretName set) do not need cert-manager and return false.
func certManagerRequired(ha *hav1.HomeAssistant) bool {
	if a := ha.Spec.Alpha; a != nil {
		if a.TLS != nil && a.TLS.Native != nil && a.TLS.Native.Enabled && a.TLS.Native.SecretName == "" {
			return true
		}
	}
	if g := ha.Spec.Gateway; g != nil && g.Enabled && g.SecretName == "" && g.IssuerRef != nil {
		return true
	}
	if i := ha.Spec.Ingress; i != nil && i.TLS != nil && i.TLS.Enabled &&
		i.TLS.SecretName == "" && i.TLS.IssuerRef != nil {
		return true
	}
	return false
}

// reconcileTLS handles the cert-manager integration. For the graceful-degradation
// contract: when a TLS mode is requested but cert-manager is not
// installed, it records status conditions, emits an event and requeues — it never
// returns an error, so the rest of the reconcile and other resources keep working
// (constitution principle I). Certificate issuance and exposure are layered
// on top of the CertManagerAvailable gate established here.
func (r *HomeAssistantReconciler) reconcileTLS(ctx context.Context, ha *hav1.HomeAssistant) (ctrl.Result, error) {
	// Native TLS with a user-provided Secret needs no cert-manager.
	if handled, err := r.nativeTLSUsingProvidedSecret(ctx, ha); err != nil {
		return ctrl.Result{}, err
	} else if handled && !certManagerRequired(ha) {
		return ctrl.Result{}, nil
	}

	// No cert-manager-backed TLS requested → clean up any orphaned native
	// certificate (e.g. native TLS was just disabled) and return without noise.
	// Only attempt cleanup when cert-manager is present — without its CRD there is
	// nothing to delete (and no need to hit the API every reconcile).
	if !certManagerRequired(ha) {
		if n := nativeTLS(ha); n == nil || !n.Enabled {
			if available, _ := r.certManagerAvailable(ctx); available {
				if err := r.deleteNativeCertificate(ctx, ha); err != nil {
					return ctrl.Result{}, err
				}
			}
		}
		return ctrl.Result{}, nil
	}
	log := logf.FromContext(ctx)

	available, err := r.certManagerAvailable(ctx)
	if err != nil {
		// Transient detection failure: retry without erroring.
		log.V(1).Info("cert-manager detection failed; will retry", "error", err)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if !available {
		var changed bool
		if err := r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
			c := meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
				Type:               conditionCertManagerAvailable,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: h.Generation,
				Reason:             reasonCertManagerNotInstalled,
				Message:            "cert-manager is not installed; TLS is inactive and Home Assistant continues over HTTP",
			})
			c = meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
				Type:               conditionTLSReady,
				Status:             metav1.ConditionUnknown,
				ObservedGeneration: h.Generation,
				Reason:             reasonWaitingForCertManager,
				Message:            "Waiting for cert-manager to become available",
			}) || c
			changed = c
			return c
		}); err != nil {
			return ctrl.Result{}, err
		}
		if changed {
			r.Recorder.Eventf(ha, nil, corev1.EventTypeWarning,
				eventCertManagerUnavailable, eventCertManagerUnavailable,
				"cert-manager not installed; requested TLS is inactive, serving HTTP")
		}
		// Requeue so a later cert-manager install is picked up.
		return ctrl.Result{RequeueAfter: certManagerDetectionTTL}, nil
	}

	// cert-manager available: record availability, then provision per-mode.
	nativeEnabled := false
	certReady := false
	if n := nativeTLS(ha); n != nil && n.Enabled && n.SecretName == "" {
		nativeEnabled = true
		ready, err := r.ensureNativeCertificate(ctx, ha)
		if err != nil {
			return ctrl.Result{}, err
		}
		certReady = ready
	}

	if err := r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
		c := meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
			Type:               conditionCertManagerAvailable,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: h.Generation,
			Reason:             reasonCertManagerInstalled,
			Message:            "cert-manager is installed",
		})
		if !certReady {
			c = meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
				Type:               conditionTLSReady,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: h.Generation,
				Reason:             reasonCertificateNotIssued,
				Message:            "Waiting for cert-manager to issue the native TLS certificate",
			}) || c
		}
		log.V(1).Info("reconcileTLS: cert-manager/native TLS condition mutate",
			"changed", c, "nativeEnabled", nativeEnabled, "certReady", certReady,
			"resourceVersion", h.ResourceVersion, "generation", h.Generation)
		return c
	}); err != nil {
		log.Error(err, "reconcileTLS: status update failed after retry")
		return ctrl.Result{}, err
	}

	if !nativeEnabled {
		return ctrl.Result{}, nil
	}
	if !certReady {
		// Poll until the certificate is issued.
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// Certificate material ready: nothing left to do here. Activation
	// (http/config/*, TLSReady from here on) is handled by
	// reconcileHTTPConfigViaWS, called once per reconcile for every
	// HomeAssistant from the main Reconcile() loop — that step is not gated
	// on native TLS specifically, so it moved out of this TLS-only function.
	return ctrl.Result{}, nil
}

// nativeTLSUsingProvidedSecret handles the bring-your-own native TLS case (a
// user-provided Secret, no cert-manager). It removes any operator-managed
// certificate and, once the Secret is valid, hands off to the same WS-first
// activation flow as the cert-manager path — identical behavior regardless
// of certificate source. Returns true when it handled the resource (whether
// or not the Secret was valid).
func (r *HomeAssistantReconciler) nativeTLSUsingProvidedSecret(
	ctx context.Context, ha *hav1.HomeAssistant,
) (bool, error) {
	n := nativeTLS(ha)
	if n == nil || !n.Enabled || n.SecretName == "" {
		return false, nil
	}
	secretName := n.SecretName

	secret := &corev1.Secret{}
	getErr := r.Get(ctx, client.ObjectKey{Name: secretName, Namespace: ha.Namespace}, secret)
	if getErr != nil || len(secret.Data["tls.crt"]) == 0 || len(secret.Data["tls.key"]) == 0 {
		if err := r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
			return meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
				Type:               conditionTLSReady,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: h.Generation,
				Reason:             reasonProvidedSecretInvalid,
				Message:            "Secret " + secretName + " is missing or does not contain tls.crt/tls.key",
			})
		}); err != nil {
			return true, err
		}
		return true, nil
	}

	if err := r.deleteNativeCertificate(ctx, ha); err != nil {
		return false, err
	}
	// Secret valid: nothing left to do here — see the comment in reconcileTLS's
	// cert-manager branch above (activation moved to reconcileHTTPConfigViaWS).
	return true, nil
}
