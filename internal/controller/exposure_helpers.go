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

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

// Gateway API GroupVersionKinds the operator manages as unstructured (avoiding a
// dependency on sigs.k8s.io/gateway-api and keeping graceful degradation simple —
// consistent with how cert-manager Certificates are handled).
const (
	gatewayAPIGroup         = "gateway.networking.k8s.io"
	defaultGatewayClassName = "traefik"
)

var (
	httpRouteGVK = schema.GroupVersionKind{Group: gatewayAPIGroup, Version: "v1", Kind: "HTTPRoute"}
	gatewayGVK   = schema.GroupVersionKind{Group: gatewayAPIGroup, Version: "v1", Kind: "Gateway"}
)

func ingressTLSCertificateName(ha *hav1.HomeAssistant) string { return ha.Name + "-ingress-tls" }
func gatewayTLSCertificateName(ha *hav1.HomeAssistant) string { return ha.Name + "-gateway-tls" }
func managedGatewayName(ha *hav1.HomeAssistant) string        { return ha.Name + "-gateway" }

func servicePort(ha *hav1.HomeAssistant) int32 {
	if ha.Spec.Service != nil && ha.Spec.Service.Port != 0 {
		return ha.Spec.Service.Port
	}
	return defaultHomeAssistantPort
}

// reconcileExposure manages Ingress and Gateway API exposure for Home Assistant,
// including the cert-manager Certificate backing the edge TLS. Missing cert-manager
// is never fatal: HTTP exposure is still created and the TLS condition reflects the
// degradation (handled in reconcileTLS). Disabled modes are cleaned up.
func (r *HomeAssistantReconciler) reconcileExposure(ctx context.Context, ha *hav1.HomeAssistant) error {
	available, err := r.certManagerAvailable(ctx)
	if err != nil {
		// Transient detection failure — treat cert-manager as unavailable (HTTP
		// exposure still works) and log, so it is distinguishable from "absent".
		logf.FromContext(ctx).V(1).Info("cert-manager detection failed during exposure", "error", err)
	}
	exposed := false

	// --- Ingress ---
	if ha.Spec.Ingress != nil && ha.Spec.Ingress.Enabled {
		if err := r.reconcileIngress(ctx, ha, available); err != nil {
			return err
		}
		exposed = true
	} else {
		if err := r.deleteIngress(ctx, ha); err != nil {
			return err
		}
		if err := r.deleteCertificate(ctx, ha, ingressTLSCertificateName(ha)); err != nil {
			return err
		}
	}

	// --- Gateway API ---
	if ha.Spec.Gateway != nil && ha.Spec.Gateway.Enabled {
		routed, err := r.reconcileGatewayRoute(ctx, ha, available)
		if err != nil {
			return err
		}
		// Only count as exposed when a route was actually attached (a parentRef or
		// managed Gateway existed) — an enabled Gateway with no attach point is not.
		exposed = exposed || routed
	} else {
		if err := r.deleteExposureResource(ctx, ha, httpRouteGVK, ha.Name); err != nil {
			return err
		}
		if err := r.deleteExposureResource(ctx, ha, gatewayGVK, managedGatewayName(ha)); err != nil {
			return err
		}
		if err := r.deleteCertificate(ctx, ha, gatewayTLSCertificateName(ha)); err != nil {
			return err
		}
	}

	return r.updateExposureReady(ctx, ha, exposed)
}

// updateExposureReady sets ExposureReady=True when exposure resources are active,
// and clears a previously-set condition when exposure is disabled (so a stale True
// does not linger). It adds no condition for resources that never enabled exposure.
func (r *HomeAssistantReconciler) updateExposureReady(ctx context.Context, ha *hav1.HomeAssistant, exposed bool) error {
	if exposed {
		log := logf.FromContext(ctx)
		message := "Exposure resources reconciled"
		haConfig, err := r.findHomeAssistantConfiguration(ctx, ha)
		if err != nil {
			log.V(1).Info("updateExposureReady: failed to look up HomeAssistantConfiguration for trusted-proxies status",
				"error", err)
			haConfig = nil
		}
		message += "; " + trustedProxiesStatusMessage(ha, haConfig)
		var changed bool
		if err := r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
			changed = meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
				Type:               conditionExposureReady,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: h.Generation,
				Reason:             reasonExposureReady,
				Message:            message,
			})
			log.V(1).Info("updateExposureReady: condition mutate",
				"changed", changed, "resourceVersion", h.ResourceVersion, "generation", h.Generation)
			return changed
		}); err != nil {
			log.Error(err, "updateExposureReady: status update failed after retry")
			return err
		}
		if changed {
			r.Recorder.Eventf(ha, nil, corev1.EventTypeNormal, eventExposureConfigured, eventExposureConfigured,
				"Exposure resources reconciled for %q", ha.Name)
		}
		return nil
	}
	return r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
		return meta.RemoveStatusCondition(&h.Status.Conditions, conditionExposureReady)
	})
}

