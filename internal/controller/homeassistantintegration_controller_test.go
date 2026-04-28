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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

var _ = Describe("HomeAssistantIntegration Controller", func() {
	const (
		haName    = "test-ha-int"
		namespace = "default"
		timeout   = time.Second * 10
		interval  = time.Millisecond * 250
	)

	var (
		reconciler     *HomeAssistantIntegrationReconciler
		mockServer     *httptest.Server
		flowRequests   chan string
		deleteRequests chan string
	)

	reconcileIntegration := func(name string) (reconcile.Result, error) {
		return reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
		})
	}

	reconcileIntegrationTwice := func(name string) error {
		if _, err := reconcileIntegration(name); err != nil {
			return err
		}
		_, err := reconcileIntegration(name)
		return err
	}

	setupToken := func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      haName + "-api-token",
				Namespace: namespace,
			},
			Data: map[string][]byte{
				"token": []byte("test-token"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		ha := &hav1alpha1.HomeAssistant{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha)).To(Succeed())
		ha.Status.Bootstrap = &hav1alpha1.BootstrapStatus{
			APITokenSecretName: haName + "-api-token",
		}
		Expect(k8sClient.Status().Update(ctx, ha)).To(Succeed())
	}

	BeforeEach(func() {
		flowRequests = make(chan string, 20)
		deleteRequests = make(chan string, 20)

		// Start mock HA server
		mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/config/config_entries/entry":
				// ListConfigEntries — return empty list by default
				_ = json.NewEncoder(w).Encode([]haclient.ConfigEntry{})

			case r.Method == http.MethodPost && r.URL.Path == "/api/config/config_entries/flow":
				// StartConfigFlow — return a create_entry response immediately (zero-config style)
				flowRequests <- r.URL.Path
				result, _ := json.Marshal(map[string]interface{}{
					"entry_id": "test-entry-id-001",
					"domain":   "recorder",
					"title":    "Recorder",
				})
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"flow_id": "flow-test-001",
					"type":    "create_entry",
					"title":   "Recorder",
					"result":  json.RawMessage(result),
				})

			case r.Method == http.MethodDelete:
				deleteRequests <- r.URL.Path
				w.WriteHeader(http.StatusOK)

			default:
				http.NotFound(w, r)
			}
		}))

		reconciler = &HomeAssistantIntegrationReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: events.NewFakeRecorder(100),
			NewHAClient: func(_ string) *haclient.Client {
				return haclient.NewClient(mockServer.URL)
			},
		}

		ha := &hav1alpha1.HomeAssistant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      haName,
				Namespace: namespace,
			},
			Spec: hav1alpha1.HomeAssistantSpec{Version: "stable"},
		}
		Expect(k8sClient.Create(ctx, ha)).To(Succeed())
	})

	AfterEach(func() {
		// Cleanup integrations (trigger finalizer reconcile)
		intList := &hav1alpha1.HomeAssistantIntegrationList{}
		_ = k8sClient.List(ctx, intList)
		for i := range intList.Items {
			_ = k8sClient.Delete(ctx, &intList.Items[i])
			_, _ = reconcileIntegration(intList.Items[i].Name)
		}
		Eventually(func() int {
			list := &hav1alpha1.HomeAssistantIntegrationList{}
			_ = k8sClient.List(ctx, list)
			return len(list.Items)
		}, timeout, interval).Should(Equal(0))

		// Cleanup secrets
		secretList := &corev1.SecretList{}
		_ = k8sClient.List(ctx, secretList)
		for i := range secretList.Items {
			_ = k8sClient.Delete(ctx, &secretList.Items[i])
		}

		// Cleanup HAs
		haList := &hav1alpha1.HomeAssistantList{}
		_ = k8sClient.List(ctx, haList)
		for i := range haList.Items {
			_ = k8sClient.Delete(ctx, &haList.Items[i])
		}

		mockServer.Close()
	})

	Context("When validating HomeAssistant reference", func() {
		It("should set IntegrationReady=False when referenced HomeAssistant does not exist", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-no-ha",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: "non-existent-ha"},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())

			// First reconcile adds finalizer
			_, err := reconcileIntegration("int-no-ha")
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile validates HA ref
			result, err := reconcileIntegration("int-no-ha")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "int-no-ha", Namespace: namespace}, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Reason).To(Equal(reasonIntegrationHANotReady))
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("When API token not available", func() {
		It("should requeue 30s with IntegrationReady=False", func() {
			// No token set up — ha.Status.Bootstrap is nil
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-no-token",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())

			// First reconcile adds finalizer
			_, err := reconcileIntegration("int-no-token")
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile hits token check
			result, err := reconcileIntegration("int-no-token")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				nn := types.NamespacedName{Name: "int-no-token", Namespace: namespace}
				g.Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Reason).To(Equal(reasonIntegrationTokenNotAvailable))
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("When HA API succeeds (with mock server)", func() {
		BeforeEach(func() {
			setupToken()
		})

		It("should add finalizer on first reconcile", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-finalizer",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())

			_, err := reconcileIntegration("int-finalizer")
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				nn := types.NamespacedName{Name: "int-finalizer", Namespace: namespace}
				g.Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
				g.Expect(updated.Finalizers).To(ContainElement(integrationFinalizerName))
			}, timeout, interval).Should(Succeed())
		})

		It("should set IntegrationReady=True after successful config flow", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-ready",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())
			Expect(reconcileIntegrationTwice("int-ready")).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "int-ready", Namespace: namespace}, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(condition.Reason).To(Equal(reasonIntegrationConfigured))
			}, timeout, interval).Should(Succeed())
		})

		It("should store EntryID in status after successful config flow", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-entryid",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())
			Expect(reconcileIntegrationTwice("int-entryid")).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "int-entryid", Namespace: namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.EntryID).To(Equal("test-entry-id-001"))
			}, timeout, interval).Should(Succeed())
		})

		It("should set ConfigHash in status", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-hash",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())
			Expect(reconcileIntegrationTwice("int-hash")).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "int-hash", Namespace: namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.ConfigHash).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})

		It("should set ObservedGeneration matching metadata.generation", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-obsgen",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())
			Expect(reconcileIntegrationTwice("int-obsgen")).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "int-obsgen", Namespace: namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.ObservedGeneration).To(Equal(updated.Generation))
			}, timeout, interval).Should(Succeed())
		})

		It("should call StartConfigFlow via HA API on reconcile", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-flow-check",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())
			Expect(reconcileIntegrationTwice("int-flow-check")).To(Succeed())

			Eventually(flowRequests, timeout, interval).Should(Receive())
		})

		It("should set IntegrationReady=True when integration already configured (adopt)", func() {
			// Override mock to return an existing entry (already configured)
			mockServer.Close()
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/config/config_entries/entry":
					entries := []haclient.ConfigEntry{
						{EntryID: "existing-entry-999", Domain: "mqtt", Title: "MQTT", State: "loaded"},
					}
					_ = json.NewEncoder(w).Encode(entries)
				default:
					http.NotFound(w, r)
				}
			}))
			reconciler.NewHAClient = func(_ string) *haclient.Client {
				return haclient.NewClient(mockServer.URL)
			}

			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-adopt",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "mqtt",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())
			Expect(reconcileIntegrationTwice("int-adopt")).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "int-adopt", Namespace: namespace}, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(condition.Reason).To(Equal(reasonAlreadyConfigured))
				g.Expect(updated.Status.EntryID).To(Equal("existing-entry-999"))
			}, timeout, interval).Should(Succeed())
		})

		It("should resolve configuration from plain values", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-plain-config",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
					Configuration: map[string]hav1alpha1.IntegrationValue{
						"broker": {Value: ptr.To("mosquitto.default.svc")},
					},
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())

			// Use a server that expects a form step submission
			mockServer.Close()
			submitted := make(chan map[string]interface{}, 1)
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/config/config_entries/entry":
					_ = json.NewEncoder(w).Encode([]haclient.ConfigEntry{})
				case r.Method == http.MethodPost && r.URL.Path == "/api/config/config_entries/flow":
					// Return a form step
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"flow_id":     "flow-form-001",
						"type":        "form",
						"data_schema": []interface{}{},
					})
				case r.Method == http.MethodPost:
					// Submit step
					var body map[string]interface{}
					_ = json.NewDecoder(r.Body).Decode(&body)
					submitted <- body
					result, _ := json.Marshal(map[string]interface{}{
						"entry_id": "submitted-entry-001",
						"domain":   "recorder",
						"title":    "Recorder",
					})
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"flow_id": "flow-form-001",
						"type":    "create_entry",
						"result":  json.RawMessage(result),
					})
				default:
					http.NotFound(w, r)
				}
			}))
			reconciler.NewHAClient = func(_ string) *haclient.Client {
				return haclient.NewClient(mockServer.URL)
			}

			Expect(reconcileIntegrationTwice("int-plain-config")).To(Succeed())

			// Verify the submitted data contains our value
			Eventually(submitted, timeout, interval).Should(Receive(HaveKeyWithValue("broker", "mosquitto.default.svc")))
		})

		It("should submit jsonValue fields as native JSON objects", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-json-config",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "openweathermap",
					Configuration: map[string]hav1alpha1.IntegrationValue{
						"api_key":  {Value: ptr.To("test-api-key")},
						"location": {JSONValue: ptr.To(`{"latitude": 54.17708, "longitude": 18.557}`)},
						"mode":     {Value: ptr.To("forecast")},
					},
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())

			mockServer.Close()
			submitted := make(chan map[string]interface{}, 1)
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/config/config_entries/entry":
					_ = json.NewEncoder(w).Encode([]haclient.ConfigEntry{})
				case r.Method == http.MethodPost && r.URL.Path == "/api/config/config_entries/flow":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"flow_id": "flow-json-001", "type": "form", "data_schema": []interface{}{},
					})
				case r.Method == http.MethodPost:
					var body map[string]interface{}
					_ = json.NewDecoder(r.Body).Decode(&body)
					submitted <- body
					result, _ := json.Marshal(map[string]interface{}{
						"entry_id": "json-entry-001", "domain": "openweathermap", "title": "OpenWeatherMap",
					})
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"flow_id": "flow-json-001", "type": "create_entry", "result": json.RawMessage(result),
					})
				default:
					http.NotFound(w, r)
				}
			}))
			reconciler.NewHAClient = func(_ string) *haclient.Client { return haclient.NewClient(mockServer.URL) }

			Expect(reconcileIntegrationTwice("int-json-config")).To(Succeed())

			Eventually(submitted, timeout, interval).Should(Receive(And(
				HaveKeyWithValue("api_key", "test-api-key"),
				HaveKeyWithValue("mode", "forecast"),
				HaveKeyWithValue("location", BeAssignableToTypeOf(map[string]interface{}{})),
			)))
		})

		It("should fail resolution when jsonValue is invalid JSON", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-bad-json",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "openweathermap",
					Configuration: map[string]hav1alpha1.IntegrationValue{
						"location": {JSONValue: ptr.To(`not valid json`)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())
			Expect(reconcileIntegrationTwice("int-bad-json")).To(Succeed())

			updated := &hav1alpha1.HomeAssistantIntegration{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "int-bad-json", Namespace: namespace}, updated)).To(Succeed())
			cond := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(reasonSecretResolutionFailed))
		})

		It("should resolve configuration from Kubernetes Secret", func() {
			// Create a secret with the broker value
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mqtt-secret",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"broker-host": []byte("secret-broker.default.svc"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-secret-config",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
					Configuration: map[string]hav1alpha1.IntegrationValue{
						"broker": {
							SecretKeyRef: &hav1alpha1.IntegrationSecretKeyRef{
								Name: "mqtt-secret",
								Key:  "broker-host",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())

			submitted := make(chan map[string]interface{}, 1)
			mockServer.Close()
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/config/config_entries/entry":
					_ = json.NewEncoder(w).Encode([]haclient.ConfigEntry{})
				case r.Method == http.MethodPost && r.URL.Path == "/api/config/config_entries/flow":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"flow_id": "flow-sec-001",
						"type":    "form",
					})
				case r.Method == http.MethodPost:
					var body map[string]interface{}
					_ = json.NewDecoder(r.Body).Decode(&body)
					submitted <- body
					result, _ := json.Marshal(map[string]interface{}{
						"entry_id": "sec-entry-001",
						"domain":   "recorder",
						"title":    "Recorder",
					})
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"flow_id": "flow-sec-001",
						"type":    "create_entry",
						"result":  json.RawMessage(result),
					})
				default:
					http.NotFound(w, r)
				}
			}))
			reconciler.NewHAClient = func(_ string) *haclient.Client {
				return haclient.NewClient(mockServer.URL)
			}

			Expect(reconcileIntegrationTwice("int-secret-config")).To(Succeed())

			Eventually(submitted, timeout, interval).Should(Receive(HaveKeyWithValue("broker", "secret-broker.default.svc")))
		})

		It("should set IntegrationReady=False when Secret key not found", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "missing-key-secret",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"other-key": []byte("value"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-missing-key",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
					Configuration: map[string]hav1alpha1.IntegrationValue{
						"broker": {
							SecretKeyRef: &hav1alpha1.IntegrationSecretKeyRef{
								Name: "missing-key-secret",
								Key:  "nonexistent-key",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())

			// First reconcile: finalizer
			_, err := reconcileIntegration("int-missing-key")
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile: secret resolution fails
			result, err := reconcileIntegration("int-missing-key")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				nn := types.NamespacedName{Name: "int-missing-key", Namespace: namespace}
				g.Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Reason).To(Equal(reasonSecretResolutionFailed))
			}, timeout, interval).Should(Succeed())
		})

		It("should call RemoveConfigEntry on deletion", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-delete-api",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())
			Expect(reconcileIntegrationTwice("int-delete-api")).To(Succeed())

			// Verify entry was stored
			Eventually(func() string {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "int-delete-api", Namespace: namespace}, updated)
				return updated.Status.EntryID
			}, timeout, interval).ShouldNot(BeEmpty())

			// Delete the CR
			toDelete := &hav1alpha1.HomeAssistantIntegration{}
			nnDel := types.NamespacedName{Name: "int-delete-api", Namespace: namespace}
			Expect(k8sClient.Get(ctx, nnDel, toDelete)).To(Succeed())
			Expect(k8sClient.Delete(ctx, toDelete)).To(Succeed())

			_, err := reconcileIntegration("int-delete-api")
			Expect(err).NotTo(HaveOccurred())

			Eventually(deleteRequests, timeout, interval).Should(Receive(ContainSubstring("test-entry-id-001")))
		})

		It("should set IntegrationReady=False on config flow failure (ConfigFlowFailed)", func() {
			mockServer.Close()
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/config/config_entries/entry":
					_ = json.NewEncoder(w).Encode([]haclient.ConfigEntry{})
				case r.Method == http.MethodPost && r.URL.Path == "/api/config/config_entries/flow":
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"message":"internal error"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			reconciler.NewHAClient = func(_ string) *haclient.Client {
				return haclient.NewClient(mockServer.URL)
			}

			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-flow-fail",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())

			// First reconcile: adds finalizer
			_, err := reconcileIntegration("int-flow-fail")
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile: StartConfigFlow fails
			result, err := reconcileIntegration("int-flow-fail")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				nn := types.NamespacedName{Name: "int-flow-fail", Namespace: namespace}
				g.Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Reason).To(Equal(reasonConfigFlowFailed))
			}, timeout, interval).Should(Succeed())
		})

		It("should remove finalizer even when HA is unavailable during deletion (best-effort)", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-delete-ha-down",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())
			Expect(reconcileIntegrationTwice("int-delete-ha-down")).To(Succeed())

			// Wait for entryID to be stored
			Eventually(func() string {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "int-delete-ha-down", Namespace: namespace}, updated)
				return updated.Status.EntryID
			}, timeout, interval).ShouldNot(BeEmpty())

			// Simulate HA unavailable: DELETE returns 503
			mockServer.Close()
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			reconciler.NewHAClient = func(_ string) *haclient.Client {
				return haclient.NewClient(mockServer.URL)
			}

			// Delete the CR
			toDelete := &hav1alpha1.HomeAssistantIntegration{}
			nnDown := types.NamespacedName{Name: "int-delete-ha-down", Namespace: namespace}
			Expect(k8sClient.Get(ctx, nnDown, toDelete)).To(Succeed())
			Expect(k8sClient.Delete(ctx, toDelete)).To(Succeed())

			// Reconcile — must not return error (best-effort)
			_, err := reconcileIntegration("int-delete-ha-down")
			Expect(err).NotTo(HaveOccurred())

			// Finalizer must be removed so Kubernetes can garbage-collect the CR
			Eventually(func() bool {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				nn := types.NamespacedName{Name: "int-delete-ha-down", Namespace: namespace}
				if err := k8sClient.Get(ctx, nn, updated); err != nil {
					return true // already gone
				}
				for _, f := range updated.Finalizers {
					if f == integrationFinalizerName {
						return false
					}
				}
				return true
			}, timeout, interval).Should(BeTrue())
		})

		It("should be idempotent when entry already exists (no extra StartConfigFlow calls)", func() {
			// Mock: entry always present in ListConfigEntries → adoption path, no flow
			mockServer.Close()
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/config/config_entries/entry":
					_ = json.NewEncoder(w).Encode([]haclient.ConfigEntry{
						{EntryID: "idem-entry-001", Domain: "recorder", State: "loaded"},
					})
				case r.Method == http.MethodPost && r.URL.Path == "/api/config/config_entries/flow":
					// Should NOT be called in idempotent path
					flowRequests <- r.URL.Path
					w.WriteHeader(http.StatusInternalServerError)
				default:
					http.NotFound(w, r)
				}
			}))
			reconciler.NewHAClient = func(_ string) *haclient.Client {
				return haclient.NewClient(mockServer.URL)
			}

			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-idempotent",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())

			// Three consecutive reconciles: 1=finalizer, 2=adopt, 3=idempotent no-op
			for i := 0; i < 3; i++ {
				_, err := reconcileIntegration("int-idempotent")
				Expect(err).NotTo(HaveOccurred())
			}

			// StartConfigFlow must never have been called
			Consistently(flowRequests, time.Second, interval).ShouldNot(Receive())

			// Status must be Ready with entryID from adoption
			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				nn := types.NamespacedName{Name: "int-idempotent", Namespace: namespace}
				g.Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(updated.Status.EntryID).To(Equal("idem-entry-001"))
			}, timeout, interval).Should(Succeed())
		})

		It("should reconfigure on spec change (delete old entry + start new flow)", func() {
			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-reconfig",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())
			Expect(reconcileIntegrationTwice("int-reconfig")).To(Succeed())

			// Wait for initial entryID
			Eventually(func() string {
				updated := &hav1alpha1.HomeAssistantIntegration{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "int-reconfig", Namespace: namespace}, updated)
				return updated.Status.EntryID
			}, timeout, interval).Should(Equal("test-entry-id-001"))

			// Drain initial flow request
			Eventually(flowRequests, timeout, interval).Should(Receive())

			// Change spec configuration → triggers hash mismatch on next reconcile
			updated := &hav1alpha1.HomeAssistantIntegration{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "int-reconfig", Namespace: namespace}, updated)).To(Succeed())
			updated.Spec.Configuration = map[string]hav1alpha1.IntegrationValue{
				"broker": {Value: ptr.To("new-broker.svc")},
			}
			Expect(k8sClient.Update(ctx, updated)).To(Succeed())

			// Reconcile with new spec: hash changed → delete old entry → new flow
			_, err := reconcileIntegration("int-reconfig")
			Expect(err).NotTo(HaveOccurred())

			// Old entry must be deleted
			Eventually(deleteRequests, timeout, interval).Should(Receive(ContainSubstring("test-entry-id-001")))
			// New flow must be started
			Eventually(flowRequests, timeout, interval).Should(Receive())
		})

		It("should re-create entry when stale entryID is no longer in HA", func() {
			// Phase 1: adoption mock (entry always present in ListConfigEntries)
			mockServer.Close()
			adoptServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/config/config_entries/entry":
					_ = json.NewEncoder(w).Encode([]haclient.ConfigEntry{
						{EntryID: "stale-entry-001", Domain: "recorder", State: "loaded"},
					})
				default:
					http.NotFound(w, r)
				}
			}))
			defer adoptServer.Close()
			reconciler.NewHAClient = func(_ string) *haclient.Client {
				return haclient.NewClient(adoptServer.URL)
			}

			integration := &hav1alpha1.HomeAssistantIntegration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "int-stale",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantIntegrationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Domain:           "recorder",
				},
			}
			Expect(k8sClient.Create(ctx, integration)).To(Succeed())
			// Reconcile 1 (finalizer) + Reconcile 2 (adopt → entryID set)
			Expect(reconcileIntegrationTwice("int-stale")).To(Succeed())

			Eventually(func() string {
				obj := &hav1alpha1.HomeAssistantIntegration{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "int-stale", Namespace: namespace}, obj)
				return obj.Status.EntryID
			}, timeout, interval).Should(Equal("stale-entry-001"))

			// Phase 2: entry vanished from HA (simulate deletion outside operator)
			// New mock: empty ListConfigEntries + handles new flow
			staleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/config/config_entries/entry":
					_ = json.NewEncoder(w).Encode([]haclient.ConfigEntry{})
				case r.Method == http.MethodPost && r.URL.Path == "/api/config/config_entries/flow":
					flowRequests <- r.URL.Path
					result, _ := json.Marshal(map[string]interface{}{
						"entry_id": "new-entry-after-stale",
						"domain":   "recorder",
						"title":    "Recorder",
					})
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"flow_id": "flow-new-001",
						"type":    "create_entry",
						"result":  json.RawMessage(result),
					})
				default:
					http.NotFound(w, r)
				}
			}))
			defer staleServer.Close()
			reconciler.NewHAClient = func(_ string) *haclient.Client {
				return haclient.NewClient(staleServer.URL)
			}

			// Reconcile: verifyEntryExists → false → clear entryID → start new flow
			_, err := reconcileIntegration("int-stale")
			Expect(err).NotTo(HaveOccurred())

			// New flow must be started
			Eventually(flowRequests, timeout, interval).Should(Receive())

			// Status must reflect the new entryID
			Eventually(func() string {
				obj := &hav1alpha1.HomeAssistantIntegration{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "int-stale", Namespace: namespace}, obj)
				return obj.Status.EntryID
			}, timeout, interval).Should(Equal("new-entry-after-stale"))
		})
	})
})
