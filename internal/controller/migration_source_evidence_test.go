package controller

import (
	"context"
	"testing"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSourceStopEvidence(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	zero, one := int32(0), int32(1)
	workload := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "source",
			Namespace:  "home",
			UID:        "uid",
			Generation: 2,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &zero,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "source"}},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 2},
	}
	m := &hav1alpha1.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{Namespace: "home"},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			SourceWorkload: &hav1alpha1.MigrationSourceWorkload{
				Kind: "Deployment",
				Name: "source",
				UID:  "uid",
			},
			Approvals: []hav1alpha1.MigrationApproval{
				{ID: "source-stop", Digest: "confirmed", SourceUID: "uid", Approved: true},
			},
		},
	}
	for _, tc := range []struct {
		name   string
		mutate func(*appsv1.Deployment, *hav1alpha1.HomeAssistantMigration)
		pod    *corev1.Pod
		want   bool
	}{
		{"stopped", nil, nil, true},
		{"running replicas", func(w *appsv1.Deployment, _ *hav1alpha1.HomeAssistantMigration) { w.Spec.Replicas = &one }, nil, false},
		{"stale status", func(w *appsv1.Deployment, _ *hav1alpha1.HomeAssistantMigration) { w.Status.ObservedGeneration = 1 }, nil, false},
		{"recreated source", func(w *appsv1.Deployment, _ *hav1alpha1.HomeAssistantMigration) { w.UID = "other" }, nil, false},
		{"wrong approval", func(_ *appsv1.Deployment, m *hav1alpha1.HomeAssistantMigration) {
			m.Spec.Approvals[0].SourceUID = "other"
		}, nil, false},
		{"selected pod running", nil, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "home", Labels: map[string]string{"app": "source"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, migration := workload.DeepCopy(), m.DeepCopy()
			if tc.mutate != nil {
				tc.mutate(w, migration)
			}
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(w)
			if tc.pod != nil {
				builder = builder.WithObjects(tc.pod)
			}
			got, err := sourceStopVerified(context.Background(), builder.Build(), migration)
			if err != nil || got != tc.want {
				t.Fatalf("sourceStopVerified = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
