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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// restMapperWithCertManager returns a RESTMapper that either knows or does not
// know the cert-manager Certificate kind, to simulate cert-manager presence.
func restMapperWithCertManager(present bool) meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	if present {
		m.Add(schema.GroupVersionKind{
			Group:   certManagerGroup,
			Version: certManagerVersion,
			Kind:    certManagerKind,
		}, meta.RESTScopeNamespace)
	}
	return m
}

func newTLSTestReconciler(t *testing.T, certManagerPresent bool, objs ...client.Object) *HomeAssistantReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatalf("add hav1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 to scheme: %v", err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add hav1alpha1 to scheme: %v", err)
	}
	// Register the cert-manager Certificate GVK as unstructured so the fake
	// client can create/get it (the operator uses unstructured in production too).
	scheme.AddKnownTypeWithName(certificateGVK, &unstructured.Unstructured{})
	listGVK := certificateGVK
	listGVK.Kind += "List"
	scheme.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(restMapperWithCertManager(certManagerPresent)).
		WithObjects(objs...).
		WithStatusSubresource(&hav1.HomeAssistant{}).
		Build()
	return &HomeAssistantReconciler{
		Client:   cl,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(16),
	}
}

func nativeTLSHA(name string) *hav1.HomeAssistant {
	return &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{
			Alpha: &hav1.AlphaSpec{
				TLS: &hav1.TLSAlphaSpec{
					Native: &hav1.NativeTLSAlphaSpec{
						Enabled:   true,
						IssuerRef: &hav1.IssuerReference{Name: "test-issuer", Kind: "ClusterIssuer"},
					},
				},
			},
		},
	}
}

