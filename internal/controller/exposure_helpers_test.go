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
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

func newExposureReconciler(t *testing.T, certManagerPresent bool, objs ...client.Object) *HomeAssistantReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatalf("add hav1: %v", err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add networkingv1: %v", err)
	}
	for _, gvk := range []schema.GroupVersionKind{certificateGVK, httpRouteGVK, gatewayGVK} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		lgvk := gvk
		lgvk.Kind += "List"
		scheme.AddKnownTypeWithName(lgvk, &unstructured.UnstructuredList{})
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(restMapperWithCertManager(certManagerPresent)).
		WithObjects(objs...).
		WithStatusSubresource(&hav1.HomeAssistant{}).
		Build()
	return &HomeAssistantReconciler{Client: cl, Scheme: scheme, Recorder: events.NewFakeRecorder(16)}
}

func ingressHA(name string, issuer bool) *hav1.HomeAssistant {
	tls := &hav1.IngressTLSSpec{Enabled: true}
	if issuer {
		tls.IssuerRef = &hav1.IssuerReference{Name: "le", Kind: "ClusterIssuer"}
	}
	return &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{
			Ingress: &hav1.IngressSpec{Enabled: true, Host: "ha.example.com", TLS: tls},
		},
	}
}

func getUnstructured(
	t *testing.T, r *HomeAssistantReconciler, gvk schema.GroupVersionKind, name string,
) (*unstructured.Unstructured, error) {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u, r.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "default"}, u)
}

func managedGatewayHA(name, gatewayClassName string) *hav1.HomeAssistant {
	return &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{Gateway: &hav1.GatewaySpec{
			Enabled:          true,
			Host:             name + ".example.com",
			ManageGateway:    true,
			GatewayClassName: gatewayClassName,
		}},
	}
}

func TestReconcileExposureManagedGatewayClass(t *testing.T) {
	t.Run("copies a selected class without looking it up", func(t *testing.T) {
		ha := managedGatewayHA("home", "traefik")
		r := newExposureReconciler(t, false, ha)

		if err := r.reconcileExposure(context.Background(), ha); err != nil {
			t.Fatalf("reconcileExposure error: %v", err)
		}
		gateway, err := getUnstructured(t, r, gatewayGVK, managedGatewayName(ha))
		if err != nil {
			t.Fatalf("expected managed Gateway created: %v", err)
		}
		className, _, _ := unstructured.NestedString(gateway.Object, "spec", "gatewayClassName")
		if className != "traefik" {
			t.Fatalf("expected gatewayClassName traefik, got %q", className)
		}
	})

	t.Run("allows two instances to share a selected class", func(t *testing.T) {
		first := managedGatewayHA("first", "traefik")
		second := managedGatewayHA("second", "traefik")
		r := newExposureReconciler(t, false, first, second)

		for _, ha := range []*hav1.HomeAssistant{first, second} {
			if err := r.reconcileExposure(context.Background(), ha); err != nil {
				t.Fatalf("reconcileExposure(%s) error: %v", ha.Name, err)
			}
			gateway, err := getUnstructured(t, r, gatewayGVK, managedGatewayName(ha))
			if err != nil {
				t.Fatalf("expected managed Gateway for %s: %v", ha.Name, err)
			}
			className, _, _ := unstructured.NestedString(gateway.Object, "spec", "gatewayClassName")
			if className != "traefik" {
				t.Fatalf("expected %s Gateway to use traefik, got %q", ha.Name, className)
			}
		}
	})
}

