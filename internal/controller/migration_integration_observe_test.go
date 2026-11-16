package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestImportedIntegrationObservationDoesNotStartFlowOrDelete(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			calls++
			if req.Method != http.MethodGet ||
				req.URL.Path != "/api/config/config_entries/entry" {
				t.Errorf("unexpected HA operation: %s %s", req.Method, req.URL.Path)
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write(
				[]byte(
					`[{"entry_id":"other","domain":"mqtt"},{"entry_id":"wanted","domain":"mqtt"}]`,
				),
			)
		}),
	)
	defer server.Close()
	integration := &hav1.HomeAssistantIntegration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "imported",
			Namespace: "home",
			Annotations: map[string]string{
				migrationImportedAnnotation:  "true",
				migrationSourceIDAnnotation:  "wanted",
				migrationTargetUIDAnnotation: "uid",
				migrationAdoptionAnnotation:  migrationAdopted,
			},
		},
		Spec: hav1.HomeAssistantIntegrationSpec{
			HomeAssistantRef: hav1.HomeAssistantReference{Name: "ha"},
			Domain:           "mqtt",
		},
	}
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "home", UID: "uid"},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-homeassistant-api-token", Namespace: "home"},
		Data:       map[string][]byte{"token": []byte("test-token")},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(integration).
		WithObjects(integration, ha, secret).
		Build()
	r := &HomeAssistantIntegrationReconciler{
		Client:      kube,
		NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(integration)}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("HA requests = %d, want 1", calls)
	}
	stored := &hav1.HomeAssistantIntegration{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(integration), stored); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
	if condition == nil || condition.Reason != "IntegrationConfigurationIncomplete" ||
		condition.Status != metav1.ConditionFalse {
		t.Fatalf("condition = %#v", condition)
	}
	stored.Annotations[migrationTargetUIDAnnotation] = "wrong-uid"
	if err := kube.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(stored)}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("target UID mismatch triggered an HA request")
	}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(stored), stored); err != nil {
		t.Fatal(err)
	}
	condition = meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
	if condition == nil || condition.Reason != "TargetIdentityMismatch" {
		t.Fatalf("condition = %#v", condition)
	}
}