func TestCertManagerRequired(t *testing.T) {
	tests := []struct {
		name string
		ha   *hav1.HomeAssistant
		want bool
	}{
		{
			name: "no TLS requested",
			ha:   &hav1.HomeAssistant{},
			want: false,
		},
		{
			name: "native TLS enabled with issuer",
			ha:   nativeTLSHA("ha"),
			want: true,
		},
		{
			name: "native TLS enabled bring-your-own secret does not need cert-manager",
			ha: &hav1.HomeAssistant{Spec: hav1.HomeAssistantSpec{Alpha: &hav1.AlphaSpec{
				TLS: &hav1.TLSAlphaSpec{Native: &hav1.NativeTLSAlphaSpec{Enabled: true, SecretName: "byo"}},
			}}},
			want: false,
		},
		{
			name: "ingress TLS with issuerRef needs cert-manager",
			ha: &hav1.HomeAssistant{Spec: hav1.HomeAssistantSpec{Ingress: &hav1.IngressSpec{
				Enabled: true,
				TLS:     &hav1.IngressTLSSpec{Enabled: true, IssuerRef: &hav1.IssuerReference{Name: "i"}},
			}}},
			want: true,
		},
		{
			name: "ingress TLS with secretName is bring-your-own",
			ha: &hav1.HomeAssistant{Spec: hav1.HomeAssistantSpec{Ingress: &hav1.IngressSpec{
				Enabled: true,
				TLS:     &hav1.IngressTLSSpec{Enabled: true, SecretName: "byo"},
			}}},
			want: false,
		},
		{
			name: "gateway enabled without issuer does not need cert-manager",
			ha: &hav1.HomeAssistant{Spec: hav1.HomeAssistantSpec{
				Gateway: &hav1.GatewaySpec{Enabled: true, Host: "h"},
			}},
			want: false,
		},
		{
			name: "gateway enabled with issuer needs cert-manager",
			ha: &hav1.HomeAssistant{Spec: hav1.HomeAssistantSpec{
				Gateway: &hav1.GatewaySpec{Enabled: true, Host: "h", IssuerRef: &hav1.IssuerReference{Name: "i"}},
			}},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := certManagerRequired(tc.ha); got != tc.want {
				t.Fatalf("certManagerRequired = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCertManagerAvailable(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		r := newTLSTestReconciler(t, false)
		got, err := r.certManagerAvailable(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Fatal("expected cert-manager unavailable")
		}
	})

	t.Run("installed", func(t *testing.T) {
		r := newTLSTestReconciler(t, true)
		got, err := r.certManagerAvailable(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got {
			t.Fatal("expected cert-manager available")
		}
	})

	t.Run("result is cached within TTL", func(t *testing.T) {
		r := newTLSTestReconciler(t, true)
		if _, err := r.certManagerAvailable(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Flip the underlying mapper to "absent" but keep the cache warm; the
		// cached (true) result must be returned until the TTL elapses.
		r.Client = newTLSTestReconciler(t, false).Client
		got, err := r.certManagerAvailable(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got {
			t.Fatal("expected cached availability to persist within TTL")
		}
	})
}

// TestReconcileTLSDegradation covers graceful degradation: TLS requested but cert-manager
// absent must degrade gracefully (condition + requeue, no error, no certificate).
func TestReconcileTLSDegradation(t *testing.T) {
	ha := nativeTLSHA("home")
	r := newTLSTestReconciler(t, false, ha)

	res, err := r.reconcileTLS(context.Background(), ha)
	if err != nil {
		t.Fatalf("reconcileTLS returned error: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected requeue to poll for cert-manager, got %v", res.RequeueAfter)
	}

	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionCertManagerAvailable)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonCertManagerNotInstalled {
		t.Fatalf("expected CertManagerAvailable=False/%s, got %+v", reasonCertManagerNotInstalled, cond)
	}
	tlsCond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if tlsCond == nil || tlsCond.Status != metav1.ConditionUnknown {
		t.Fatalf("expected TLSReady=Unknown, got %+v", tlsCond)
	}
}

func TestReconcileTLSNoopWhenNoTLSRequested(t *testing.T) {
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"}}
	r := newTLSTestReconciler(t, false, ha)

	res, err := r.reconcileTLS(context.Background(), ha)
	if err != nil {
		t.Fatalf("reconcileTLS returned error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no requeue when no TLS requested, got %v", res.RequeueAfter)
	}
	if len(ha.Status.Conditions) != 0 {
		t.Fatalf("expected no TLS conditions when no TLS requested, got %d", len(ha.Status.Conditions))
	}
}

// TestReconcileTLSAvailableSetsCondition verifies the available path records the
// CertManagerAvailable=True condition.
func TestReconcileTLSAvailableSetsCondition(t *testing.T) {
	ha := nativeTLSHA("home")
	r := newTLSTestReconciler(t, true, ha)

	if _, err := r.reconcileTLS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileTLS returned error: %v", err)
	}
	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionCertManagerAvailable)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonCertManagerInstalled {
		t.Fatalf("expected CertManagerAvailable=True/%s, got %+v", reasonCertManagerInstalled, cond)
	}
}

// getCertificate fetches the operator-managed native TLS Certificate.
func getCertificate(
	t *testing.T, r *HomeAssistantReconciler, ha *hav1.HomeAssistant,
) (*unstructured.Unstructured, error) {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(certificateGVK)
	err := r.Get(context.Background(), client.ObjectKey{Name: nativeTLSCertificateName(ha), Namespace: ha.Namespace}, u)
	return u, err
}

// TestReconcileTLSCreatesNativeCertificate: with cert-manager available and
// native TLS on, the operator creates a Certificate (with the Service FQDN SAN)
// and reports TLSReady=False until it is issued.
func TestReconcileTLSCreatesNativeCertificate(t *testing.T) {
	ha := nativeTLSHA("home")
	r := newTLSTestReconciler(t, true, ha)

	res, err := r.reconcileTLS(context.Background(), ha)
	if err != nil {
		t.Fatalf("reconcileTLS error: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected requeue while waiting for issuance, got %v", res.RequeueAfter)
	}

	cert, err := getCertificate(t, r, ha)
	if err != nil {
		t.Fatalf("expected Certificate to be created: %v", err)
	}
	dnsNames, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	wantFQDN := "home.default.svc.cluster.local"
	found := false
	for _, d := range dnsNames {
		if d == wantFQDN {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Service FQDN %q in dnsNames %v", wantFQDN, dnsNames)
	}
	issuer, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name")
	if issuer != "test-issuer" {
		t.Fatalf("expected issuerRef.name=test-issuer, got %q", issuer)
	}

	tlsCond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if tlsCond == nil || tlsCond.Status != metav1.ConditionFalse || tlsCond.Reason != reasonCertificateNotIssued {
		t.Fatalf("expected TLSReady=False/%s, got %+v", reasonCertificateNotIssued, tlsCond)
	}
}

// TestReconcileTLSNativeReady: a Certificate reporting Ready=True flips TLSReady.
func TestReconcileTLSNativeReady(t *testing.T) {
	ha := nativeTLSHA("home")
	cert := &unstructured.Unstructured{Object: map[string]interface{}{}}
	cert.SetGroupVersionKind(certificateGVK)
	cert.SetName(nativeTLSCertificateName(ha))
	cert.SetNamespace(ha.Namespace)
	cert.Object["spec"] = desiredNativeCertificateSpec(ha)
	_ = unstructured.SetNestedSlice(cert.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	// cert-manager always populates the Secret before flipping the Certificate
	// to Ready — reconcileHTTPConfigViaWS (called once certReady is true) reads
	// it directly to fingerprint the certificate content, so a realistic
	// fixture needs it present too.
	secret := nativeTLSSecretFor(ha, "cert-v1")

	r := newTLSTestReconciler(t, true, ha, cert, secret)
	if _, err := r.reconcileTLS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileTLS error: %v", err)
	}
	// reconcileTLS itself only provisions the Certificate/CertManagerAvailable
	// now — activation moved to reconcileHTTPConfigViaWS, called unconditionally
	// from the main Reconcile() loop for every HA.
	if _, err := r.reconcileHTTPConfigViaWS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	// No API token Secret exists in this fixture (bootstrap not done yet), so
	// reconcileHTTPConfigViaWS cannot attempt WS at all and falls back to the
	// YAML-mechanism contract (reasonWSConfigUnsupported) — NOT reasonTLSReady,
	// which nativeTLSManagedByWS would otherwise treat as "WS already owns
	// this" and cause applyNativeTLS to wrongly skip YAML injection during
	// this exact window (fix: found via manual e2e reproduction).
	tlsCond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if tlsCond == nil || tlsCond.Status != metav1.ConditionTrue || tlsCond.Reason != reasonWSConfigUnsupported {
		t.Fatalf("expected TLSReady=True/%s, got %+v", reasonWSConfigUnsupported, tlsCond)
	}
}

// TestReconcileTLSNativeBYO: bring-your-own Secret needs no cert-manager and
// creates no Certificate.
func TestReconcileTLSNativeBYO(t *testing.T) {
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{Alpha: &hav1.AlphaSpec{TLS: &hav1.TLSAlphaSpec{
			Native: &hav1.NativeTLSAlphaSpec{Enabled: true, SecretName: "my-tls"},
		}}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tls", Namespace: "default"},
		Data:       map[string][]byte{"tls.crt": []byte("cert"), "tls.key": []byte("key")},
	}
	r := newTLSTestReconciler(t, false, ha, secret) // cert-manager absent — must not matter

	if _, err := r.reconcileTLS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileTLS error: %v", err)
	}
	if _, err := r.reconcileHTTPConfigViaWS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	// No API token Secret exists in this fixture, so reconcileHTTPConfigViaWS
	// cannot attempt WS at all and falls back to the YAML-mechanism contract
	// (reasonWSConfigUnsupported), matching TestReconcileTLSNativeReady in the
	// same situation — not the old reasonUsingProvidedSecret (which no longer
	// distinguishes source once the WS-first flow is the sole writer of
	// TLSReady).
	tlsCond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if tlsCond == nil || tlsCond.Status != metav1.ConditionTrue || tlsCond.Reason != reasonWSConfigUnsupported {
		t.Fatalf("expected TLSReady=True/%s, got %+v", reasonWSConfigUnsupported, tlsCond)
	}
	if _, err := getCertificate(t, r, ha); err == nil {
		t.Fatal("expected no operator-managed Certificate for bring-your-own secret")
	}
}

// --- Native TLS via WS http/config/* ---
//
// mockHTTPConfigServer starts a WS test server performing the standard
// auth_required/auth_ok handshake once per connection, then replies to
// exactly one command using respond(cmd). haclient opens a fresh connection
// per command ("one-shot" pattern), so respond may be called more than once
// across a single reconcileHTTPConfigViaWS call (e.g. GetHTTPConfig then
// ConfigureHTTPConfig) — switch on cmd["type"] to distinguish them.
func mockHTTPConfigServer(
	t *testing.T, respond func(cmd map[string]interface{}) map[string]interface{},
) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
		var authMsg map[string]interface{}
		_ = conn.ReadJSON(&authMsg)
		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

		var cmd map[string]interface{}
		if err := conn.ReadJSON(&cmd); err != nil {
			return
		}
		resp := respond(cmd)
		if resp == nil {
			// respond callbacks report an unexpected command via t.Errorf (not
			// t.Fatalf/t.Fatal — those must only be called from the goroutine
			// running the test, never from this connection-handler goroutine)
			// and return nil; fall back to a well-formed error response here
			// instead of dereferencing/assigning into a nil map.
			resp = map[string]interface{}{
				"type": "result", "success": false,
				"error": map[string]interface{}{"code": "unexpected_command", "message": "see t.Errorf output"},
			}
		}
		resp["id"] = cmd["id"]
		_ = conn.WriteJSON(resp)
	}))
	t.Cleanup(server.Close)
	return server
}

