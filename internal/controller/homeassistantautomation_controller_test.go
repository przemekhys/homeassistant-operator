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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

var _ = Describe("HomeAssistantAutomation Controller", func() {
	const (
		haName    = "test-ha-auto"
		namespace = "default"
		timeout   = time.Second * 10
		interval  = time.Millisecond * 250
	)

	var (
		reconciler  *HomeAssistantAutomationReconciler
		mockServer  *httptest.Server
		putRequests chan string
		delRequests chan string
	)

	// Helper to create a test automation CR
	createTestAutomation := func(
		name string,
		haRef string,
		alias string,
		triggers []runtime.RawExtension,
		actions []runtime.RawExtension,
	) *hav1alpha1.HomeAssistantAutomation {
		triggerList := make([]hav1alpha1.AutomationTrigger, len(triggers))
		for i, t := range triggers {
			triggerList[i] = hav1alpha1.AutomationTrigger{RawExtension: t}
		}
		actionList := make([]hav1alpha1.AutomationAction, len(actions))
		for i, a := range actions {
			actionList[i] = hav1alpha1.AutomationAction{RawExtension: a}
		}
		return &hav1alpha1.HomeAssistantAutomation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: hav1alpha1.HomeAssistantAutomationSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{
					Name: haRef,
				},
				Alias:    alias,
				Triggers: triggerList,
				Actions:  actionList,
			},
		}
	}

	rawExt := func(data map[string]interface{}) runtime.RawExtension {
		raw, err := json.Marshal(data)
		Expect(err).NotTo(HaveOccurred())
		return runtime.RawExtension{Raw: raw}
	}

	defaultTrigger := func() runtime.RawExtension {
		return rawExt(map[string]interface{}{"platform": "sun", "event": "sunset"})
	}

	defaultAction := func() runtime.RawExtension {
		return rawExt(map[string]interface{}{
			"service": "light.turn_on",
			"target":  map[string]interface{}{"entity_id": "light.living_room"},
		})
	}

	reconcileAutomation := func(name string) (reconcile.Result, error) {
		return reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
		})
	}

	reconcileAutomationTwice := func(name string) error {
		if _, err := reconcileAutomation(name); err != nil {
			return err
		}
		_, err := reconcileAutomation(name)
		return err
	}

	// Sets up the API token for tests that need a successful PUT
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
		putRequests = make(chan string, 20)
		delRequests = make(chan string, 20)

		// Start mock HA server
		mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/config/"):
				// POST /api/config/automation/config/{id} — create/update
				putRequests <- r.URL.Path
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodDelete:
				// DELETE /api/config/automation/config/{id}
				delRequests <- r.URL.Path
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodGet && r.URL.Path == "/api/config":
				// IsComponentLoaded check
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"components":["automation","scene","script"],"version":"2024.1.0"}`))
			case r.Method == http.MethodPost:
				// Service calls for hot-reload
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`[]`))
			default:
				http.NotFound(w, r)
			}
		}))

		reconciler = &HomeAssistantAutomationReconciler{
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
		// Cleanup automations (trigger finalizer reconcile)
		autoList := &hav1alpha1.HomeAssistantAutomationList{}
		_ = k8sClient.List(ctx, autoList)
		for i := range autoList.Items {
			_ = k8sClient.Delete(ctx, &autoList.Items[i])
			_, _ = reconcileAutomation(autoList.Items[i].Name)
		}
		Eventually(func() int {
			list := &hav1alpha1.HomeAssistantAutomationList{}
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
		It("should set Ready=False when referenced HomeAssistant does not exist", func() {
			auto := createTestAutomation("auto-no-ha", "non-existent-ha", "Test",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			Expect(k8sClient.Create(ctx, auto)).To(Succeed())

			// First reconcile adds finalizer
			_, err := reconcileAutomation("auto-no-ha")
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile validates HA ref
			result, err := reconcileAutomation("auto-no-ha")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Second))

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantAutomation{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "auto-no-ha", Namespace: namespace}, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Reason).To(Equal(reasonInvalidAutomation))
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("When API token not available", func() {
		It("should requeue 30s without setting Ready=True", func() {
			// No token set up — ha.Status.Bootstrap is nil
			auto := createTestAutomation("auto-no-token", haName, "No Token",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			Expect(k8sClient.Create(ctx, auto)).To(Succeed())

			// First reconcile adds finalizer
			_, err := reconcileAutomation("auto-no-token")
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile hits token check
			result, err := reconcileAutomation("auto-no-token")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Ready condition should NOT be True
			Consistently(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantAutomation{}
				nn := types.NamespacedName{Name: "auto-no-token", Namespace: namespace}
				g.Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(condition).To(Or(BeNil(), WithTransform(
					func(c *metav1.Condition) metav1.ConditionStatus { return c.Status },
					Equal(metav1.ConditionFalse),
				)))
			}, time.Second*2, interval).Should(Succeed())
		})
	})

	Context("When PUT succeeds (with mock HA server)", func() {
		BeforeEach(func() {
			setupToken()
		})

		It("should set Ready=True after successful reconcile", func() {
			auto := createTestAutomation("auto-ready", haName, "Sunset Lights",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			Expect(k8sClient.Create(ctx, auto)).To(Succeed())
			Expect(reconcileAutomationTwice("auto-ready")).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantAutomation{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "auto-ready", Namespace: namespace}, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(condition.Reason).To(Equal(reasonAutomationGenerated))
			}, timeout, interval).Should(Succeed())
		})

		It("should set AutomationHash in status", func() {
			auto := createTestAutomation("auto-hash", haName, "Hash Test",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			Expect(k8sClient.Create(ctx, auto)).To(Succeed())
			Expect(reconcileAutomationTwice("auto-hash")).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantAutomation{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "auto-hash", Namespace: namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.AutomationHash).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})

		It("should set ObservedGeneration matching metadata.generation", func() {
			auto := createTestAutomation("auto-obsgen", haName, "ObsGen",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			Expect(k8sClient.Create(ctx, auto)).To(Succeed())
			Expect(reconcileAutomationTwice("auto-obsgen")).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantAutomation{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "auto-obsgen", Namespace: namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.ObservedGeneration).To(Equal(updated.Generation))
			}, timeout, interval).Should(Succeed())
		})

		It("should produce stable hash on repeated reconciles", func() {
			auto := createTestAutomation("auto-stable-hash", haName, "Stable",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			Expect(k8sClient.Create(ctx, auto)).To(Succeed())
			Expect(reconcileAutomationTwice("auto-stable-hash")).To(Succeed())

			var firstHash string
			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantAutomation{}
				nn := types.NamespacedName{Name: "auto-stable-hash", Namespace: namespace}
				g.Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
				firstHash = updated.Status.AutomationHash
				g.Expect(firstHash).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			Expect(reconcileAutomationTwice("auto-stable-hash")).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantAutomation{}
				nn := types.NamespacedName{Name: "auto-stable-hash", Namespace: namespace}
				g.Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
				g.Expect(updated.Status.AutomationHash).To(Equal(firstHash))
			}, timeout, interval).Should(Succeed())
		})

		It("should change hash when triggers change", func() {
			auto := createTestAutomation("auto-hash-change", haName, "Hash Change",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			Expect(k8sClient.Create(ctx, auto)).To(Succeed())
			Expect(reconcileAutomationTwice("auto-hash-change")).To(Succeed())

			var initialHash string
			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantAutomation{}
				nn := types.NamespacedName{Name: "auto-hash-change", Namespace: namespace}
				g.Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
				initialHash = updated.Status.AutomationHash
				g.Expect(initialHash).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Update triggers
			Eventually(func() error {
				updated := &hav1alpha1.HomeAssistantAutomation{}
				nn := types.NamespacedName{Name: "auto-hash-change", Namespace: namespace}
				if err := k8sClient.Get(ctx, nn, updated); err != nil {
					return err
				}
				updated.Spec.Triggers = []hav1alpha1.AutomationTrigger{
					{RawExtension: rawExt(map[string]interface{}{"platform": "time", "at": "06:00:00"})},
				}
				return k8sClient.Update(ctx, updated)
			}, timeout, interval).Should(Succeed())

			Expect(reconcileAutomationTwice("auto-hash-change")).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantAutomation{}
				nn := types.NamespacedName{Name: "auto-hash-change", Namespace: namespace}
				g.Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
				g.Expect(updated.Status.AutomationHash).NotTo(Equal(initialHash))
			}, timeout, interval).Should(Succeed())
		})

		It("should call PutAutomation via HA API on reconcile", func() {
			auto := createTestAutomation("auto-put-check", haName, "PUT Check",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			Expect(k8sClient.Create(ctx, auto)).To(Succeed())
			Expect(reconcileAutomationTwice("auto-put-check")).To(Succeed())

			Eventually(putRequests, timeout, interval).Should(Receive(ContainSubstring("auto-put-check")))
		})

		It("should call DeleteAutomation via HA API when resource deleted", func() {
			auto := createTestAutomation("auto-del-api", haName, "Delete API",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			Expect(k8sClient.Create(ctx, auto)).To(Succeed())
			Expect(reconcileAutomationTwice("auto-del-api")).To(Succeed())

			// Delete the CR
			toDelete := &hav1alpha1.HomeAssistantAutomation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "auto-del-api", Namespace: namespace}, toDelete)).To(Succeed())
			Expect(k8sClient.Delete(ctx, toDelete)).To(Succeed())

			// Reconcile to trigger finalizer
			_, _ = reconcileAutomation("auto-del-api")

			Eventually(delRequests, timeout, interval).Should(Receive(ContainSubstring("auto-del-api")))
		})
	})

	Context("When PUT fails (server returns error)", func() {
		It("should set Ready=False and requeue 30s", func() {
			// Create a server that returns 500
			failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message": "internal error"}`))
			}))
			defer failServer.Close()

			reconciler.NewHAClient = func(_ string) *haclient.Client {
				return haclient.NewClient(failServer.URL)
			}

			setupToken()

			auto := createTestAutomation("auto-put-fail", haName, "PUT Fail",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			Expect(k8sClient.Create(ctx, auto)).To(Succeed())

			// First reconcile: finalizer
			_, err := reconcileAutomation("auto-put-fail")
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile: PUT fails
			result, err := reconcileAutomation("auto-put-fail")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			Eventually(func(g Gomega) {
				updated := &hav1alpha1.HomeAssistantAutomation{}
				nn := types.NamespacedName{Name: "auto-put-fail", Namespace: namespace}
				g.Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("When converting automationToYaml", func() {
		It("should convert triggers, conditions, and actions correctly", func() {
			auto := createTestAutomation("auto-yaml", haName, "Full Auto",
				[]runtime.RawExtension{
					rawExt(map[string]interface{}{"platform": "sun", "event": "sunset"}),
				},
				[]runtime.RawExtension{
					rawExt(map[string]interface{}{
						"service": "light.turn_on",
						"target":  map[string]interface{}{"entity_id": "light.porch"},
					}),
				},
			)
			auto.Spec.Conditions = []hav1alpha1.AutomationCondition{
				{RawExtension: rawExt(map[string]interface{}{
					"condition": "state",
					"entity_id": "input_boolean.guest_mode",
					"state":     "on",
				})},
			}

			yamlMap, err := reconciler.automationToYaml(auto)
			Expect(err).NotTo(HaveOccurred())
			Expect(yamlMap).To(HaveKey("trigger"))
			Expect(yamlMap).To(HaveKey("condition"))
			Expect(yamlMap).To(HaveKey("action"))
			Expect(yamlMap["alias"]).To(Equal("Full Auto"))
		})

		It("should auto-generate ID from CR name when spec.id is not set", func() {
			auto := createTestAutomation("my-sunset-automation", haName, "Sunset",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})

			yamlMap, err := reconciler.automationToYaml(auto)
			Expect(err).NotTo(HaveOccurred())
			Expect(yamlMap["id"]).To(Equal("my-sunset-automation"))
		})

		It("should use explicit spec.id when set", func() {
			auto := createTestAutomation("auto-explicit-id", haName, "Custom ID",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			auto.Spec.ID = "custom-automation-id"

			yamlMap, err := reconciler.automationToYaml(auto)
			Expect(err).NotTo(HaveOccurred())
			Expect(yamlMap["id"]).To(Equal("custom-automation-id"))
		})

		It("should map Go field names to HA format (maxExceeded, initialState, mode)", func() {
			auto := createTestAutomation("auto-fields", haName, "Field Map",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			auto.Spec.MaxExceeded = "error"
			auto.Spec.InitialState = ptr.To(true)
			auto.Spec.Mode = hav1alpha1.AutomationModeParallel
			max := int32(5)
			auto.Spec.Max = &max

			yamlMap, err := reconciler.automationToYaml(auto)
			Expect(err).NotTo(HaveOccurred())
			Expect(yamlMap["max_exceeded"]).To(Equal("error"))
			Expect(yamlMap["initial_state"]).To(BeTrue())
			Expect(yamlMap["mode"]).To(Equal("parallel"))
			Expect(yamlMap["max"]).To(Equal(int32(5)))
		})

		It("should include description when provided", func() {
			auto := createTestAutomation("auto-desc", haName, "Described",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})
			auto.Spec.Description = "Turns on lights at sunset"

			yamlMap, err := reconciler.automationToYaml(auto)
			Expect(err).NotTo(HaveOccurred())
			Expect(yamlMap["description"]).To(Equal("Turns on lights at sunset"))
		})

		It("should omit conditions key when no conditions specified", func() {
			auto := createTestAutomation("auto-no-cond", haName, "No Cond",
				[]runtime.RawExtension{defaultTrigger()}, []runtime.RawExtension{defaultAction()})

			yamlMap, err := reconciler.automationToYaml(auto)
			Expect(err).NotTo(HaveOccurred())
			Expect(yamlMap).NotTo(HaveKey("condition"))
		})
	})
})
