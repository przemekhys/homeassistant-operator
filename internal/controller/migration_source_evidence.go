package controller

import (
	"context"
	"strings"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// sourceStopVerified checks direct Kubernetes evidence when supplied, in
// addition to the administrator's explicit confirmation. For external sources
// only that confirmation is available; it is never inferred from networking.
func sourceStopVerified(
	ctx context.Context,
	reader client.Reader,
	m *hav1alpha1.HomeAssistantMigration,
) (bool, error) {
	var approval *hav1alpha1.MigrationApproval
	for i := range m.Spec.Approvals {
		if m.Spec.Approvals[i].ID == "source-stop" {
			if approval != nil {
				return false, nil
			}
			approval = &m.Spec.Approvals[i]
		}
	}
	if approval == nil || !approval.Approved || strings.TrimSpace(approval.Digest) == "" {
		return false, nil
	}
	ref := m.Spec.SourceWorkload
	if ref == nil {
		return true, nil
	}
	if ref.Name == "" || ref.UID == "" || approval.SourceUID != ref.UID {
		return false, nil
	}
	key := client.ObjectKey{Namespace: m.Namespace, Name: ref.Name}
	var object client.Object
	var selector *metav1.LabelSelector
	var replicas *int32
	var observed int64
	var running int32
	switch ref.Kind {
	case "Deployment":
		v := &appsv1.Deployment{}
		if err := reader.Get(ctx, key, v); err != nil {
			return false, err
		}
		object, selector, replicas, observed, running = v, v.Spec.Selector, v.Spec.Replicas, v.Status.ObservedGeneration, v.Status.Replicas
	case "StatefulSet":
		v := &appsv1.StatefulSet{}
		if err := reader.Get(ctx, key, v); err != nil {
			return false, err
		}
		object, selector, replicas, observed, running = v, v.Spec.Selector, v.Spec.Replicas, v.Status.ObservedGeneration, v.Status.Replicas
	default:
		return false, nil
	}
	if string(object.GetUID()) != ref.UID || !object.GetDeletionTimestamp().IsZero() ||
		replicas == nil ||
		*replicas != 0 ||
		observed < object.GetGeneration() ||
		running != 0 ||
		selector == nil ||
		(len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return false, nil
	}
	labels, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return false, err
	}
	var pods corev1.PodList
	if err := reader.List(ctx, &pods, client.InNamespace(m.Namespace), client.MatchingLabelsSelector{Selector: labels}); err != nil {
		return false, err
	}
	for _, pod := range pods.Items {
		// Terminating Pods still count: their processes may be alive.
		if !pod.DeletionTimestamp.IsZero() ||
			(pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed) {
			return false, nil
		}
	}
	return true, nil
}
