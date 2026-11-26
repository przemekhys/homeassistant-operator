package controller

import (
	"context"
	"testing"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReconcileServiceReconcilesOnboardingPortByName(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home-assistant", Namespace: "default"},
		Spec:       hav1.HomeAssistantSpec{Service: &hav1.ServiceSpec{Type: corev1.ServiceTypeNodePort}},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: ha.Name, Namespace: ha.Namespace},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort, Ports: []corev1.ServicePort{{
			Name: "http", Port: defaultPort, TargetPort: intstr.FromInt(defaultPort), Protocol: corev1.ProtocolTCP, NodePort: 30123,
		}}},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()
	r := &HomeAssistantReconciler{Client: kube, Scheme: scheme}

	ha.Spec.Alpha = &hav1.AlphaSpec{MigrationRestore: &hav1.MigrationRestoreAlphaSpec{Enabled: true}}
	if err := r.reconcileService(ctx, ha); err != nil {
		t.Fatal(err)
	}
	current := &corev1.Service{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(svc), current); err != nil {
		t.Fatal(err)
	}
	if len(current.Spec.Ports) != 2 || current.Spec.Ports[0].NodePort != 30123 || current.Spec.Ports[1].Name != "migration-onboarding" || current.Spec.Ports[1].Port != 8124 {
		t.Fatalf("unexpected opt-in ports: %#v", current.Spec.Ports)
	}
	ha.Spec.Service.NodePort = 30124
	if err := r.reconcileService(ctx, ha); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(svc), current); err != nil {
		t.Fatal(err)
	}
	if current.Spec.Ports[0].NodePort != 30124 {
		t.Fatalf("explicit NodePort change ignored: %#v", current.Spec.Ports)
	}

	ha.Spec.Alpha.MigrationRestore.Enabled = false
	if err := r.reconcileService(ctx, ha); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(svc), current); err != nil {
		t.Fatal(err)
	}
	if len(current.Spec.Ports) != 1 || current.Spec.Ports[0].Name != "http" || current.Spec.Ports[0].NodePort != 30124 {
		t.Fatalf("unexpected opt-out ports: %#v", current.Spec.Ports)
	}
}
