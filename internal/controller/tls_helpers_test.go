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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
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
	return newTLSTestReconcilerWithFuncs(t, certManagerPresent, interceptor.Funcs{}, objs...)
}

func newTLSTestReconcilerWithFuncs(
	t *testing.T, certManagerPresent bool, funcs interceptor.Funcs, objs ...client.Object,
) *HomeAssistantReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatalf("add hav1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
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
		WithInterceptorFuncs(funcs).
		Build()
	return &HomeAssistantReconciler{
		Client:   cl,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(16),
	}
}

// ingressTLSHA returns a HomeAssistant that requests edge (Ingress) TLS via an
// issuerRef — i.e. a mode that needs cert-manager.
func ingressTLSHA(name string) *hav1.HomeAssistant {
	return &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-uid")},
		Spec: hav1.HomeAssistantSpec{
			Ingress: &hav1.IngressSpec{
				Enabled: true,
				TLS: &hav1.IngressTLSSpec{
					Enabled:   true,
					IssuerRef: &hav1.IssuerReference{Name: "test-issuer", Kind: "ClusterIssuer"},
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
			name: "ingress TLS with issuerRef needs cert-manager",
			ha:   ingressTLSHA("ha"),
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

// TestReconcileTLSDegradation covers graceful degradation: an edge TLS mode is
// requested but cert-manager is absent — degrade gracefully (condition + requeue,
// no error).
func TestReconcileTLSDegradation(t *testing.T) {
	ha := ingressTLSHA("home")
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
	ha := ingressTLSHA("home")
	r := newTLSTestReconciler(t, true, ha)

	if _, err := r.reconcileTLS(context.Background(), ha); err != nil {
		t.Fatalf("reconcileTLS returned error: %v", err)
	}
	cond := meta.FindStatusCondition(ha.Status.Conditions, conditionCertManagerAvailable)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonCertManagerInstalled {
		t.Fatalf("expected CertManagerAvailable=True/%s, got %+v", reasonCertManagerInstalled, cond)
	}
}
