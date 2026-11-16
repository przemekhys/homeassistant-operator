package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestObserveImportedDefinitionNeverWritesHA(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	var calls, sceneState atomic.Int32
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Method != http.MethodGet || r.URL.Path != "/api/config/scene/config/evening" {
				t.Errorf("unexpected HA request %s %s", r.Method, r.URL.Path)
				w.WriteHeader(400)
				return
			}
			if sceneState.Load() == 1 {
				_, _ = w.Write(
					[]byte(
						`{"id":"evening","name":"Changed","entities":{"light.room":{"state":"on"}}}`,
					),
				)
				return
			}
			if sceneState.Load() == 2 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(
				[]byte(
					`{"id":"evening","name":"Evening","entities":{"light.room":{"state":"on"}}}`,
				),
			)
		}),
	)
	defer server.Close()
	scene := &hav1.HomeAssistantScene{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "scene",
			Namespace: "home",
			Annotations: map[string]string{
				migrationImportedAnnotation:  "true",
				migrationSourceIDAnnotation:  "evening",
				migrationTargetUIDAnnotation: "ha-uid",
			},
		},
		Spec: hav1.HomeAssistantSceneSpec{
			ID:               "evening",
			Name:             "Evening",
			HomeAssistantRef: hav1.HomeAssistantReference{Name: "ha"},
			Entities:         []hav1.SceneEntity{{EntityID: "light.room", State: "on"}},
		},
	}
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "home", UID: "ha-uid"},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-homeassistant-api-token", Namespace: "home"},
		Data:       map[string][]byte{"token": []byte("token")},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(scene).
		WithObjects(scene, ha, secret).
		Build()
	r := &HomeAssistantSceneReconciler{
		Client:      kube,
		NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
	}
	result, err := r.Reconcile(
		context.Background(),
		ctrl.Request{NamespacedName: client.ObjectKeyFromObject(scene)},
	)
	if err != nil || result.RequeueAfter == 0 || calls.Load() != 1 {
		t.Fatalf("observation = %#v, %v; calls=%d", result, err, calls.Load())
	}
	stored := &hav1.HomeAssistantScene{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(scene), stored); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
	if condition == nil || condition.Reason != "Adopted" ||
		condition.Status != metav1.ConditionTrue {
		t.Fatalf("condition = %#v", condition)
	}
	for _, step := range []struct {
		state  int32
		reason string
	}{{1, "CanonicalFieldsDiffer"}, {2, "DefinitionUnavailable"}} {
		sceneState.Store(step.state)
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(scene)}); err != nil {
			t.Fatal(err)
		}
		if err := kube.Get(context.Background(), client.ObjectKeyFromObject(scene), stored); err != nil {
			t.Fatal(err)
		}
		condition = meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
		if condition == nil || condition.Status != metav1.ConditionFalse ||
			condition.Reason != step.reason {
			t.Fatalf("state=%d condition=%#v", step.state, condition)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestImportedSceneOverwriteApprovalRemainsBlockedAndCanBeRevoked(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	scene := &hav1.HomeAssistantScene{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "scene",
			Namespace:  "home",
			UID:        "scene-uid",
			Generation: 3,
			Annotations: map[string]string{
				migrationImportedAnnotation:  "true",
				migrationSourceIDAnnotation:  "source-id",
				migrationTargetUIDAnnotation: "ha-uid",
			},
		},
		Spec: hav1.HomeAssistantSceneSpec{
			ID:               "source-id",
			Name:             "Scene",
			HomeAssistantRef: hav1.HomeAssistantReference{Name: "ha"},
		},
	}
	observed := json.RawMessage(`{"id":"source-id","name":"Changed","entities":{}}`)
	comparison := compareMigrationDefinition(scene, "source-id", observed)
	if comparison.State != migrationAdoptionConflict || comparison.ComparisonDigest == "" {
		t.Fatalf("comparison=%#v", comparison)
	}
	scene.Annotations[migrationOverwriteApprovalAnnotation] = fmt.Sprintf(
		`{"version":"v1","approved":true,"objectUID":"scene-uid","generation":3,"targetUID":"ha-uid","sourceID":"source-id","comparisonDigest":%q}`,
		comparison.ComparisonDigest,
	)
	calls := 0
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method != http.MethodGet {
				t.Errorf("unexpected HA write: %s", r.Method)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write(observed)
		}),
	)
	defer server.Close()
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "home", UID: "ha-uid"},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-homeassistant-api-token", Namespace: "home"},
		Data:       map[string][]byte{"token": []byte("token")},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(scene).
		WithObjects(scene, ha, secret).
		Build()
	key := client.ObjectKeyFromObject(scene)
	for i, want := range []string{"UnsafeOverwriteUnsupported", "CanonicalFieldsDiffer"} {
		r := &HomeAssistantSceneReconciler{
			Client:      kube,
			NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
		}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		stored := &hav1.HomeAssistantScene{}
		if err := kube.Get(context.Background(), key, stored); err != nil {
			t.Fatal(err)
		}
		condition := meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
		if condition == nil || condition.Status != metav1.ConditionFalse ||
			condition.Reason != want ||
			!strings.Contains(condition.Message, comparison.ComparisonDigest) {
			t.Fatalf("condition=%#v", condition)
		}
		if i == 0 {
			delete(stored.Annotations, migrationOverwriteApprovalAnnotation)
			if err := kube.Update(context.Background(), stored); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls != 2 {
		t.Fatalf("requests=%d, want 2", calls)
	}
}

func TestImportedScriptAdoptionReobservesWithoutWrites(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	script := &hav1.HomeAssistantScript{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "script",
			Namespace:  "home",
			UID:        "script-uid",
			Generation: 2,
			Annotations: map[string]string{
				migrationImportedAnnotation:  "true",
				migrationSourceIDAnnotation:  "script-id",
				migrationTargetUIDAnnotation: "ha-uid",
			},
		},
		Spec: hav1.HomeAssistantScriptSpec{
			ID:               "script-id",
			Alias:            "Script",
			HomeAssistantRef: hav1.HomeAssistantReference{Name: "ha"},
		},
	}
	script.Status.Conditions = []metav1.Condition{
		{Type: conditionTypeReady, Status: metav1.ConditionTrue, Reason: "PreviouslyReady"},
	}
	definition, err := (&HomeAssistantScriptReconciler{}).scriptToYaml(script)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var scenario atomic.Int32
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Method != http.MethodGet ||
				r.URL.Path != "/api/config/script/config/script-id" {
				t.Errorf("unexpected HA request %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if scenario.Load() == 1 {
				_, _ = w.Write([]byte(`{"alias":"Changed"}`))
				return
			}
			if scenario.Load() == 2 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(payload)
		}),
	)
	defer server.Close()
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "home", UID: "ha-uid"},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-homeassistant-api-token", Namespace: "home"},
		Data:       map[string][]byte{"token": []byte("token")},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(script).
		WithObjects(script, ha, secret).
		Build()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(script)}
	for i := 0; i < 2; i++ {
		r := &HomeAssistantScriptReconciler{
			Client:      kube,
			NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
		}
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		stored := &hav1.HomeAssistantScript{}
		if err := kube.Get(context.Background(), req.NamespacedName, stored); err != nil {
			t.Fatal(err)
		}
		condition := meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
		if condition == nil || condition.Status != metav1.ConditionTrue ||
			condition.Reason != "Adopted" ||
			condition.ObservedGeneration != 2 {
			t.Fatalf("condition=%#v", condition)
		}
		if ready := meta.FindStatusCondition(stored.Status.Conditions, conditionTypeReady); ready == nil ||
			ready.Status != metav1.ConditionFalse {
			t.Fatalf("unsafe ready condition=%#v", ready)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("requests=%d, want two fresh reads", calls.Load())
	}
	stored := &hav1.HomeAssistantScript{}
	for _, step := range []struct {
		state  int32
		reason string
	}{{1, "CanonicalFieldsDiffer"}, {2, "DefinitionUnavailable"}} {
		scenario.Store(step.state)
		r := &HomeAssistantScriptReconciler{
			Client:      kube,
			NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
		}
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if err := kube.Get(context.Background(), req.NamespacedName, stored); err != nil {
			t.Fatal(err)
		}
		condition := meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
		if condition == nil || condition.Status != metav1.ConditionFalse ||
			condition.Reason != step.reason {
			t.Fatalf("state=%d condition=%#v", step.state, condition)
		}
	}
	if err := kube.Get(context.Background(), req.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	stored.Annotations[migrationPausedAnnotation] = "true"
	if err := kube.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	r := &HomeAssistantScriptReconciler{
		Client:      kube,
		NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), req.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
	if calls.Load() != 4 || condition == nil || condition.Status != metav1.ConditionFalse ||
		condition.Reason != "MigrationPaused" {
		t.Fatalf("calls=%d condition=%#v", calls.Load(), condition)
	}
}

func TestImportedAutomationMatchingDefinitionRemainsUnqualified(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	automation := &hav1.HomeAssistantAutomation{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "automation",
			Namespace:  "home",
			UID:        "automation-uid",
			Generation: 3,
			Annotations: map[string]string{
				migrationImportedAnnotation:  "true",
				migrationSourceIDAnnotation:  "source-id",
				migrationTargetUIDAnnotation: "ha-uid",
			},
		},
		Spec: hav1.HomeAssistantAutomationSpec{
			ID:               "source-id",
			Alias:            "Automation",
			HomeAssistantRef: hav1.HomeAssistantReference{Name: "ha"},
		},
	}
	definition, err := (&HomeAssistantAutomationReconciler{}).automationToYaml(automation)
	if err != nil {
		t.Fatal(err)
	}
	delete(definition, "enabled")
	payload, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet ||
				r.URL.Path != "/api/config/automation/config/source-id" {
				t.Errorf("unexpected HA request %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write(payload)
		}),
	)
	defer server.Close()
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "home", UID: "ha-uid"},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-homeassistant-api-token", Namespace: "home"},
		Data:       map[string][]byte{"token": []byte("token")},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(automation).
		WithObjects(automation, ha, secret).
		Build()
	r := &HomeAssistantAutomationReconciler{
		Client:      kube,
		NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
	}
	key := client.ObjectKeyFromObject(automation)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	stored := &hav1.HomeAssistantAutomation{}
	if err := kube.Get(context.Background(), key, stored); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(stored.Status.Conditions, "MigrationAdoption")
	if condition == nil || condition.Status != metav1.ConditionFalse ||
		condition.Reason != "AdoptionNotQualified" {
		t.Fatalf("condition=%#v", condition)
	}
}