// tokenSecretFor creates the API token Secret getAPIToken expects, so
// reconcileHTTPConfigViaWS can proceed past the "bootstrap not done" fallback.
func tokenSecretFor(ha *hav1.HomeAssistant) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ha.Name + "-homeassistant-api-token", Namespace: ha.Namespace},
		Data:       map[string][]byte{"token": []byte("test-token")},
	}
}

func wsURL(server *httptest.Server) string {
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// nativeTLSSecretFor builds the native-TLS Secret reconcileHTTPConfigViaWS reads
// to compute its content fingerprint. Different crtContent values simulate a
// rotation (same mount path, different bytes) — exactly the case
// ssl_certificate/ssl_key's static path alone cannot detect.
func nativeTLSSecretFor(ha *hav1.HomeAssistant, crtContent string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: nativeTLSSecretName(ha), Namespace: ha.Namespace},
		Data:       map[string][]byte{"tls.crt": []byte(crtContent), "tls.key": []byte(crtContent + "-key")},
	}
}

// withConfirmedNativeTLSFingerprint marks secret's content as already
// confirmed-active on ha, simulating a prior successful promote.
func withConfirmedNativeTLSFingerprint(ha *hav1.HomeAssistant, secret *corev1.Secret) {
	if ha.Annotations == nil {
		ha.Annotations = map[string]string{}
	}
	ha.Annotations[nativeTLSWSFingerprintAnnotationKey] = nativeTLSSecretFingerprint(secret)
}

// TestDesiredHTTPConfigDataAndNeedsConfigure covers the fingerprint/diff
// helper behind the WS-first flow, including the fix for the bug the e2e
// rotation scenario caught — ssl_certificate/ssl_key are static file *paths*
// that never change across a rotation, so a matching Stable is NOT enough;
// only a confirmed content fingerprint proves the current certificate was
// actually applied.
func TestDesiredHTTPConfigDataAndNeedsConfigure(t *testing.T) {
	ha := nativeTLSHA("home")
	desired, _ := desiredHTTPConfigData(ha, "", true, true)
	if desired.SSLCertificate != nativeTLSCertPath || desired.SSLKey != nativeTLSKeyPath {
		t.Fatalf("unexpected desired cert/key paths: %+v", desired)
	}
	if desired.UseXForwardedFor || len(desired.TrustedProxies) != 0 {
		t.Fatalf("expected no trusted-proxies defaults without ingress/gateway exposure: %+v", desired)
	}

	t.Run("no configure when stable matches AND fingerprint already confirmed", func(t *testing.T) {
		if httpConfigNeedsConfigure(desired, desired, true) {
			t.Fatal("expected no configure needed")
		}
	})

	t.Run("configure needed when stable matches but fingerprint unconfirmed (rotation, same path)", func(t *testing.T) {
		if !httpConfigNeedsConfigure(desired, desired, false) {
			t.Fatal("expected configure needed: matching path does not prove the current content was applied")
		}
	})

	t.Run("configure needed when stable differs, regardless of fingerprint", func(t *testing.T) {
		stable := &haclient.HTTPConfigData{SSLCertificate: "/old/tls.crt"}
		if !httpConfigNeedsConfigure(stable, desired, true) {
			t.Fatal("expected configure needed: stable fields differ from desired")
		}
	})
}

// TestHTTPConfigDataEqualComparesBoolPointersByValue guards against a
// regression IPBanEnabled/UseXFrameOptions being *bool (needed so an
// explicit false is distinguishable from the field never being configured,
// see haclient.HTTPConfigData's doc comment) would otherwise reintroduce:
// stable (freshly unmarshaled from HA's response) and desired (freshly
// parsed from spec.configuration) always hold distinct *bool allocations
// even when they agree on the value. Comparing those pointers with == would
// report "different" on every single reconcile, sending a needless
// http/config/configure — and triggering HA's own internal restart — forever.
func TestHTTPConfigDataEqualComparesBoolPointersByValue(t *testing.T) {
	a := &haclient.HTTPConfigData{IPBanEnabled: ptr.To(false), UseXFrameOptions: ptr.To(true)}
	b := &haclient.HTTPConfigData{IPBanEnabled: ptr.To(false), UseXFrameOptions: ptr.To(true)}
	if !httpConfigDataEqual(a, b) {
		t.Fatal("expected equal HTTPConfigData for distinct *bool pointers holding the same values")
	}
	if httpConfigNeedsConfigure(a, b, true) {
		t.Fatal("expected no configure needed: only pointer identity differs, not the configured value")
	}
}

// TestReconcileNativeTLSViaWS_ConfigureSent covers the "configure" leg: a
// stable config that doesn't match desired triggers ConfigureHTTPConfig and
// TLSReady flips to False/TLSConfigPending, without any StatefulSet hash bump
// (this function never touches ConfigMap/StatefulSet at all).
func TestReconcileNativeTLSViaWS_ConfigureSent(t *testing.T) {
	ha := nativeTLSHA("home")
	var configureCalled bool
	server := mockHTTPConfigServer(t, func(cmd map[string]interface{}) map[string]interface{} {
		switch cmd["type"] {
		case "http/config":
			return map[string]interface{}{
				"type": "result", "success": true,
				"result": map[string]interface{}{
					"stable":             map[string]interface{}{"ssl_certificate": "/old/tls.crt"},
					"pending":            nil,
					"revert_at":          nil,
					"active_config_type": "stable",
				},
			}
		case "http/config/configure":
			configureCalled = true
			config, _ := cmd["config"].(map[string]interface{})
			if config["ssl_certificate"] != nativeTLSCertPath {
				t.Errorf("expected configure with ssl_certificate=%s, got %v", nativeTLSCertPath, config)
			}
			return map[string]interface{}{"type": "result", "success": true, "result": map[string]interface{}{"restart": true}}
		default:
			t.Errorf("unexpected command: %v", cmd["type"])
			return nil
		}
	})

	secret := nativeTLSSecretFor(ha, "cert-v1")
	r := newTLSTestReconciler(t, true, ha, secret, tokenSecretFor(ha))
	r.NewHAClient = func(string) *haclient.Client { return haclient.NewClient(wsURL(server)) }

	res, err := r.reconcileHTTPConfigViaWS(context.Background(), ha)
	if err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	if !configureCalled {
		t.Fatal("expected ConfigureHTTPConfig to be called")
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected a requeue to poll for health-check, got %v", res)
	}
	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonTLSConfigPending {
		t.Fatalf("expected TLSReady=False/%s, got %+v", reasonTLSConfigPending, cond)
	}
}