func TestReconcileExposureManagedGatewayDefaultClass(t *testing.T) {
	t.Run("omitted class defaults to traefik", func(t *testing.T) {
		ha := managedGatewayHA("legacy-home", "")
		r := newExposureReconciler(t, false, ha)

		if err := r.reconcileExposure(context.Background(), ha); err != nil {
			t.Fatalf("reconcileExposure error: %v", err)
		}
		gateway, err := getUnstructured(t, r, gatewayGVK, managedGatewayName(ha))
		if err != nil {
			t.Fatalf("expected managed Gateway created: %v", err)
		}
		className, _, _ := unstructured.NestedString(gateway.Object, "spec", "gatewayClassName")
		if className != defaultGatewayClassName {
			t.Fatalf("expected default gatewayClassName %q, got %q", defaultGatewayClassName, className)
		}
	})

	t.Run("external parent takes precedence over managed class", func(t *testing.T) {
		ha := managedGatewayHA("external-home", "cilium")
		ha.Spec.Gateway.ParentRef = &hav1.GatewayParentRef{
			Name: "shared-gateway", Namespace: "gateway-system", SectionName: "https",
		}
		r := newExposureReconciler(t, false, ha)

		if err := r.reconcileExposure(context.Background(), ha); err != nil {
			t.Fatalf("reconcileExposure error: %v", err)
		}
		if _, err := getUnstructured(t, r, gatewayGVK, managedGatewayName(ha)); err == nil {
			t.Fatal("expected no managed Gateway when parentRef is configured")
		}
		route, err := getUnstructured(t, r, httpRouteGVK, ha.Name)
		if err != nil {
			t.Fatalf("expected HTTPRoute created: %v", err)
		}
		parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
		if len(parents) != 1 {
			t.Fatalf("expected one external parentRef, got %#v", parents)
		}
		parent, ok := parents[0].(map[string]interface{})
		if !ok || parent["name"] != "shared-gateway" || parent["namespace"] != "gateway-system" {
			t.Fatalf("unexpected external parentRef: %#v", parents)
		}
	})
}

func TestReconcileExposureManagedGatewayClassUpdatesInPlace(t *testing.T) {
	ha := managedGatewayHA("mutable-home", "")
	gateway := &unstructured.Unstructured{}
	gateway.SetGroupVersionKind(gatewayGVK)
	gateway.SetName(managedGatewayName(ha))
	gateway.SetNamespace(ha.Namespace)
	gateway.SetUID(types.UID("managed-gateway-uid"))
	gateway.Object["spec"] = map[string]interface{}{}
	r := newExposureReconciler(t, false, ha, gateway)

	transitions := []struct {
		configuredClass string
		wantClass       string
	}{
		{wantClass: defaultGatewayClassName},
		{configuredClass: "traefik", wantClass: "traefik"},
		{configuredClass: "cilium", wantClass: "cilium"},
		{wantClass: defaultGatewayClassName},
	}
	for _, transition := range transitions {
		ha.Spec.Gateway.GatewayClassName = transition.configuredClass
		wantClass := transition.wantClass
		if err := r.reconcileExposure(context.Background(), ha); err != nil {
			t.Fatalf("reconcileExposure for class %q: %v", wantClass, err)
		}
		current, err := getUnstructured(t, r, gatewayGVK, managedGatewayName(ha))
		if err != nil {
			t.Fatalf("get managed Gateway: %v", err)
		}
		if current.GetUID() != types.UID("managed-gateway-uid") {
			t.Fatalf("expected Gateway UID to remain stable, got %q", current.GetUID())
		}
		className, _, _ := unstructured.NestedString(current.Object, "spec", "gatewayClassName")
		if className != wantClass {
			t.Fatalf("expected gatewayClassName %q, got %q", wantClass, className)
		}
	}
}

// TestReconcileExposureIngress: Ingress enabled with an issuerRef and
// cert-manager present → Ingress + Certificate created, ExposureReady=True.
func TestReconcileExposureIngress(t *testing.T) {
	ha := ingressHA("home", true)
	r := newExposureReconciler(t, true, ha)

	if err := r.reconcileExposure(context.Background(), ha); err != nil {
		t.Fatalf("reconcileExposure error: %v", err)
	}

	ing := &networkingv1.Ingress{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: "home", Namespace: "default"}, ing); err != nil {
		t.Fatalf("expected Ingress created: %v", err)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != "ha.example.com" {
		t.Fatalf("unexpected Ingress rules: %+v", ing.Spec.Rules)
	}
	if len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != "home-ingress-tls" {
		t.Fatalf("expected TLS secret home-ingress-tls, got %+v", ing.Spec.TLS)
	}
	if _, err := getUnstructured(t, r, certificateGVK, "home-ingress-tls"); err != nil {
		t.Fatalf("expected ingress Certificate created: %v", err)
	}
	if !meta.IsStatusConditionTrue(ha.Status.Conditions, conditionExposureReady) {
		t.Fatal("expected ExposureReady=True")
	}
}