// findHomeAssistantConfiguration returns the HomeAssistantConfiguration that
// references the given HomeAssistant (spec.homeAssistantRef.name), or nil if
// none exists yet. Shared with getGeneratedConfigMapName so both read the same
// sibling lookup rather than duplicating the List call.
func (r *HomeAssistantReconciler) findHomeAssistantConfiguration(
	ctx context.Context, ha *hav1.HomeAssistant,
) (*hav1.HomeAssistantConfiguration, error) {
	haConfigList := &hav1.HomeAssistantConfigurationList{}
	if err := r.List(ctx, haConfigList, client.InNamespace(ha.Namespace)); err != nil {
		return nil, err
	}
	for i := range haConfigList.Items {
		if haConfigList.Items[i].Spec.HomeAssistantRef.Name == ha.Name {
			return &haConfigList.Items[i], nil
		}
	}
	return nil, nil
}

// trustedProxiesStatusMessage phrases the trusted-proxies outcome for the
// ExposureReady condition message, covering all outcomes an exposed
// HomeAssistant can be in. The opt-out check reads ha.Spec directly
// (no cross-CRD read needed); distinguishing "applied" from "user-managed"
// requires the sibling HomeAssistantConfiguration's status, since only its
// reconciler knows whether the user had already set the keys themselves. A
// nil haConfig (lookup failed, or not created yet) or a haConfig that hasn't
// completed a reconcile yet (TrustedProxiesDefaulted still nil — the two
// controllers reconcile independently, so this is reachable in normal
// operation, not just on error) is reported as pending rather than
// misreported as "user-configured".
func trustedProxiesStatusMessage(ha *hav1.HomeAssistant, haConfig *hav1.HomeAssistantConfiguration) string {
	if ha.Spec.DisableDefaultTrustedProxies {
		return "default trusted proxies disabled (opt-out)"
	}
	if haConfig == nil || haConfig.Status.TrustedProxiesDefaulted == nil {
		return "trusted proxies status pending (HomeAssistantConfiguration not yet reconciled)"
	}
	if *haConfig.Status.TrustedProxiesDefaulted {
		return "default trusted proxies applied"
	}
	return "using user-configured trusted proxies"
}

// reconcileIngress creates/updates the operator-managed Ingress and its
// cert-manager Certificate (when an issuerRef is set and cert-manager is present).
func (r *HomeAssistantReconciler) reconcileIngress(
	ctx context.Context, ha *hav1.HomeAssistant, cmAvailable bool,
) error {
	in := ha.Spec.Ingress
	host := in.Host

	// TLS Secret: bring-your-own wins; otherwise the operator-issued Secret.
	var tlsSecret string
	if in.TLS != nil && in.TLS.Enabled {
		switch {
		case in.TLS.SecretName != "":
			tlsSecret = in.TLS.SecretName
		case in.TLS.IssuerRef != nil:
			tlsSecret = ingressTLSCertificateName(ha)
			if cmAvailable {
				err := r.ensureCertificate(ctx, ha, ingressTLSCertificateName(ha), []string{host}, in.TLS.IssuerRef)
				if err != nil {
					return err
				}
			}
		}
	}

	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: ha.Name, Namespace: ha.Namespace}}
	pathType := networkingv1.PathTypePrefix
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ingress, func() error {
		ingress.Annotations = in.Annotations
		ingress.Spec.IngressClassName = in.IngressClassName
		ingress.Spec.Rules = []networkingv1.IngressRule{{
			Host: host,
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{{
					Path:     "/",
					PathType: &pathType,
					Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
						Name: ha.Name,
						Port: networkingv1.ServiceBackendPort{Number: servicePort(ha)},
					}},
				}},
			}},
		}}
		if tlsSecret != "" {
			ingress.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{host}, SecretName: tlsSecret}}
		} else {
			ingress.Spec.TLS = nil
		}
		return controllerutil.SetControllerReference(ha, ingress, r.Scheme)
	})
	return err
}