// TestReconcileNativeTLSViaWS_SteadyState covers the "already applied" leg:
// stable already matches desired, no pending, AND the current Secret content
// fingerprint was already confirmed active — the only combination that should
// produce true steady state with no configure call.
func TestReconcileNativeTLSViaWS_SteadyState(t *testing.T) {
	ha := nativeTLSHA("home")
	desired, _ := desiredHTTPConfigData(ha, "", true, true)
	secret := nativeTLSSecretFor(ha, "cert-v1")
	withConfirmedNativeTLSFingerprint(ha, secret)
	server := mockHTTPConfigServer(t, func(cmd map[string]interface{}) map[string]interface{} {
		if cmd["type"] == "http/config/configure" {
			t.Fatal("expected no configure call once fingerprint is already confirmed")
		}
		return map[string]interface{}{
			"type": "result", "success": true,
			"result": map[string]interface{}{
				"stable": map[string]interface{}{
					"ssl_certificate": desired.SSLCertificate,
					"ssl_key":         desired.SSLKey,
				},
				"pending":            nil,
				"revert_at":          nil,
				"active_config_type": "stable",
			},
		}
	})

	r := newTLSTestReconciler(t, true, ha, secret, tokenSecretFor(ha))
	r.NewHAClient = func(string) *haclient.Client { return haclient.NewClient(wsURL(server)) }

	if _, err := r.reconcileHTTPConfigViaWS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonTLSReady {
		t.Fatalf("expected TLSReady=True/%s, got %+v", reasonTLSReady, cond)
	}
}

// TestReconcileNativeTLSViaWS_ContentRotationSamePathTriggersConfigure is the
// regression test for the bug the e2e rotation scenario caught: HA's
// ssl_certificate/ssl_key are static file *paths* that never change across a
// rotation (cert-manager reissues into the same mount path), so a Stable that
// already matches desired's paths must NOT be treated as "nothing to do" when
// the underlying Secret content has actually changed since the last confirmed
// fingerprint — a fresh configure must still be sent.
func TestReconcileNativeTLSViaWS_ContentRotationSamePathTriggersConfigure(t *testing.T) {
	ha := nativeTLSHA("home")
	desired, _ := desiredHTTPConfigData(ha, "", true, true)
	oldSecret := nativeTLSSecretFor(ha, "cert-v1")
	withConfirmedNativeTLSFingerprint(ha, oldSecret) // confirmed for the OLD content
	newSecret := nativeTLSSecretFor(ha, "cert-v2")   // rotated: same path, different bytes

	var configureCalled bool
	server := mockHTTPConfigServer(t, func(cmd map[string]interface{}) map[string]interface{} {
		switch cmd["type"] {
		case "http/config":
			return map[string]interface{}{
				"type": "result", "success": true,
				"result": map[string]interface{}{
					// Stable already reports the same static paths desired
					// wants — this is exactly the case that used to be
					// (incorrectly) treated as "already applied".
					"stable":             map[string]interface{}{"ssl_certificate": desired.SSLCertificate, "ssl_key": desired.SSLKey},
					"pending":            nil,
					"revert_at":          nil,
					"active_config_type": "stable",
				},
			}
		case "http/config/configure":
			configureCalled = true
			return map[string]interface{}{"type": "result", "success": true, "result": map[string]interface{}{"restart": true}}
		}
		t.Errorf("unexpected command: %v", cmd["type"])
		return nil
	})

	r := newTLSTestReconciler(t, true, ha, newSecret, tokenSecretFor(ha))
	r.NewHAClient = func(string) *haclient.Client { return haclient.NewClient(wsURL(server)) }

	res, err := r.reconcileHTTPConfigViaWS(context.Background(), ha)
	if err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	if !configureCalled {
		t.Fatal("expected ConfigureHTTPConfig to be called despite matching stable paths — content fingerprint changed")
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected a requeue to poll for health-check, got %v", res)
	}
	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonTLSConfigPending {
		t.Fatalf("expected TLSReady=False/%s, got %+v", reasonTLSConfigPending, cond)
	}
}

// TestReconcileNativeTLSViaWS_Reverted covers the case where HA keeps a
// failed pending populated with Error/ErrorMessage rather than resetting it to
// nil. reconcileHTTPConfigViaWS must detect this via Pending.Error != "" (never
// Pending == nil), report TLSConfigReverted, and emit exactly one Event
// without ever calling PromoteHTTPConfig or re-sending configure.
func TestReconcileNativeTLSViaWS_Reverted(t *testing.T) {
	ha := nativeTLSHA("home")
	desired, _ := desiredHTTPConfigData(ha, "", true, true)
	var promoteCalled, configureCalled bool
	server := mockHTTPConfigServer(t, func(cmd map[string]interface{}) map[string]interface{} {
		switch cmd["type"] {
		case "http/config":
			return map[string]interface{}{
				"type": "result", "success": true,
				"result": map[string]interface{}{
					"stable": map[string]interface{}{"ssl_certificate": "/old/tls.crt"},
					"pending": map[string]interface{}{
						"ssl_certificate": desired.SSLCertificate,
						"ssl_key":         desired.SSLKey,
						"error":           "apply_failed",
						"error_message":   "Failed to bind to the configured address",
					},
					"revert_at":          nil,
					"active_config_type": "stable",
				},
			}
		case "http/config/configure":
			configureCalled = true
		case "http/config/promote":
			promoteCalled = true
		}
		return map[string]interface{}{"type": "result", "success": true, "result": nil}
	})

	r := newTLSTestReconciler(t, true, ha, nativeTLSSecretFor(ha, "cert-v1"), tokenSecretFor(ha))
	r.NewHAClient = func(string) *haclient.Client { return haclient.NewClient(wsURL(server)) }

	if _, err := r.reconcileHTTPConfigViaWS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	if configureCalled || promoteCalled {
		t.Fatal("expected no configure/promote call once a reverted pending is observed")
	}
	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonTLSConfigReverted {
		t.Fatalf("expected TLSReady=True/%s, got %+v", reasonTLSConfigReverted, cond)
	}
	if !strings.Contains(cond.Message, "Failed to bind to the configured address") {
		t.Fatalf("expected Pending.ErrorMessage surfaced in condition message, got %q", cond.Message)
	}
	select {
	case ev := <-r.Recorder.(*events.FakeRecorder).Events:
		if !strings.Contains(ev, eventTLSConfigReverted) {
			t.Fatalf("expected a %s event, got %q", eventTLSConfigReverted, ev)
		}
	default:
		t.Fatal("expected an Event to be recorded for the reverted rotation")
	}
}