// TestReconcileExposureIngressNoCertManager: without cert-manager the Ingress is
// still created (HTTP), but no Certificate is provisioned.
func TestReconcileExposureIngressNoCertManager(t *testing.T) {
	ha := ingressHA("home", true)
	r := newExposureReconciler(t, false, ha)

	if err := r.reconcileExposure(context.Background(), ha); err != nil {
		t.Fatalf("reconcileExposure error: %v", err)
	}
	err := r.Get(context.Background(), client.ObjectKey{Name: "home", Namespace: "default"}, &networkingv1.Ingress{})
	if err != nil {
		t.Fatalf("expected Ingress created even without cert-manager: %v", err)
	}
	if _, err := getUnstructured(t, r, certificateGVK, "home-ingress-tls"); err == nil {
		t.Fatal("expected no Certificate without cert-manager")
	}
}

// TestReconcileExposureGatewayRoute: Gateway enabled with a parentRef and issuer →
// HTTPRoute + Certificate created.
func TestReconcileExposureGatewayRoute(t *testing.T) {
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{Gateway: &hav1.GatewaySpec{
			Enabled:   true,
			Host:      "ha.example.com",
			IssuerRef: &hav1.IssuerReference{Name: "le", Kind: "ClusterIssuer"},
			ParentRef: &hav1.GatewayParentRef{Name: "traefik", Namespace: "gateway", SectionName: "https"},
		}},
	}
	r := newExposureReconciler(t, true, ha)

	if err := r.reconcileExposure(context.Background(), ha); err != nil {
		t.Fatalf("reconcileExposure error: %v", err)
	}
	route, err := getUnstructured(t, r, httpRouteGVK, "home")
	if err != nil {
		t.Fatalf("expected HTTPRoute created: %v", err)
	}
	hostnames, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	if len(hostnames) != 1 || hostnames[0] != "ha.example.com" {
		t.Fatalf("unexpected HTTPRoute hostnames: %v", hostnames)
	}
	if _, err := getUnstructured(t, r, certificateGVK, "home-gateway-tls"); err != nil {
		t.Fatalf("expected gateway Certificate created: %v", err)
	}
	if !meta.IsStatusConditionTrue(ha.Status.Conditions, conditionExposureReady) {
		t.Fatal("expected ExposureReady=True")
	}
}

// TestReconcileExposureGatewayRouteFilters: declared filters are applied,
// in order, to the managed HTTPRoute's single rule.
func TestReconcileExposureGatewayRouteFilters(t *testing.T) {
	statusCode := 301
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{Gateway: &hav1.GatewaySpec{
			Enabled:   true,
			Host:      "ha.example.com",
			ParentRef: &hav1.GatewayParentRef{Name: "traefik", Namespace: "gateway", SectionName: "https"},
			Filters: []hav1.HTTPRouteFilter{
				{
					Type: "RequestRedirect",
					RequestRedirect: &hav1.HTTPRequestRedirectFilter{
						Scheme:     ptrTo("https"),
						StatusCode: &statusCode,
					},
				},
				{
					Type: "ResponseHeaderModifier",
					ResponseHeaderModifier: &hav1.HTTPHeaderFilter{
						Set: []hav1.HTTPHeader{{Name: "X-Frame-Options", Value: "SAMEORIGIN"}},
					},
				},
			},
		}},
	}
	r := newExposureReconciler(t, true, ha)

	if err := r.reconcileExposure(context.Background(), ha); err != nil {
		t.Fatalf("reconcileExposure error: %v", err)
	}
	route, err := getUnstructured(t, r, httpRouteGVK, "home")
	if err != nil {
		t.Fatalf("expected HTTPRoute created: %v", err)
	}
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	if len(rules) != 1 {
		t.Fatalf("expected exactly one rule, got %d", len(rules))
	}
	rule, ok := rules[0].(map[string]interface{})
	if !ok {
		t.Fatalf("rule is not a map: %#v", rules[0])
	}
	rawFilters, ok := rule["filters"].([]interface{})
	if !ok {
		t.Fatalf("expected rule.filters to be a list, got %#v", rule["filters"])
	}
	if len(rawFilters) != 2 {
		t.Fatalf("expected 2 filters, got %d: %#v", len(rawFilters), rawFilters)
	}
	first, _ := rawFilters[0].(map[string]interface{})
	if first["type"] != "RequestRedirect" {
		t.Fatalf("expected first filter type RequestRedirect, got %#v", first)
	}
	redirect, _ := first["requestRedirect"].(map[string]interface{})
	if redirect["scheme"] != "https" {
		t.Fatalf("expected requestRedirect.scheme=https, got %#v", redirect)
	}
	second, _ := rawFilters[1].(map[string]interface{})
	if second["type"] != "ResponseHeaderModifier" {
		t.Fatalf("expected second filter type ResponseHeaderModifier, got %#v", second)
	}
	// Gateway API rejects a rule combining backendRefs with a RequestRedirect
	// filter, so the rule must not carry backendRefs here.
	if _, present := rule["backendRefs"]; present {
		t.Fatalf("expected no backendRefs on a rule with a RequestRedirect filter, got %#v", rule["backendRefs"])
	}
}

