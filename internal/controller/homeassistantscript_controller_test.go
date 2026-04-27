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
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// simpleScriptAction is a raw JSON action for use in script tests
var simpleScriptAction = hav1alpha1.ScriptAction{
	RawExtension: runtime.RawExtension{Raw: []byte(`{"service":"test.service"}`)},
}

var _ = Describe("HomeAssistantScript Controller", func() {
	const (
		scriptTimeout  = time.Second * 10
		scriptInterval = time.Millisecond * 250
	)

	var (
		reconciler   *HomeAssistantScriptReconciler
		mockServer   *httptest.Server
		postRequests chan string
		delRequests  chan string
	)

	createTestScript := func(
		name, namespace, haRef, alias string,
		sequence []hav1alpha1.ScriptAction,
	) *hav1alpha1.HomeAssistantScript {
		return &hav1alpha1.HomeAssistantScript{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: hav1alpha1.HomeAssistantScriptSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haRef},
				Alias:            alias,
				Sequence:         sequence,
			},
		}
	}

	reconcileScript := func(name, namespace string) (reconcile.Result, error) {
		return reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
		})
	}

	setupScriptToken := func(haName, namespace string) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      haName + "-api-token",
				Namespace: namespace,
			},
			Data: map[string][]byte{"token": []byte("test-token")},
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
		postRequests = make(chan string, 20)
		delRequests = make(chan string, 20)

		mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/config/"):
				// POST /api/config/script/config/{id} — create/update
				postRequests <- r.URL.Path
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodDelete:
				// DELETE /api/config/script/config/{id}
				delRequests <- r.URL.Path
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodGet && r.URL.Path == "/api/config":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"components":["script"],"version":"2024.1.0"}`))
			case r.Method == http.MethodPost:
				// Service calls for hot-reload
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`[]`))
			default:
				http.NotFound(w, r)
			}
		}))

		reconciler = &HomeAssistantScriptReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: events.NewFakeRecorder(100),
			NewHAClient: func(_ string) *haclient.Client {
				return haclient.NewClient(mockServer.URL)
			},
		}
	})

	AfterEach(func() {
		// Cleanup scripts
		scriptList := &hav1alpha1.HomeAssistantScriptList{}
		_ = k8sClient.List(ctx, scriptList)
		for i := range scriptList.Items {
			ns := scriptList.Items[i].Namespace
			name := scriptList.Items[i].Name
			_ = k8sClient.Delete(ctx, &scriptList.Items[i])
			_, _ = reconcileScript(name, ns)
		}

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

	Context("Finalizer Handling", func() {
		const ns = "default"

		BeforeEach(func() {
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ha-script-fin", Namespace: ns},
				Spec:       hav1alpha1.HomeAssistantSpec{},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
		})

		It("should add finalizer on first reconcile", func() {
			script := createTestScript("finalizer-script", ns, "test-ha-script-fin", "Finalizer Test",
				[]hav1alpha1.ScriptAction{simpleScriptAction})
			Expect(k8sClient.Create(ctx, script)).To(Succeed())

			// First reconcile adds finalizer
			_, err := reconcileScript(script.Name, ns)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: script.Name, Namespace: ns}, script); err != nil {
					return false
				}
				for _, f := range script.Finalizers {
					if f == scriptFinalizerName {
						return true
					}
				}
				return false
			}, scriptTimeout, scriptInterval).Should(BeTrue())
		})
	})

	Context("Status Updates (with mock HA server)", func() {
		const haStatusName = "test-ha-script-status"
		const ns = "default"

		BeforeEach(func() {
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: haStatusName, Namespace: ns},
				Spec:       hav1alpha1.HomeAssistantSpec{},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
			setupScriptToken(haStatusName, ns)
		})

		It("should set Ready condition after successful reconciliation", func() {
			script := createTestScript("status-script", ns, haStatusName, "Status Test",
				[]hav1alpha1.ScriptAction{simpleScriptAction})
			Expect(k8sClient.Create(ctx, script)).To(Succeed())

			// Two reconciles: finalizer + actual reconciliation
			_, err := reconcileScript(script.Name, ns)
			Expect(err).NotTo(HaveOccurred())
			_, err = reconcileScript(script.Name, ns)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: script.Name, Namespace: ns}, script); err != nil {
					return false
				}
				for _, cond := range script.Status.Conditions {
					if cond.Type == conditionTypeReady && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, scriptTimeout, scriptInterval).Should(BeTrue())
		})
	})

	Context("Validation", func() {
		const ns = "default"

		It("should requeue 30s when API token missing", func() {
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ha-script-notoken", Namespace: ns},
				Spec:       hav1alpha1.HomeAssistantSpec{},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			script := createTestScript("script-no-token", ns, ha.Name, "No Token",
				[]hav1alpha1.ScriptAction{simpleScriptAction})
			Expect(k8sClient.Create(ctx, script)).To(Succeed())

			// First reconcile adds finalizer
			_, err := reconcileScript(script.Name, ns)
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile hits token check
			result, err := reconcileScript(script.Name, ns)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))
		})
	})
})

// Test helper functions
var _ = Describe("HomeAssistantScript Helper Functions", func() {
	var reconciler *HomeAssistantScriptReconciler

	BeforeEach(func() {
		reconciler = &HomeAssistantScriptReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: events.NewFakeRecorder(100),
		}
	})

	Context("scriptToYaml", func() {
		It("should convert script with basic fields", func() {
			actions := []hav1alpha1.ScriptAction{
				{RawExtension: runtime.RawExtension{
					Raw: []byte(`{"service":"light.turn_on"}`),
				}},
			}

			script := &hav1alpha1.HomeAssistantScript{
				Spec: hav1alpha1.HomeAssistantScriptSpec{
					Alias:       "Test Script",
					Description: "Test description",
					Icon:        "mdi:script",
					Mode:        hav1alpha1.ScriptModeSingle,
					Sequence:    actions,
				},
			}

			yaml, err := reconciler.scriptToYaml(script)
			Expect(err).NotTo(HaveOccurred())
			Expect(yaml).To(HaveKey("alias"))
			Expect(yaml["alias"]).To(Equal("Test Script"))
			Expect(yaml).To(HaveKey("description"))
			Expect(yaml).To(HaveKey("icon"))
			Expect(yaml).To(HaveKey("mode"))
			Expect(yaml).To(HaveKey("sequence"))
		})
	})

	Context("calculateScriptHash", func() {
		It("should generate consistent hash for same script", func() {
			actions := []hav1alpha1.ScriptAction{
				{RawExtension: runtime.RawExtension{
					Raw: []byte(`{"service":"test.service"}`),
				}},
			}

			script := &hav1alpha1.HomeAssistantScript{
				Spec: hav1alpha1.HomeAssistantScriptSpec{
					Alias:    "Hash Test",
					Sequence: actions,
				},
			}

			hash1, err1 := reconciler.calculateScriptHash(script)
			hash2, err2 := reconciler.calculateScriptHash(script)

			Expect(err1).NotTo(HaveOccurred())
			Expect(err2).NotTo(HaveOccurred())
			Expect(hash1).To(Equal(hash2))
			Expect(hash1).NotTo(BeEmpty())
		})
	})
})