// TestReconcileNativeTLSViaWS_BringYourOwnSecret covers the case where the WS
// flow behaves identically for a bring-your-own Secret as for a
// cert-manager-issued one — same mechanism, reached via
// nativeTLSUsingProvidedSecret instead of the cert-manager branch.
func TestReconcileNativeTLSViaWS_BringYourOwnSecret(t *testing.T) {
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{Alpha: &hav1.AlphaSpec{TLS: &hav1.TLSAlphaSpec{
			Native: &hav1.NativeTLSAlphaSpec{Enabled: true, SecretName: "my-tls"},
		}}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tls", Namespace: "default"},
		Data:       map[string][]byte{"tls.crt": []byte("cert"), "tls.key": []byte("key")},
	}
	var configureCalled bool
	server := mockHTTPConfigServer(t, func(cmd map[string]interface{}) map[string]interface{} {
		switch cmd["type"] {
		case "http/config":
			return map[string]interface{}{
				"type": "result", "success": true,
				"result": map[string]interface{}{
					"stable":             map[string]interface{}{"ssl_certificate": "/old/tls.crt"},
					"pending":            nil,
					"revert_at":          nil,
					"active_config_type": "stable",
				},
			}
		case "http/config/configure":
			configureCalled = true
			return map[string]interface{}{"type": "result", "success": true, "result": map[string]interface{}{"restart": true}}
		}
		return map[string]interface{}{"type": "result", "success": true, "result": nil}
	})

	r := newTLSTestReconciler(t, false, ha, secret, tokenSecretFor(ha)) // cert-manager absent — must not matter
	r.NewHAClient = func(string) *haclient.Client { return haclient.NewClient(wsURL(server)) }

	if _, err := r.reconcileTLS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileTLS error: %v", err)
	}
	if _, err := r.reconcileHTTPConfigViaWS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	if !configureCalled {
		t.Fatal("expected ConfigureHTTPConfig to be called for the bring-your-own Secret path too")
	}
	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonTLSConfigPending {
		t.Fatalf("expected TLSReady=False/%s, got %+v", reasonTLSConfigPending, cond)
	}
}

// TestReconcileNativeTLSViaWS_Unsupported covers the WS-unavailable leg:
// GetHTTPConfig failing with unknown_command falls back to the "material
// ready" contract with reasonWSConfigUnsupported, without retrying in a loop.
func TestReconcileNativeTLSViaWS_Unsupported(t *testing.T) {
	ha := nativeTLSHA("home")
	server := mockHTTPConfigServer(t, func(cmd map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"type": "result", "success": false,
			"error": map[string]interface{}{"code": "unknown_command", "message": "Unknown command"},
		}
	})

	r := newTLSTestReconciler(t, true, ha, nativeTLSSecretFor(ha, "cert-v1"), tokenSecretFor(ha))
	r.NewHAClient = func(string) *haclient.Client { return haclient.NewClient(wsURL(server)) }

	res, err := r.reconcileHTTPConfigViaWS(context.Background(), ha)
	if err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no special requeue when falling back to YAML, got %v", res)
	}
	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonWSConfigUnsupported {
		t.Fatalf("expected TLSReady=True/%s, got %+v", reasonWSConfigUnsupported, cond)
	}
}

// TestReconcileNativeTLSViaWS_NotRunning covers the case where HA rejects
// configure with not_running (bootstrap in progress), which falls back the
// same way as unknown_command.
func TestReconcileNativeTLSViaWS_NotRunning(t *testing.T) {
	ha := nativeTLSHA("home")
	server := mockHTTPConfigServer(t, func(cmd map[string]interface{}) map[string]interface{} {
		switch cmd["type"] {
		case "http/config":
			return map[string]interface{}{
				"type": "result", "success": true,
				"result": map[string]interface{}{
					"stable": map[string]interface{}{"ssl_certificate": "/old/tls.crt"}, "pending": nil,
					"revert_at": nil, "active_config_type": "stable",
				},
			}
		case "http/config/configure":
			return map[string]interface{}{
				"type": "result", "success": false,
				"error": map[string]interface{}{"code": "not_running", "message": "Home Assistant is starting up"},
			}
		}
		t.Errorf("unexpected command: %v", cmd["type"])
		return nil
	})

	r := newTLSTestReconciler(t, true, ha, nativeTLSSecretFor(ha, "cert-v1"), tokenSecretFor(ha))
	r.NewHAClient = func(string) *haclient.Client { return haclient.NewClient(wsURL(server)) }

	res, err := r.reconcileHTTPConfigViaWS(context.Background(), ha)
	if err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	if res.RequeueAfter != nativeTLSPollInterval {
		t.Fatalf("expected a retry requeue on a transient configure failure, got %v", res)
	}
}

// TestReconcileNativeTLSViaWS_NoCachingAcrossReconciles covers the case where
// an HA that rejected http/config as unknown_command on one reconcile must be
// tried again fresh on the next — the operator never remembers "WS
// unsupported" in memory. Two independent reconciler instances
// (simulating two separate reconcile passes, each re-deriving everything from
// the current ha object with no shared state) are used deliberately to prove
// nothing but ha.Status carries information forward.
func TestReconcileNativeTLSViaWS_NoCachingAcrossReconciles(t *testing.T) {
	ha := nativeTLSHA("home")

	unsupportedServer := mockHTTPConfigServer(t, func(cmd map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"type": "result", "success": false,
			"error": map[string]interface{}{"code": "unknown_command", "message": "Unknown command"},
		}
	})
	secret := nativeTLSSecretFor(ha, "cert-v1")
	r1 := newTLSTestReconciler(t, true, ha, secret, tokenSecretFor(ha))
	r1.NewHAClient = func(string) *haclient.Client { return haclient.NewClient(wsURL(unsupportedServer)) }
	if _, err := r1.reconcileHTTPConfigViaWS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if cond == nil || cond.Reason != reasonWSConfigUnsupported {
		t.Fatalf("expected first pass to report %s, got %+v", reasonWSConfigUnsupported, cond)
	}

	// "HA was upgraded" — a fresh reconciler (no shared state with r1) against
	// a server that now supports http/config and already reports matching
	// paths. Since this instance's fingerprint was never confirmed via WS
	// (the previous pass never got past "unsupported"), a fresh configure is
	// still expected — matching paths alone must not be mistaken for "already
	// applied" (see TestReconcileNativeTLSViaWS_ContentRotationSamePathTriggersConfigure).
	desired, _ := desiredHTTPConfigData(ha, "", true, true)
	var configureCalled bool
	supportedServer := mockHTTPConfigServer(t, func(cmd map[string]interface{}) map[string]interface{} {
		switch cmd["type"] {
		case "http/config":
			return map[string]interface{}{
				"type": "result", "success": true,
				"result": map[string]interface{}{
					"stable": map[string]interface{}{
						"ssl_certificate": desired.SSLCertificate, "ssl_key": desired.SSLKey,
					},
					"pending": nil, "revert_at": nil, "active_config_type": "stable",
				},
			}
		case "http/config/configure":
			configureCalled = true
			return map[string]interface{}{"type": "result", "success": true, "result": map[string]interface{}{"restart": true}}
		}
		t.Errorf("unexpected command: %v", cmd["type"])
		return nil
	})
	r2 := newTLSTestReconciler(t, true, ha, secret, tokenSecretFor(ha))
	r2.NewHAClient = func(string) *haclient.Client { return haclient.NewClient(wsURL(supportedServer)) }
	if _, err := r2.reconcileHTTPConfigViaWS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	if !configureCalled {
		t.Fatal("expected the second pass to send a fresh configure (fingerprint never confirmed for this instance)")
	}
	cond = meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonTLSConfigPending {
		t.Fatalf("expected second pass to pick up newly-available WS support and start configuring (%s), got %+v",
			reasonTLSConfigPending, cond)
	}
}

