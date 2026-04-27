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
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

var _ = Describe("HomeAssistantScene Controller", func() {
	const (
		sceneTimeout  = time.Second * 10
		sceneInterval = time.Millisecond * 250
	)

	var (
		reconciler   *HomeAssistantSceneReconciler
		mockServer   *httptest.Server
		postRequests chan string
		delRequests  chan string
	)

	createTestScene := func(
		name, namespace, haRef, sceneName string,
		entities []hav1alpha1.SceneEntity,
	) *hav1alpha1.HomeAssistantScene {
		return &hav1alpha1.HomeAssistantScene{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: hav1alpha1.HomeAssistantSceneSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haRef},
				Name:             sceneName,
				Entities:         entities,
			},
		}
	}

	createTestEntity := func(entityID, state string, attrs map[string]interface{}) hav1alpha1.SceneEntity {
		entity := hav1alpha1.SceneEntity{
			EntityID: entityID,
			State:    state,
		}
		if attrs != nil {
			raw, _ := json.Marshal(attrs)
			entity.Attributes = runtime.RawExtension{Raw: raw}
		}
		return entity
	}

	reconcileScene := func(name, namespace string) (reconcile.Result, error) {
		return reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
		})
	}

	setupSceneToken := func(haName, namespace string) {
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
				// POST /api/config/scene/config/{id} — create/update
				postRequests <- r.URL.Path
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodDelete:
				// DELETE /api/config/scene/config/{id}
				delRequests <- r.URL.Path
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodGet && r.URL.Path == "/api/config":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"components":["scene"],"version":"2024.1.0"}`))
			case r.Method == http.MethodPost:
				// Service calls for hot-reload
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`[]`))
			default:
				http.NotFound(w, r)
			}
		}))

		reconciler = &HomeAssistantSceneReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: events.NewFakeRecorder(100),
			NewHAClient: func(_ string) *haclient.Client {
				return haclient.NewClient(mockServer.URL)
			},
		}
	})

	AfterEach(func() {
		// Cleanup scenes
		sceneList := &hav1alpha1.HomeAssistantSceneList{}
		_ = k8sClient.List(ctx, sceneList)
		for i := range sceneList.Items {
			ns := sceneList.Items[i].Namespace
			name := sceneList.Items[i].Name
			_ = k8sClient.Delete(ctx, &sceneList.Items[i])
			_, _ = reconcileScene(name, ns)
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

	Context("YAML Conversion (direct unit tests)", func() {
		It("should convert scene to YAML with all fields", func() {
			scene := &hav1alpha1.HomeAssistantScene{
				ObjectMeta: metav1.ObjectMeta{Name: "test-scene"},
				Spec: hav1alpha1.HomeAssistantSceneSpec{
					ID:   "custom_id",
					Name: "Test Scene",
					Icon: "mdi:test",
					Entities: []hav1alpha1.SceneEntity{
						createTestEntity("light.test", "on", map[string]interface{}{
							"brightness": 100,
							"color_temp": 400,
						}),
					},
				},
			}

			yamlMap, err := reconciler.sceneToYaml(scene)
			Expect(err).NotTo(HaveOccurred())
			Expect(yamlMap["id"]).To(Equal("custom_id"))
			Expect(yamlMap["name"]).To(Equal("Test Scene"))
			Expect(yamlMap["icon"]).To(Equal("mdi:test"))

			entities := yamlMap["entities"].(map[string]interface{})
			Expect(entities).To(HaveKey("light.test"))
			lightData := entities["light.test"].(map[string]interface{})
			Expect(lightData["state"]).To(Equal("on"))
			Expect(lightData["brightness"]).To(Equal(100.0))
			Expect(lightData["color_temp"]).To(Equal(400.0))
		})

		It("should omit optional fields when not set", func() {
			scene := &hav1alpha1.HomeAssistantScene{
				ObjectMeta: metav1.ObjectMeta{Name: "minimal-scene"},
				Spec: hav1alpha1.HomeAssistantSceneSpec{
					Entities: []hav1alpha1.SceneEntity{
						createTestEntity("light.test", "on", nil),
					},
				},
			}

			yamlMap, err := reconciler.sceneToYaml(scene)
			Expect(err).NotTo(HaveOccurred())
			Expect(yamlMap).To(HaveKey("id"))
			Expect(yamlMap).NotTo(HaveKey("name"))
			Expect(yamlMap).NotTo(HaveKey("icon"))
		})

		It("should auto-generate ID from CR name", func() {
			scene := &hav1alpha1.HomeAssistantScene{
				ObjectMeta: metav1.ObjectMeta{Name: "auto-id-scene"},
				Spec: hav1alpha1.HomeAssistantSceneSpec{
					Entities: []hav1alpha1.SceneEntity{
						createTestEntity("light.test", "on", nil),
					},
				},
			}

			yamlMap, err := reconciler.sceneToYaml(scene)
			Expect(err).NotTo(HaveOccurred())
			Expect(yamlMap["id"]).To(Equal("auto-id-scene"))
		})

		It("should handle entities with state only (no attributes)", func() {
			scene := &hav1alpha1.HomeAssistantScene{
				ObjectMeta: metav1.ObjectMeta{Name: "state-only-scene"},
				Spec: hav1alpha1.HomeAssistantSceneSpec{
					Entities: []hav1alpha1.SceneEntity{
						{EntityID: "light.simple", State: "off"},
					},
				},
			}

			yamlMap, err := reconciler.sceneToYaml(scene)
			Expect(err).NotTo(HaveOccurred())

			entities := yamlMap["entities"].(map[string]interface{})
			lightData := entities["light.simple"].(map[string]interface{})
			Expect(lightData["state"]).To(Equal("off"))
			Expect(lightData).To(HaveLen(1))
		})
	})

	Context("Hash Tracking (direct unit tests)", func() {
		It("should calculate stable hash (idempotent)", func() {
			scene := &hav1alpha1.HomeAssistantScene{
				ObjectMeta: metav1.ObjectMeta{Name: "hash-test"},
				Spec: hav1alpha1.HomeAssistantSceneSpec{
					Entities: []hav1alpha1.SceneEntity{
						createTestEntity("light.test", "on", map[string]interface{}{"brightness": 50}),
					},
				},
			}

			hash1, err := reconciler.calculateSceneHash(scene)
			Expect(err).NotTo(HaveOccurred())
			hash2, err := reconciler.calculateSceneHash(scene)
			Expect(err).NotTo(HaveOccurred())
			Expect(hash1).To(Equal(hash2))
		})

		It("should change hash when entities change", func() {
			scene := &hav1alpha1.HomeAssistantScene{
				ObjectMeta: metav1.ObjectMeta{Name: "hash-change-test"},
				Spec: hav1alpha1.HomeAssistantSceneSpec{
					Entities: []hav1alpha1.SceneEntity{createTestEntity("light.test", "on", nil)},
				},
			}

			hash1, err := reconciler.calculateSceneHash(scene)
			Expect(err).NotTo(HaveOccurred())

			scene.Spec.Entities = []hav1alpha1.SceneEntity{createTestEntity("light.test", "off", nil)}

			hash2, err := reconciler.calculateSceneHash(scene)
			Expect(err).NotTo(HaveOccurred())
			Expect(hash1).NotTo(Equal(hash2))
		})

		It("should change hash when entity attributes change", func() {
			scene := &hav1alpha1.HomeAssistantScene{
				ObjectMeta: metav1.ObjectMeta{Name: "hash-attr-test"},
				Spec: hav1alpha1.HomeAssistantSceneSpec{
					Entities: []hav1alpha1.SceneEntity{
						createTestEntity("light.test", "on", map[string]interface{}{"brightness": 30}),
					},
				},
			}

			hash1, err := reconciler.calculateSceneHash(scene)
			Expect(err).NotTo(HaveOccurred())

			scene.Spec.Entities = []hav1alpha1.SceneEntity{
				createTestEntity("light.test", "on", map[string]interface{}{"brightness": 50}),
			}

			hash2, err := reconciler.calculateSceneHash(scene)
			Expect(err).NotTo(HaveOccurred())
			Expect(hash1).NotTo(Equal(hash2))
		})
	})

	Context("Validation", func() {
		It("should set Ready=False when HomeAssistant not found", func() {
			ns := "default"
			scene := &hav1alpha1.HomeAssistantScene{
				ObjectMeta: metav1.ObjectMeta{Name: "scene-no-ha", Namespace: ns},
				Spec: hav1alpha1.HomeAssistantSceneSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: "non-existent"},
					Entities:         []hav1alpha1.SceneEntity{createTestEntity("light.test", "on", nil)},
				},
			}

			Expect(k8sClient.Create(ctx, scene)).To(Succeed())

			// First reconcile adds finalizer
			_, err := reconcileScene(scene.Name, ns)
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile validates HA ref
			result, err := reconcileScene(scene.Name, ns)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			Eventually(func() bool {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: scene.Name, Namespace: ns}, scene)
				for _, cond := range scene.Status.Conditions {
					if cond.Type == conditionTypeReady && cond.Status == metav1.ConditionFalse {
						return true
					}
				}
				return false
			}, sceneTimeout, sceneInterval).Should(BeTrue())
		})

		It("should requeue 30s when API token missing", func() {
			ns := "default"
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ha-scene-notoken", Namespace: ns},
				Spec:       hav1alpha1.HomeAssistantSpec{Version: "stable"},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			scene := &hav1alpha1.HomeAssistantScene{
				ObjectMeta: metav1.ObjectMeta{Name: "scene-no-token", Namespace: ns},
				Spec: hav1alpha1.HomeAssistantSceneSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: ha.Name},
					Entities:         []hav1alpha1.SceneEntity{createTestEntity("light.test", "on", nil)},
				},
			}
			Expect(k8sClient.Create(ctx, scene)).To(Succeed())

			// First reconcile adds finalizer
			_, err := reconcileScene(scene.Name, ns)
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile hits token check
			result, err := reconcileScene(scene.Name, ns)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))
		})
	})

	Context("Status Conditions (with mock HA server)", func() {
		const haStatusName = "test-ha-scene-status"
		const ns = "default"

		BeforeEach(func() {
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: haStatusName, Namespace: ns},
				Spec:       hav1alpha1.HomeAssistantSpec{Version: "2024.1.0"},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
			setupSceneToken(haStatusName, ns)
		})

		It("should set Ready=True after successful PUT", func() {
			scene := createTestScene("scene-ready", ns, haStatusName, "Movie Night",
				[]hav1alpha1.SceneEntity{createTestEntity("light.test", "on", nil)})
			Expect(k8sClient.Create(ctx, scene)).To(Succeed())

			_, err := reconcileScene(scene.Name, ns)
			Expect(err).NotTo(HaveOccurred())
			_, err = reconcileScene(scene.Name, ns)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: scene.Name, Namespace: ns}, scene)
				for _, cond := range scene.Status.Conditions {
					if cond.Type == conditionTypeReady && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, sceneTimeout, sceneInterval).Should(BeTrue())
		})

		It("should update ObservedGeneration", func() {
			scene := createTestScene("scene-obsgen", ns, haStatusName, "Gen Test",
				[]hav1alpha1.SceneEntity{createTestEntity("light.test", "on", nil)})
			Expect(k8sClient.Create(ctx, scene)).To(Succeed())

			_, err := reconcileScene(scene.Name, ns)
			Expect(err).NotTo(HaveOccurred())
			_, err = reconcileScene(scene.Name, ns)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: scene.Name, Namespace: ns}, scene)
				return scene.Status.ObservedGeneration == scene.Generation
			}, sceneTimeout, sceneInterval).Should(BeTrue())
		})
	})

	Context("SetupWithManager", func() {
		It("should setup controller successfully", func() {
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{
				Scheme: k8sClient.Scheme(),
			})
			Expect(err).NotTo(HaveOccurred())

			r := &HomeAssistantSceneReconciler{
				Client: mgr.GetClient(),
				Scheme: mgr.GetScheme(),
			}
			err = r.SetupWithManager(mgr)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