func TestImportedDefinitionControllersReadOnlyOnConflictAndUnavailable(t *testing.T) {
	for _, kind := range []string{"automation", "scene", "script"} {
		for _, unavailable := range []bool{false, true} {
			t.Run(
				kind+"/"+map[bool]string{false: "conflict", true: "unavailable"}[unavailable],
				func(t *testing.T) {
					scheme := runtime.NewScheme()
					if err := hav1.AddToScheme(scheme); err != nil {
						t.Fatal(err)
					}
					if err := corev1.AddToScheme(scheme); err != nil {
						t.Fatal(err)
					}
					calls := 0
					server := httptest.NewServer(
						http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							calls++
							if r.Method != http.MethodGet ||
								r.URL.Path != "/api/config/"+kind+"/config/source-id" {
								t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
								w.WriteHeader(400)
								return
							}
							if unavailable {
								w.WriteHeader(http.StatusServiceUnavailable)
								_, _ = w.Write([]byte("private-value"))
								return
							}
							_, _ = w.Write(
								[]byte(`{"id":"source-id","unexpected":"private-value"}`),
							)
						}),
					)
					defer server.Close()
					metadata := metav1.ObjectMeta{
						Name:      "imported",
						Namespace: "home",
						Annotations: map[string]string{
							migrationImportedAnnotation:  "true",
							migrationSourceIDAnnotation:  "source-id",
							migrationTargetUIDAnnotation: "ha-uid",
						},
					}
					ref := hav1.HomeAssistantReference{Name: "ha"}
					var object client.Object
					switch kind {
					case "automation":
						object = &hav1.HomeAssistantAutomation{
							ObjectMeta: metadata,
							Spec: hav1.HomeAssistantAutomationSpec{
								ID:               "source-id",
								HomeAssistantRef: ref,
								Alias:            "Automation",
							},
						}
					case "scene":
						object = &hav1.HomeAssistantScene{
							ObjectMeta: metadata,
							Spec: hav1.HomeAssistantSceneSpec{
								ID:               "source-id",
								HomeAssistantRef: ref,
								Name:             "Scene",
							},
						}
					case "script":
						object = &hav1.HomeAssistantScript{
							ObjectMeta: metadata,
							Spec: hav1.HomeAssistantScriptSpec{
								ID:               "source-id",
								HomeAssistantRef: ref,
								Alias:            "Script",
							},
						}
					}
					ha := &hav1.HomeAssistant{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "ha",
							Namespace: "home",
							UID:       "ha-uid",
						},
					}
					secret := &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "ha-homeassistant-api-token",
							Namespace: "home",
						},
						Data: map[string][]byte{"token": []byte("token")},
					}
					kube := fake.NewClientBuilder().
						WithScheme(scheme).
						WithStatusSubresource(object).
						WithObjects(object, ha, secret).
						Build()
					factory := func(string) *haclient.Client { return haclient.NewClient(server.URL) }
					req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(object)}
					var err error
					switch kind {
					case "automation":
						_, err = (&HomeAssistantAutomationReconciler{Client: kube, NewHAClient: factory}).Reconcile(
							context.Background(),
							req,
						)
					case "scene":
						_, err = (&HomeAssistantSceneReconciler{Client: kube, NewHAClient: factory}).Reconcile(
							context.Background(),
							req,
						)
					case "script":
						_, err = (&HomeAssistantScriptReconciler{Client: kube, NewHAClient: factory}).Reconcile(
							context.Background(),
							req,
						)
					}
					if err != nil || calls != 1 {
						t.Fatalf("reconcile error=%v, HA requests=%d", err, calls)
					}
					stored := object.DeepCopyObject().(client.Object)
					if err := kube.Get(context.Background(), req.NamespacedName, stored); err != nil {
						t.Fatal(err)
					}
					var conditions []metav1.Condition
					switch v := stored.(type) {
					case *hav1.HomeAssistantAutomation:
						conditions = v.Status.Conditions
					case *hav1.HomeAssistantScene:
						conditions = v.Status.Conditions
					case *hav1.HomeAssistantScript:
						conditions = v.Status.Conditions
					}
					condition := meta.FindStatusCondition(conditions, "MigrationAdoption")
					want := "CanonicalFieldsDiffer"
					if unavailable {
						want = "DefinitionUnavailable"
					}
					if condition == nil || condition.Reason != want ||
						condition.Status != metav1.ConditionFalse ||
						strings.Contains(condition.Message, "private-value") {
						t.Fatalf("condition = %#v", condition)
					}
				},
			)
		}
	}
}
