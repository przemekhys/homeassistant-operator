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

// newHAClientForHA builds a Home Assistant API client for ha. The operator always
// speaks plain HTTP to Home Assistant inside the cluster; TLS is terminated at
// the edge (Ingress / Gateway API), never in the HA pod. override (when non-nil,
// e.g. in tests) supplies the underlying client for a given base URL.
func newHAClientForHA(ha *hav1.HomeAssistant, override func(string) *haclient.Client) *haclient.Client {
	url := buildHomeAssistantURL(ha)
	if override != nil {
		return override(url)
	}
	return haclient.NewClient(url)
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

// ensureCertificate creates or updates a cert-manager Certificate (as
// unstructured) with the given name, SANs and issuer. It is idempotent
// (get-or-create + update-on-drift) and sets an owner reference so the
// Certificate is garbage-collected with the HA.
func (r *HomeAssistantReconciler) ensureCertificate(
	ctx context.Context, ha *hav1.HomeAssistant, name string, dnsNames []string, ref *hav1.IssuerReference,
) error {
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
			return err
		}
		if err := r.Create(ctx, cert); err != nil {
			return err
		}
		r.Recorder.Eventf(ha, nil, corev1.EventTypeNormal, eventCertificateRequested, eventCertificateRequested,
			"Requested certificate %q via cert-manager", name)
		return nil
	case err != nil:
		return err
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
			return err
		}
	}
	return nil
}

// deleteCertificate removes an operator-managed Certificate by name if present.
// Best-effort: NotFound is ignored, and a missing cert-manager CRD (NoMatchError)
// means there is nothing to delete. Callers that first fetched and validated the
// Certificate should pass client.Preconditions{UID: ...} so a delete+recreate
// between their Get and this Delete cannot remove the replacement object; the
// resulting Conflict is treated like NotFound (the object we validated is gone).
func (r *HomeAssistantReconciler) deleteCertificate(
	ctx context.Context, ha *hav1.HomeAssistant, name string, opts ...client.DeleteOption,
) error {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	cert.SetName(name)
	cert.SetNamespace(ha.Namespace)
	if err := r.Delete(ctx, cert, opts...); err != nil &&
		!apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) && !apierrors.IsConflict(err) {
		return err
	}
	return nil
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

// certManagerRequired reports whether the HomeAssistant spec requests an edge TLS
// mode (Ingress or Gateway API) that needs cert-manager to issue a certificate.
// Bring-your-own Secret modes (SecretName set) do not need cert-manager.
func certManagerRequired(ha *hav1.HomeAssistant) bool {
	if g := ha.Spec.Gateway; g != nil && g.Enabled && g.SecretName == "" && g.IssuerRef != nil {
		return true
	}
	if i := ha.Spec.Ingress; i != nil && i.TLS != nil && i.TLS.Enabled &&
		i.TLS.SecretName == "" && i.TLS.IssuerRef != nil {
		return true
	}
	return false
}

// reconcileTLS is the cert-manager gate for edge TLS (Ingress / Gateway API).
// When such a mode is requested but cert-manager is not installed, it records
// CertManagerAvailable=False, emits an event and requeues — it never returns an
// error, so the rest of the reconcile and other resources keep working
// (constitution principle I). The Certificate objects themselves are provisioned
// by reconcileExposure, layered on the gate established here.
func (r *HomeAssistantReconciler) reconcileTLS(ctx context.Context, ha *hav1.HomeAssistant) (ctrl.Result, error) {
	if !certManagerRequired(ha) {
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
			changed = meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
				Type:               conditionCertManagerAvailable,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: h.Generation,
				Reason:             reasonCertManagerNotInstalled,
				Message:            "cert-manager is not installed; edge TLS is inactive and Home Assistant continues over HTTP",
			})
			return changed
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

	if err := r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
		return meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
			Type:               conditionCertManagerAvailable,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: h.Generation,
			Reason:             reasonCertManagerInstalled,
			Message:            "cert-manager is installed",
		})
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