// TestReconcileExposureGatewayRouteFiltersWithoutRedirectKeepsBackendRefs:
// filters that don't include RequestRedirect must not affect backendRefs.
func TestReconcileExposureGatewayRouteFiltersWithoutRedirectKeepsBackendRefs(t *testing.T) {
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{Gateway: &hav1.GatewaySpec{
			Enabled:   true,
			Host:      "ha.example.com",
			ParentRef: &hav1.GatewayParentRef{Name: "traefik", Namespace: "gateway", SectionName: "https"},
			Filters: []hav1.HTTPRouteFilter{{
				Type: "ResponseHeaderModifier",
				ResponseHeaderModifier: &hav1.HTTPHeaderFilter{
					Set: []hav1.HTTPHeader{{Name: "X-Frame-Options", Value: "SAMEORIGIN"}},
				},
			}},
		}},
	}
	r := newExposureReconciler(t, true, ha)

	if err := r.reconcileExposure(context.Background(), ha); err != nil {
		t.Fatalf("reconcileExposure error: %v", err)
	}
	route, err := getUnstructured(t, r, httpRouteGVK, "home")
	if err != nil {
		t.Fatalf("expected HTTPRoute created: %v", err)
	}
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	rule, ok := rules[0].(map[string]interface{})
	if !ok {
		t.Fatalf("rule is not a map: %#v", rules[0])
	}
	if _, present := rule["backendRefs"]; !present {
		t.Fatal("expected backendRefs to be present when no RequestRedirect filter is declared")
	}
}

// TestReconcileExposureGatewayRouteNoFilters: omitting filters entirely must
// leave the route's rule without a "filters" key at all — byte-for-byte
// identical to the route produced before this feature existed.
func TestReconcileExposureGatewayRouteNoFilters(t *testing.T) {
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{Gateway: &hav1.GatewaySpec{
			Enabled:   true,
			Host:      "ha.example.com",
			ParentRef: &hav1.GatewayParentRef{Name: "traefik", Namespace: "gateway", SectionName: "https"},
		}},
	}
	r := newExposureReconciler(t, true, ha)

	if err := r.reconcileExposure(context.Background(), ha); err != nil {
		t.Fatalf("reconcileExposure error: %v", err)
	}
	route, err := getUnstructured(t, r, httpRouteGVK, "home")
	if err != nil {
		t.Fatalf("expected HTTPRoute created: %v", err)
	}
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	if len(rules) != 1 {
		t.Fatalf("expected exactly one rule, got %d", len(rules))
	}
	rule, ok := rules[0].(map[string]interface{})
	if !ok {
		t.Fatalf("rule is not a map: %#v", rules[0])
	}
	if _, present := rule["filters"]; present {
		t.Fatalf("expected no filters key on the rule, got %#v", rule["filters"])
	}
}

func ptrTo[T any](v T) *T { return &v }

// TestReconcileExposureCleanup: disabling Ingress removes the managed Ingress.
func TestReconcileExposureCleanup(t *testing.T) {
	existing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"}}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"}}
	r := newExposureReconciler(t, true, ha, existing)

	if err := r.reconcileExposure(context.Background(), ha); err != nil {
		t.Fatalf("reconcileExposure error: %v", err)
	}
	err := r.Get(context.Background(), client.ObjectKey{Name: "home", Namespace: "default"}, &networkingv1.Ingress{})
	if err == nil {
		t.Fatal("expected Ingress to be deleted when exposure disabled")
	}
}