// TestReconcileTLSNativeBYOMissingSecret: a bring-your-own Secret that doesn't
// exist (or lacks tls.crt/tls.key) must not report TLSReady=True.
func TestReconcileTLSNativeBYOMissingSecret(t *testing.T) {
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{Alpha: &hav1.AlphaSpec{TLS: &hav1.TLSAlphaSpec{
			Native: &hav1.NativeTLSAlphaSpec{Enabled: true, SecretName: "my-tls"},
		}}},
	}
	r := newTLSTestReconciler(t, false, ha) // Secret "my-tls" does not exist

	if _, err := r.reconcileTLS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileTLS error: %v", err)
	}
	tlsCond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if tlsCond == nil || tlsCond.Status != metav1.ConditionFalse || tlsCond.Reason != reasonProvidedSecretInvalid {
		t.Fatalf("expected TLSReady=False/%s, got %+v", reasonProvidedSecretInvalid, tlsCond)
	}
}

// TestApplyNativeTLSSkipsYAMLWhenWSManaged covers the case where, once
// nativeTLSManagedByWS(ha) is true (TLSReady set by reconcileHTTPConfigViaWS),
// HomeAssistantConfigurationReconciler.applyNativeTLS must not also inject
// ssl_certificate/ssl_key into configuration.yaml — WS owns http: exclusively
// from that point on. No live HA/WS call is needed for this decision: it is
// read straight from status, kept in sync with the identical gate used for
// the StatefulSet restart annotation (nativeTLSManagedByWS).
func TestApplyNativeTLSSkipsYAMLWhenWSManaged(t *testing.T) {
	ha := nativeTLSHA("home")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: nativeTLSCertificateName(ha), Namespace: ha.Namespace},
		Data:       map[string][]byte{"tls.crt": []byte("cert"), "tls.key": []byte("key")},
	}
	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type: conditionTLSReady, Status: metav1.ConditionTrue,
		Reason: reasonTLSReady, Message: "confirmed active via WS",
	})

	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatalf("add hav1: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ha, secret).Build()
	r := &HomeAssistantConfigurationReconciler{Client: cl, Scheme: scheme}

	out, err := r.applyNativeTLS(context.Background(), ha, "default_config:\n")
	if err != nil {
		t.Fatalf("applyNativeTLS error: %v", err)
	}
	if out != "default_config:\n" {
		t.Fatalf("expected content unchanged (WS owns http:), got:\n%s", out)
	}
}

// TestApplyNativeTLSInjectsYAMLWhenWSUnsupported: the pre-existing YAML
// injection path stays intact when WS is not (yet) confirmed active — no
// TLSReady condition at all (fresh HA) behaves exactly as before this feature.
func TestApplyNativeTLSInjectsYAMLWhenWSUnsupported(t *testing.T) {
	ha := nativeTLSHA("home")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: nativeTLSCertificateName(ha), Namespace: ha.Namespace},
		Data:       map[string][]byte{"tls.crt": []byte("cert"), "tls.key": []byte("key")},
	}

	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatalf("add hav1: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ha, secret).Build()
	r := &HomeAssistantConfigurationReconciler{Client: cl, Scheme: scheme}

	out, err := r.applyNativeTLS(context.Background(), ha, "default_config:\n")
	if err != nil {
		t.Fatalf("applyNativeTLS error: %v", err)
	}
	if !strings.Contains(out, "ssl_certificate: "+nativeTLSCertPath) {
		t.Fatalf("expected YAML injection when WS is not confirmed active, got:\n%s", out)
	}
}

// TestReconcileTLSCleanupOnDisable: disabling native TLS deletes the managed cert.
func TestReconcileTLSCleanupOnDisable(t *testing.T) {
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"}}
	cert := &unstructured.Unstructured{Object: map[string]interface{}{}}
	cert.SetGroupVersionKind(certificateGVK)
	cert.SetName(nativeTLSCertificateName(ha))
	cert.SetNamespace(ha.Namespace)

	r := newTLSTestReconciler(t, true, ha, cert)
	if _, err := r.reconcileTLS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileTLS error: %v", err)
	}
	if _, err := getCertificate(t, r, ha); err == nil {
		t.Fatal("expected orphaned Certificate to be deleted when native TLS is off")
	}
}

func withTLSReady(ha *hav1.HomeAssistant) *hav1.HomeAssistant {
	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type: conditionTLSReady, Status: metav1.ConditionTrue, Reason: reasonTLSReady,
	})
	return ha
}

// TestNativeTLSActiveAndScheme: scheme flips to https only once the
// certificate is ready (TLSReady=True), so operator and HA switch together.
func TestNativeTLSActiveAndScheme(t *testing.T) {
	// Enabled but not yet ready → still http.
	pending := nativeTLSHA("home")
	if nativeTLSActive(pending) {
		t.Fatal("native TLS must not be active before TLSReady")
	}
	if haScheme(pending) != "http" {
		t.Fatalf("expected http before ready, got %s", haScheme(pending))
	}

	ready := withTLSReady(nativeTLSHA("home"))
	if !nativeTLSActive(ready) {
		t.Fatal("native TLS should be active when enabled and TLSReady=True")
	}
	if haScheme(ready) != "https" {
		t.Fatalf("expected https when active, got %s", haScheme(ready))
	}
	if got := buildHomeAssistantURL(ready); got != "https://home.default.svc.cluster.local:8123" {
		t.Fatalf("unexpected URL: %s", got)
	}
}

