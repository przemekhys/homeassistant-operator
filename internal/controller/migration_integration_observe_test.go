package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	var calls atomic.Int32
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			calls.Add(1)
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
	integration.Status.Conditions = []metav1.Condition{{Type: conditionTypeReady, Status: metav1.ConditionTrue, Reason: "PreviouslyReady"}}
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
	if calls.Load() != 1 {
		t.Fatalf("HA requests = %d, want 1", calls.Load())
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
	if ready := meta.FindStatusCondition(stored.Status.Conditions, conditionTypeReady); ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("stale Ready condition: %#v", ready)
	}
	stored.Annotations[migrationTargetUIDAnnotation] = "wrong-uid"
	if err := kube.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(stored)}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("target UID mismatch triggered an HA request")
	}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(stored), stored); err != nil {
		t.Fatal(err)
	}
	condition = meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
	if condition == nil || condition.Reason != "TargetIdentityMismatch" {
		t.Fatalf("condition = %#v", condition)
	}
	for _, tc := range []struct {
		name, reason string
		mutate       func(*hav1.HomeAssistantIntegration)
	}{
		{"paused", "MigrationPaused", func(x *hav1.HomeAssistantIntegration) { x.Annotations[migrationPausedAnnotation] = "true" }},
		{"incomplete", "IncompleteIdentity", func(x *hav1.HomeAssistantIntegration) {
			delete(x.Annotations, migrationPausedAnnotation)
			delete(x.Annotations, migrationSourceIDAnnotation)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.mutate(stored)
			if err := kube.Update(context.Background(), stored); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(stored)}); err != nil {
				t.Fatal(err)
			}
			if err := kube.Get(context.Background(), client.ObjectKeyFromObject(stored), stored); err != nil {
				t.Fatal(err)
			}
			adoption := meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
			ready := meta.FindStatusCondition(stored.Status.Conditions, conditionTypeReady)
			if adoption == nil || adoption.Reason != tc.reason || ready == nil || ready.Status != metav1.ConditionFalse {
				t.Fatalf("adoption=%#v ready=%#v", adoption, ready)
			}
			if calls.Load() != 1 {
				t.Fatal("blocked path contacted HA")
			}
		})
	}
	t.Run("deletion preserves HA and unrelated finalizer", func(t *testing.T) {
		now := metav1.Now()
		deleting := integration.DeepCopy()
		deleting.DeletionTimestamp = &now
		deleting.Finalizers = []string{integrationFinalizerName, "example.test/keep"}
		deleting.Status.EntryID = "wanted"
		store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(deleting).
			WithObjects(deleting, ha, secret).Build()
		reconciler := &HomeAssistantIntegrationReconciler{
			Client: store, NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
		}
		before := calls.Load()
		key := client.ObjectKeyFromObject(deleting)
		if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		got := &hav1.HomeAssistantIntegration{}
		if err := store.Get(context.Background(), key, got); err != nil {
			t.Fatal(err)
		}
		if len(got.Finalizers) != 1 || got.Finalizers[0] != "example.test/keep" {
			t.Fatalf("unexpected finalizers: %v", got.Finalizers)
		}
		if calls.Load() != before {
			t.Fatal("deleting imported integration contacted HA")
		}
	})
}