func (r *HomeAssistantReconciler) deleteIngress(ctx context.Context, ha *hav1.HomeAssistant) error {
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: ha.Name, Namespace: ha.Namespace}}
	if err := r.Delete(ctx, ingress); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// reconcileGatewayRoute creates/updates the HTTPRoute (and, when manageGateway is
// set, a Gateway) plus the cert-manager Certificate for the listener. It returns
// true only when a route was actually attached to a parent; an enabled Gateway
// with neither parentRef nor manageGateway has no attach point and returns false.
func (r *HomeAssistantReconciler) reconcileGatewayRoute(
	ctx context.Context, ha *hav1.HomeAssistant, cmAvailable bool,
) (bool, error) {
	g := ha.Spec.Gateway

	var tlsSecret string
	if g.SecretName != "" {
		tlsSecret = g.SecretName
	} else if g.IssuerRef != nil {
		tlsSecret = gatewayTLSCertificateName(ha)
		if cmAvailable {
			err := r.ensureCertificate(ctx, ha, gatewayTLSCertificateName(ha), []string{g.Host}, g.IssuerRef)
			if err != nil {
				return false, err
			}
		}
	}

	// Determine the parent Gateway to attach the route to.
	parent := map[string]interface{}{}
	switch {
	case g.ParentRef != nil:
		parent["name"] = g.ParentRef.Name
		if g.ParentRef.Namespace != "" {
			parent["namespace"] = g.ParentRef.Namespace
		}
		if g.ParentRef.SectionName != "" {
			parent["sectionName"] = g.ParentRef.SectionName
		}
	case g.ManageGateway:
		if err := r.ensureManagedGateway(ctx, ha, tlsSecret); err != nil {
			return false, err
		}
		parent["name"] = managedGatewayName(ha)
		parent["sectionName"] = "https"
	default:
		// No parent to attach to — nothing to route yet.
		return false, nil
	}

	rule := map[string]interface{}{}
	// Gateway API rejects a rule that combines backendRefs with a
	// RequestRedirect filter (the redirect short-circuits before any backend
	// is ever reached), so omit backendRefs whenever one is present.
	if !hasRequestRedirectFilter(g.Filters) {
		rule["backendRefs"] = []interface{}{map[string]interface{}{
			"name": ha.Name,
			"port": int64(servicePort(ha)),
		}}
	}
	if filters := buildGatewayFilters(g.Filters); len(filters) > 0 {
		rule["filters"] = filters
	}

	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(httpRouteGVK)
	route.SetName(ha.Name)
	route.SetNamespace(ha.Namespace)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, route, func() error {
		route.Object["spec"] = map[string]interface{}{
			"parentRefs": []interface{}{parent},
			"hostnames":  []interface{}{g.Host},
			"rules":      []interface{}{rule},
		}
		return controllerutil.SetControllerReference(ha, route, r.Scheme)
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// hasRequestRedirectFilter reports whether filters contains a RequestRedirect
// entry, which Gateway API's HTTPRoute validation forbids combining with
// backendRefs on the same rule.
func hasRequestRedirectFilter(filters []hav1.HTTPRouteFilter) bool {
	for _, f := range filters {
		if f.Type == "RequestRedirect" {
			return true
		}
	}
	return false
}

// buildGatewayFilters translates spec.gateway.filters into the equivalent
// Gateway API HTTPRouteFilter unstructured shape, in declared order. Mirrors
// validateGatewayFilter's discriminated union: only the sub-object matching
// f.Type is ever rendered, regardless of which pointer fields happen to be
// set — the admission webhook already enforces that invariant, but this keeps
// the builder correct on its own rather than relying on it. An unrecognized
// Type renders with no sub-object at all.
func buildGatewayFilters(filters []hav1.HTTPRouteFilter) []interface{} {
	if len(filters) == 0 {
		return nil
	}
	out := make([]interface{}, 0, len(filters))
	for _, f := range filters {
		m := map[string]interface{}{"type": f.Type}
		switch f.Type {
		case "RequestHeaderModifier":
			if f.RequestHeaderModifier != nil {
				m["requestHeaderModifier"] = buildHTTPHeaderFilter(f.RequestHeaderModifier)
			}
		case "ResponseHeaderModifier":
			if f.ResponseHeaderModifier != nil {
				m["responseHeaderModifier"] = buildHTTPHeaderFilter(f.ResponseHeaderModifier)
			}
		case "RequestRedirect":
			if f.RequestRedirect != nil {
				m["requestRedirect"] = buildHTTPRequestRedirectFilter(f.RequestRedirect)
			}
		case "URLRewrite":
			if f.URLRewrite != nil {
				m["urlRewrite"] = buildHTTPURLRewriteFilter(f.URLRewrite)
			}
		}
		out = append(out, m)
	}
	return out
}

func buildHTTPHeaderFilter(h *hav1.HTTPHeaderFilter) map[string]interface{} {
	m := map[string]interface{}{}
	if len(h.Set) > 0 {
		m["set"] = buildHTTPHeaders(h.Set)
	}
	if len(h.Add) > 0 {
		m["add"] = buildHTTPHeaders(h.Add)
	}
	if len(h.Remove) > 0 {
		remove := make([]interface{}, len(h.Remove))
		for i, name := range h.Remove {
			remove[i] = name
		}
		m["remove"] = remove
	}
	return m
}

func buildHTTPHeaders(headers []hav1.HTTPHeader) []interface{} {
	out := make([]interface{}, len(headers))
	for i, h := range headers {
		out[i] = map[string]interface{}{"name": h.Name, "value": h.Value}
	}
	return out
}

func buildHTTPRequestRedirectFilter(f *hav1.HTTPRequestRedirectFilter) map[string]interface{} {
	m := map[string]interface{}{}
	if f.Scheme != nil {
		m["scheme"] = *f.Scheme
	}
	if f.Hostname != nil {
		m["hostname"] = *f.Hostname
	}
	if f.Path != nil {
		m["path"] = buildHTTPPathModifier(f.Path)
	}
	if f.Port != nil {
		m["port"] = int64(*f.Port)
	}
	if f.StatusCode != nil {
		m["statusCode"] = int64(*f.StatusCode)
	}
	return m
}

func buildHTTPURLRewriteFilter(f *hav1.HTTPURLRewriteFilter) map[string]interface{} {
	m := map[string]interface{}{}
	if f.Hostname != nil {
		m["hostname"] = *f.Hostname
	}
	if f.Path != nil {
		m["path"] = buildHTTPPathModifier(f.Path)
	}
	return m
}

func buildHTTPPathModifier(p *hav1.HTTPPathModifier) map[string]interface{} {
	m := map[string]interface{}{"type": p.Type}
	if p.ReplaceFullPath != nil {
		m["replaceFullPath"] = *p.ReplaceFullPath
	}
	if p.ReplacePrefixMatch != nil {
		m["replacePrefixMatch"] = *p.ReplacePrefixMatch
	}
	return m
}

// ensureManagedGateway creates/updates a Gateway with an HTTPS listener when the
// user opts into operator-managed Gateway (spec.gateway.manageGateway).
func (r *HomeAssistantReconciler) ensureManagedGateway(
	ctx context.Context, ha *hav1.HomeAssistant, tlsSecret string,
) error {
	gatewayClassName := ha.Spec.Gateway.GatewayClassName
	if gatewayClassName == "" {
		gatewayClassName = defaultGatewayClassName
	}
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	gw.SetName(managedGatewayName(ha))
	gw.SetNamespace(ha.Namespace)
	listener := map[string]interface{}{
		"name":     "https",
		"protocol": "HTTPS",
		"port":     int64(443),
		"hostname": ha.Spec.Gateway.Host,
	}
	if tlsSecret != "" {
		listener["tls"] = map[string]interface{}{
			"mode":            "Terminate",
			"certificateRefs": []interface{}{map[string]interface{}{"name": tlsSecret}},
		}
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, gw, func() error {
		gw.Object["spec"] = map[string]interface{}{
			"gatewayClassName": gatewayClassName,
			"listeners":        []interface{}{listener},
		}
		return controllerutil.SetControllerReference(ha, gw, r.Scheme)
	})
	return err
}

func (r *HomeAssistantReconciler) deleteExposureResource(
	ctx context.Context, ha *hav1.HomeAssistant, gvk schema.GroupVersionKind, name string,
) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetName(name)
	u.SetNamespace(ha.Namespace)
	if err := r.Delete(ctx, u); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		return err
	}
	return nil
}