func TestLoadNativeTLSCA(t *testing.T) {
	ha := withTLSReady(nativeTLSHA("home"))
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: nativeTLSSecretName(ha), Namespace: ha.Namespace},
		Data:       map[string][]byte{"ca.crt": []byte("CA-PEM")},
	}
	r := newTLSTestReconciler(t, true, ha, secret)
	if ca := loadNativeTLSCA(context.Background(), r.Client, ha); string(ca) != "CA-PEM" {
		t.Fatalf("expected CA-PEM, got %q", string(ca))
	}

	// No secret → nil (fail closed to system roots, never InsecureSkipVerify).
	r2 := newTLSTestReconciler(t, true, nativeTLSHA("other"))
	if ca := loadNativeTLSCA(context.Background(), r2.Client, nativeTLSHA("other")); ca != nil {
		t.Fatalf("expected nil CA when secret absent, got %q", string(ca))
	}
}

// TestInjectNativeTLS: http.ssl_certificate/ssl_key are injected into the
// configuration, preserving other http keys, and !include http sections untouched.
func TestInjectNativeTLS(t *testing.T) {
	t.Run("adds http section when missing", func(t *testing.T) {
		out, err := injectNativeTLS("default_config:\n")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "ssl_certificate: /config/ssl/tls.crt") ||
			!strings.Contains(out, "ssl_key: /config/ssl/tls.key") {
			t.Fatalf("missing ssl keys:\n%s", out)
		}
	})

	t.Run("preserves existing http keys", func(t *testing.T) {
		out, err := injectNativeTLS("http:\n  use_x_forwarded_for: true\n")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "use_x_forwarded_for: true") ||
			!strings.Contains(out, "ssl_certificate: /config/ssl/tls.crt") {
			t.Fatalf("unexpected output:\n%s", out)
		}
	})

	t.Run("converts an empty/null http scalar to a mapping", func(t *testing.T) {
		out, err := injectNativeTLS("default_config:\nhttp:\n")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "ssl_certificate: /config/ssl/tls.crt") ||
			!strings.Contains(out, "ssl_key: /config/ssl/tls.key") {
			t.Fatalf("expected ssl keys under a null http:\n%s", out)
		}
	})

	t.Run("preserves tagged-scalar http include", func(t *testing.T) {
		in := "http: !include http.yaml\n"
		out, err := injectNativeTLS(in)
		if err != nil {
			t.Fatal(err)
		}
		if out != in {
			t.Fatalf("expected include preserved, got:\n%s", out)
		}
	})
}

// generatedConfigConfigMap builds the ConfigMap generatedHTTPConfigYAML reads
// (owned in production by HomeAssistantConfigurationReconciler, faked here so
// reconcileHTTPConfigViaWS can pick up the http: fields under test).
func generatedConfigConfigMap(ha *hav1.HomeAssistant, configYAML string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ha.Name + generatedConfigmapSuffix, Namespace: ha.Namespace},
		Data:       map[string]string{configurationYamlKey: configYAML},
	}
}

// TestReconcileHTTPConfigViaWS_NonTLSInstanceGetsNonTLSFields covers the case
// where an HA that never requested native TLS still gets its non-TLS http:
// fields (ip_ban_enabled, cors_allowed_origins, ...) applied via
// http/config/configure once WS is available — the mechanism is not gated on
// spec.alpha.tls.native.enabled at all.
//
// TLSReady is intentionally left untouched here (stays absent), rather than
// reaching reasonTLSReady — reconcileHTTPConfigViaWS's maybeSetCondition only
// writes TLSReady when native TLS was actually requested (see its definition
// in native_tls_ws.go), to avoid a misleading "TLSReady" status on an
// instance that never asked for TLS. ConfigureHTTPConfig being called with
// exactly the right fields is the actual signal this test verifies.
func TestReconcileHTTPConfigViaWS_NonTLSInstanceGetsNonTLSFields(t *testing.T) {
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"},
	}
	configYAML := "http:\n  ip_ban_enabled: true\n  cors_allowed_origins:\n    - https://example.com\n"
	cm := generatedConfigConfigMap(ha, configYAML)

	var configureCalled bool
	server := mockHTTPConfigServer(t, func(cmd map[string]interface{}) map[string]interface{} {
		switch cmd["type"] {
		case "http/config":
			return map[string]interface{}{
				"type": "result", "success": true,
				"result": map[string]interface{}{
					"stable":             map[string]interface{}{},
					"pending":            nil,
					"revert_at":          nil,
					"active_config_type": "stable",
				},
			}
		case "http/config/configure":
			configureCalled = true
			config, _ := cmd["config"].(map[string]interface{})
			if ipBan, _ := config["ip_ban_enabled"].(bool); !ipBan {
				t.Errorf("expected ip_ban_enabled=true in configure payload, got %v", config["ip_ban_enabled"])
			}
			origins, _ := config["cors_allowed_origins"].([]interface{})
			if len(origins) != 1 || origins[0] != "https://example.com" {
				t.Errorf("expected cors_allowed_origins=[https://example.com], got %v", config["cors_allowed_origins"])
			}
			if _, present := config["ssl_certificate"]; present {
				t.Errorf("expected no ssl_certificate in payload for a non-TLS instance, got %v", config["ssl_certificate"])
			}
			return map[string]interface{}{"type": "result", "success": true, "result": map[string]interface{}{"restart": true}}
		default:
			t.Errorf("unexpected command: %v", cmd["type"])
			return nil
		}
	})

	r := newTLSTestReconciler(t, false, ha, cm, tokenSecretFor(ha))
	r.NewHAClient = func(string) *haclient.Client { return haclient.NewClient(wsURL(server)) }

	if _, err := r.reconcileHTTPConfigViaWS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	if !configureCalled {
		t.Fatal("expected ConfigureHTTPConfig to be called for a non-TLS instance with custom http: fields")
	}
	if cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady); cond != nil {
		t.Fatalf("expected no TLSReady condition on a non-TLS instance, got %+v", cond)
	}
}

// TestReconcileHTTPConfigViaWS_MergesTLSAndNonTLSFields covers the case where,
// with native TLS enabled AND custom non-TLS http: fields present, a single
// http/config/configure call must carry both — never two competing calls.
func TestReconcileHTTPConfigViaWS_MergesTLSAndNonTLSFields(t *testing.T) {
	ha := nativeTLSHA("home")
	configYAML := "http:\n  ip_ban_enabled: true\n  login_attempts_threshold: 7\n"
	cm := generatedConfigConfigMap(ha, configYAML)
	secret := nativeTLSSecretFor(ha, "cert-v1")

	var configureCalled bool
	server := mockHTTPConfigServer(t, func(cmd map[string]interface{}) map[string]interface{} {
		switch cmd["type"] {
		case "http/config":
			return map[string]interface{}{
				"type": "result", "success": true,
				"result": map[string]interface{}{
					"stable":             map[string]interface{}{},
					"pending":            nil,
					"revert_at":          nil,
					"active_config_type": "stable",
				},
			}
		case "http/config/configure":
			configureCalled = true
			config, _ := cmd["config"].(map[string]interface{})
			if config["ssl_certificate"] != nativeTLSCertPath {
				t.Errorf("expected merged configure to carry ssl_certificate=%s, got %v",
					nativeTLSCertPath, config["ssl_certificate"])
			}
			if config["ssl_key"] != nativeTLSKeyPath {
				t.Errorf("expected merged configure to carry ssl_key=%s, got %v", nativeTLSKeyPath, config["ssl_key"])
			}
			if ipBan, _ := config["ip_ban_enabled"].(bool); !ipBan {
				t.Errorf("expected merged configure to carry ip_ban_enabled=true, got %v", config["ip_ban_enabled"])
			}
			threshold, _ := config["login_attempts_threshold"].(float64)
			if threshold != 7 {
				t.Errorf("expected merged configure to carry login_attempts_threshold=7, got %v",
					config["login_attempts_threshold"])
			}
			return map[string]interface{}{"type": "result", "success": true, "result": map[string]interface{}{"restart": true}}
		default:
			t.Errorf("unexpected command: %v", cmd["type"])
			return nil
		}
	})

	r := newTLSTestReconciler(t, true, ha, secret, cm, tokenSecretFor(ha))
	r.NewHAClient = func(string) *haclient.Client { return haclient.NewClient(wsURL(server)) }

	if _, err := r.reconcileHTTPConfigViaWS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileHTTPConfigViaWS error: %v", err)
	}
	if !configureCalled {
		t.Fatal("expected a single merged ConfigureHTTPConfig call")
	}
	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTLSReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonTLSConfigPending {
		t.Fatalf("expected TLSReady=False/%s, got %+v", reasonTLSConfigPending, cond)
	}
}

// TestBuildStatefulSetPreservesNativeTLSHashAnnotationWhenWSManaged covers the
// handover moment: a StatefulSet already deployed under the old K8s-managed
// restart mechanism (nativeTLSHashAnnotationKey set to a real hash) must keep
// that exact annotation value once TLSReady flips to a WS-managed reason,
// rather than having it deleted. Deleting it here would itself look like a
// pod-template change to needsUpdate and trigger a StatefulSet rollout racing
// with HA's own internal restart from http/config/configure.
func TestBuildStatefulSetPreservesNativeTLSHashAnnotationWhenWSManaged(t *testing.T) {
	ha := nativeTLSHA("home")
	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type: conditionTLSReady, Status: metav1.ConditionFalse, Reason: reasonTLSConfigPending, Message: "pending",
	})
	secret := nativeTLSSecretFor(ha, "cert-v1")

	const oldHash = "old-hash-from-before-ws-handover"
	currentSts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: ha.Name, Namespace: ha.Namespace},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": ha.Name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"app": ha.Name},
					Annotations: map[string]string{nativeTLSHashAnnotationKey: oldHash},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "home-assistant", Image: "old"}}},
			},
		},
	}

	r := newTLSTestReconciler(t, true, ha, secret, currentSts)
	desired, err := r.buildStatefulSet(context.Background(), ha)
	if err != nil {
		t.Fatalf("buildStatefulSet error: %v", err)
	}
	if got := desired.Spec.Template.Annotations[nativeTLSHashAnnotationKey]; got != oldHash {
		t.Fatalf("expected native-tls-hash annotation to stay %q while WS manages rotation, got %q", oldHash, got)
	}
}

// TestBuildStatefulSetLivenessProbeUsesTCPSocket covers a live-cluster
// finding: an HTTPGet liveness probe pointed at the same port/scheme HA
// serves lets Kubernetes kill+restart the container within ~30-60s of a
// native TLS rotation temporarily breaking the served protocol — far
// short of HA's own ~5 minute internal auto-revert timer, so the container
// never survives long enough for HA to self-heal. LivenessProbe must use
// TCPSocket (protocol-agnostic: only "is anything listening") regardless of
// whether native TLS is active; ReadinessProbe stays HTTPGet/protocol-aware
// so the pod is still pulled out of Service traffic immediately when it's
// actually serving the wrong protocol.
func TestBuildStatefulSetLivenessProbeUsesTCPSocket(t *testing.T) {
	for _, ha := range []*hav1.HomeAssistant{
		{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "default"}},
		nativeTLSHA("tls-active"),
	} {
		t.Run(ha.Name, func(t *testing.T) {
			var objs []client.Object
			if nativeTLS(ha) != nil {
				objs = append(objs, nativeTLSSecretFor(ha, "cert-v1"))
				meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
					Type: conditionTLSReady, Status: metav1.ConditionTrue, Reason: reasonTLSReady, Message: "ready",
				})
			}
			r := newTLSTestReconciler(t, true, append(objs, ha)...)
			sts, err := r.buildStatefulSet(context.Background(), ha)
			if err != nil {
				t.Fatalf("buildStatefulSet error: %v", err)
			}
			container := sts.Spec.Template.Spec.Containers[0]

			lp := container.LivenessProbe
			if lp == nil || lp.TCPSocket == nil {
				t.Fatalf("expected LivenessProbe.TCPSocket to be set, got %+v", lp)
			}
			if lp.HTTPGet != nil {
				t.Fatalf("expected LivenessProbe to NOT use HTTPGet (protocol-sensitive), got %+v", lp.HTTPGet)
			}
			if lp.TCPSocket.Port.IntValue() != defaultPort {
				t.Fatalf("expected LivenessProbe.TCPSocket.Port=%d, got %v", defaultPort, lp.TCPSocket.Port)
			}

			rp := container.ReadinessProbe
			if rp == nil || rp.HTTPGet == nil {
				t.Fatalf("expected ReadinessProbe.HTTPGet to remain set, got %+v", rp)
			}
		})
	}
}

// TestProbesEqualDetectsTCPSocketDifference guards the needsUpdate comparison
// path for the TCPSocket handler introduced above: without comparing this
// field, two probes with different ports would be reported as equal (silently
// skipping a real rollout the same way the pre-fix HTTPGet-only comparison
// would have for LivenessProbe).
func TestProbesEqualDetectsTCPSocketDifference(t *testing.T) {
	a := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(8123)}}}
	b := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(8124)}}}
	if probesEqual(a, b) {
		t.Fatal("expected probesEqual to detect differing TCPSocket ports")
	}
	c := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(8123)}}}
	if !probesEqual(a, c) {
		t.Fatal("expected probesEqual to treat identical TCPSocket probes as equal")
	}
}
