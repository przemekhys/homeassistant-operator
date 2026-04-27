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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

var _ = Describe("HomeAssistantConfiguration", func() {
	const (
		namespace         = "default"
		timeout           = time.Second * 10
		interval          = time.Millisecond * 250
		updatedConfigYAML = "homeassistant:\n  name: Home Updated\n"
	)

	Context("API Structure and Validation", func() {
		AfterEach(func() {
			// Cleanup all HomeAssistantConfiguration resources
			configList := &hav1alpha1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}

			// Cleanup HomeAssistant resources
			haList := &hav1alpha1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}

			// Wait for cleanup
			Eventually(func() int {
				configList := &hav1alpha1.HomeAssistantConfigurationList{}
				_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
				return len(configList.Items)
			}, timeout, interval).Should(Equal(0))
		})

		It("should allow creating HomeAssistantConfiguration with minimal config", func() {
			By("Creating a HomeAssistant CR first")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-minimal",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating a HomeAssistantConfiguration with minimal fields")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-minimal",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-minimal",
					},
					Configuration: "homeassistant:\n  name: Home\n  latitude: 51.5074\n  longitude: -0.1278\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Retrieving the created resource")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-minimal",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())
			Expect(retrieved.Spec.HomeAssistantRef.Name).To(Equal("test-ha-minimal"))
			Expect(retrieved.Spec.Configuration).To(ContainSubstring("homeassistant:"))
		})

		It("should have default values for ReloadStrategy and AutoReload", func() {
			By("Creating a HomeAssistant CR first")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-defaults",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating a configuration without specifying reload strategy")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-defaults",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-defaults",
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Verifying defaults are applied")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-defaults",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())
			Expect(retrieved.Spec.ReloadStrategy).To(Equal(hav1alpha1.ConfigurationReloadStrategyAuto))
			Expect(*retrieved.Spec.AutoReload).To(BeTrue())
		})

		It("should support custom ReloadStrategy values", func() {
			By("Creating a HomeAssistant CR first")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-strategies",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			strategies := []struct {
				name     string
				strategy hav1alpha1.ConfigurationReloadStrategy
			}{
				{"auto", hav1alpha1.ConfigurationReloadStrategyAuto},
				{"hotreload", hav1alpha1.ConfigurationReloadStrategyHotReload},
				{"restart", hav1alpha1.ConfigurationReloadStrategyRestart},
			}

			for _, s := range strategies {
				config := &hav1alpha1.HomeAssistantConfiguration{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "config-" + s.name,
						Namespace: namespace,
					},
					Spec: hav1alpha1.HomeAssistantConfigurationSpec{
						HomeAssistantRef: hav1alpha1.HomeAssistantReference{
							Name: "test-ha-strategies",
						},
						Configuration:  "homeassistant:\n  name: Home\n",
						ReloadStrategy: s.strategy,
					},
				}
				Expect(k8sClient.Create(ctx, config)).To(Succeed())

				retrieved := &hav1alpha1.HomeAssistantConfiguration{}
				key := types.NamespacedName{
					Name:      "config-" + s.name,
					Namespace: namespace,
				}
				Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())
				Expect(retrieved.Spec.ReloadStrategy).To(Equal(s.strategy))
			}
		})
	})

	Context("Typed Configuration Components", func() {
		AfterEach(func() {
			// Cleanup
			configList := &hav1alpha1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}

			haList := &hav1alpha1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}
		})

		It("should support HTTP component configuration", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-http",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating configuration with HTTP settings")
			httpConfig := &hav1alpha1.HTTPConfig{
				CORSDomains: []string{"https://example.com", "https://mobile.example.com"},
				TrustProxy:  boolPtr(true),
			}

			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-http",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-http",
					},
					Configuration: "homeassistant:\n  name: Home\n",
					HTTP:          httpConfig,
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Verifying HTTP configuration")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-http",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())
			Expect(retrieved.Spec.HTTP).NotTo(BeNil())
			Expect(retrieved.Spec.HTTP.CORSDomains).To(HaveLen(2))
			Expect(*retrieved.Spec.HTTP.TrustProxy).To(BeTrue())
		})

		It("should support Logger component configuration", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-logger",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating configuration with logger settings")
			loggerConfig := &hav1alpha1.LoggerConfig{
				DefaultLevel: "DEBUG",
				Logs: map[string]string{
					"homeassistant.core":                "DEBUG",
					"homeassistant.components.mqtt":     "DEBUG",
					"homeassistant.components.recorder": "INFO",
				},
			}

			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-logger",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-logger",
					},
					Configuration: "homeassistant:\n  name: Home\n",
					Logger:        loggerConfig,
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Verifying logger configuration")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-logger",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())
			Expect(retrieved.Spec.Logger).NotTo(BeNil())
			Expect(retrieved.Spec.Logger.DefaultLevel).To(Equal("DEBUG"))
			Expect(retrieved.Spec.Logger.Logs).To(HaveLen(3))
			Expect(retrieved.Spec.Logger.Logs["homeassistant.core"]).To(Equal("DEBUG"))
		})

		It("should support Recorder component configuration", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-recorder",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating configuration with recorder settings")
			recorderConfig := &hav1alpha1.RecorderConfig{
				Enabled:       boolPtr(true),
				Database:      "postgresql://user:pass@postgres:5432/homeassistant",
				PurgeKeepDays: int32Ptr(30),
			}

			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-recorder",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-recorder",
					},
					Configuration: "homeassistant:\n  name: Home\n",
					Recorder:      recorderConfig,
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Verifying recorder configuration")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-recorder",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())
			Expect(retrieved.Spec.Recorder).NotTo(BeNil())
			Expect(*retrieved.Spec.Recorder.Enabled).To(BeTrue())
			Expect(retrieved.Spec.Recorder.Database).To(ContainSubstring("postgresql"))
			Expect(*retrieved.Spec.Recorder.PurgeKeepDays).To(Equal(int32(30)))
		})

		It("should support MQTT component configuration", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-mqtt",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating configuration with MQTT settings")
			mqttConfig := &hav1alpha1.MQTTConfig{
				Broker:   "mqtt://mosquitto:1883",
				Username: "homeassistant",
				PasswordRef: &hav1alpha1.SecretKeySelector{
					Name: "mqtt-credentials",
					Key:  "password",
				},
				ClientID:  "homeassistant",
				KeepAlive: int32Ptr(60),
			}

			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-mqtt",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-mqtt",
					},
					Configuration: "homeassistant:\n  name: Home\n",
					MQTT:          mqttConfig,
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Verifying MQTT configuration")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-mqtt",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())
			Expect(retrieved.Spec.MQTT).NotTo(BeNil())
			Expect(retrieved.Spec.MQTT.Broker).To(Equal("mqtt://mosquitto:1883"))
			Expect(retrieved.Spec.MQTT.Username).To(Equal("homeassistant"))
			Expect(retrieved.Spec.MQTT.PasswordRef.Name).To(Equal("mqtt-credentials"))
			Expect(*retrieved.Spec.MQTT.KeepAlive).To(Equal(int32(60)))
		})

		It("should support combined typed components", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-combined",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating configuration with multiple typed components")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-combined",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-combined",
					},
					Configuration: "homeassistant:\n  name: Home\n",
					HTTP: &hav1alpha1.HTTPConfig{
						TrustProxy: boolPtr(true),
					},
					Logger: &hav1alpha1.LoggerConfig{
						DefaultLevel: "INFO",
					},
					Recorder: &hav1alpha1.RecorderConfig{
						Enabled: boolPtr(true),
					},
					MQTT: &hav1alpha1.MQTTConfig{
						Broker: "mqtt://mosquitto:1883",
					},
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Verifying all components are present")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-combined",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())
			Expect(retrieved.Spec.HTTP).NotTo(BeNil())
			Expect(retrieved.Spec.Logger).NotTo(BeNil())
			Expect(retrieved.Spec.Recorder).NotTo(BeNil())
			Expect(retrieved.Spec.MQTT).NotTo(BeNil())
		})
	})

	Context("Status Tracking", func() {
		AfterEach(func() {
			// Cleanup
			configList := &hav1alpha1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}

			haList := &hav1alpha1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}
		})

		It("should initialize empty status on creation", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-status",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating a configuration")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-status",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-status",
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Verifying status structure")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-status",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())
			Expect(retrieved.Status).NotTo(BeNil())
			Expect(retrieved.Status.ConfigHash).To(Equal(""))
			Expect(retrieved.Status.LastReloadTime).To(BeNil())
		})

		It("should support status update with conditions", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-status-update",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating a configuration")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-status-update",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-status-update",
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Updating status")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-status-update",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())

			retrieved.Status.ConfigHash = "abc123def456"
			retrieved.Status.LastReloadMethod = "hot-reload"
			retrieved.Status.LastReloadTime = &metav1.Time{Time: time.Now()}

			Expect(k8sClient.Status().Update(ctx, retrieved)).To(Succeed())

			By("Verifying status was updated")
			updated := &hav1alpha1.HomeAssistantConfiguration{}
			Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
			Expect(updated.Status.ConfigHash).To(Equal("abc123def456"))
			Expect(updated.Status.LastReloadMethod).To(Equal("hot-reload"))
			Expect(updated.Status.LastReloadTime).NotTo(BeNil())
		})
	})

	Context("kubectl short names", func() {
		AfterEach(func() {
			// Cleanup
			configList := &hav1alpha1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}

			haList := &hav1alpha1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}
		})

		It("should allow listing with 'haconfig' short name", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-list",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating multiple configurations")
			for i := 1; i <= 2; i++ {
				config := &hav1alpha1.HomeAssistantConfiguration{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "config-list-" + string(rune(48+i)),
						Namespace: namespace,
					},
					Spec: hav1alpha1.HomeAssistantConfigurationSpec{
						HomeAssistantRef: hav1alpha1.HomeAssistantReference{
							Name: "test-ha-list",
						},
						Configuration: "homeassistant:\n  name: Home\n",
					},
				}
				Expect(k8sClient.Create(ctx, config)).To(Succeed())
			}

			By("Listing all configurations")
			configList := &hav1alpha1.HomeAssistantConfigurationList{}
			Expect(k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})).To(Succeed())
			Expect(configList.Items).To(HaveLen(2))
		})

		It("should retrieve resource by short name", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-retrieve",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating a configuration")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-retrieve",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-retrieve",
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Retrieving by full resource name")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-retrieve",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())
			Expect(retrieved.Name).To(Equal("config-retrieve"))
		})
	})

	Context("Print columns in list output", func() {
		AfterEach(func() {
			// Cleanup
			configList := &hav1alpha1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}

			haList := &hav1alpha1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}
		})

		It("should support print columns for HomeAssistantConfiguration", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-printcol",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating a configuration")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-printcol",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-printcol",
					},
					Configuration:  "homeassistant:\n  name: Home\n",
					ReloadStrategy: hav1alpha1.ConfigurationReloadStrategyHotReload,
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Verifying print column fields are accessible")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-printcol",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())
			Expect(retrieved.Spec.HomeAssistantRef.Name).To(Equal("test-ha-printcol"))
			Expect(retrieved.Spec.ReloadStrategy).To(Equal(hav1alpha1.ConfigurationReloadStrategyHotReload))
			Expect(retrieved.ObjectMeta.CreationTimestamp).NotTo(BeNil())
		})
	})

	Context("Configuration updates", func() {
		AfterEach(func() {
			// Cleanup
			configList := &hav1alpha1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}

			haList := &hav1alpha1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}
		})

		It("should allow updating configuration content", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-update",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating initial configuration")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-update",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-update",
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Updating configuration content")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-update",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())

			retrieved.Spec.Configuration = "homeassistant:\n  name: Updated Home\n  latitude: 51.5074\n"
			Expect(k8sClient.Update(ctx, retrieved)).To(Succeed())

			By("Verifying configuration was updated")
			updated := &hav1alpha1.HomeAssistantConfiguration{}
			Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
			Expect(updated.Spec.Configuration).To(ContainSubstring("Updated Home"))
		})

		It("should allow updating reload strategy", func() {
			By("Creating a HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-strategy-update",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating configuration with auto strategy")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-strategy-update",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-strategy-update",
					},
					Configuration:  "homeassistant:\n  name: Home\n",
					ReloadStrategy: hav1alpha1.ConfigurationReloadStrategyAuto,
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Updating to hot-reload strategy")
			retrieved := &hav1alpha1.HomeAssistantConfiguration{}
			key := types.NamespacedName{
				Name:      "config-strategy-update",
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, key, retrieved)).To(Succeed())

			retrieved.Spec.ReloadStrategy = hav1alpha1.ConfigurationReloadStrategyHotReload
			Expect(k8sClient.Update(ctx, retrieved)).To(Succeed())

			By("Verifying strategy was updated")
			updated := &hav1alpha1.HomeAssistantConfiguration{}
			Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
			Expect(updated.Spec.ReloadStrategy).To(Equal(hav1alpha1.ConfigurationReloadStrategyHotReload))
		})
	})

	Context("Controller - ConfigMap Generation", func() {
		const (
			haName                   = "test-ha-config"
			configResourceName       = "config-generation"
			namespace                = "default"
			timeout                  = time.Second * 10
			interval                 = time.Millisecond * 250
			generatedConfigmapSuffix = "-configuration"
		)

		BeforeEach(func() {
			// Create HomeAssistant resource for tests
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
					Service: &hav1alpha1.ServiceSpec{
						Port: 8123,
						Type: corev1.ServiceTypeClusterIP,
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup HomeAssistantConfiguration resources
			configList := &hav1alpha1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}

			// Cleanup ConfigMaps
			cmList := &corev1.ConfigMapList{}
			_ = k8sClient.List(ctx, cmList, &client.ListOptions{Namespace: namespace})
			for _, cm := range cmList.Items {
				_ = k8sClient.Delete(ctx, &cm)
			}

			// Cleanup HomeAssistant
			haList := &hav1alpha1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}

			// Wait for cleanup
			Eventually(func() int {
				configList := &hav1alpha1.HomeAssistantConfigurationList{}
				_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
				return len(configList.Items)
			}, timeout, interval).Should(Equal(0))
		})

		It("should create ConfigMap when HomeAssistantConfiguration is created", func() {
			By("Creating a HomeAssistantConfiguration")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configResourceName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					Configuration: "homeassistant:\n  name: Home\n  latitude: 51.5074\n  longitude: -0.1278\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying generated ConfigMap was created")
			Eventually(func(g Gomega) {
				generatedCM := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + generatedConfigmapSuffix,
					Namespace: namespace,
				}, generatedCM)).To(Succeed())
				g.Expect(generatedCM.Data).To(HaveKey("configuration.yaml"))
				g.Expect(generatedCM.Data["configuration.yaml"]).To(ContainSubstring("homeassistant:"))
			}, timeout, interval).Should(Succeed())
		})

		It("should update ConfigMap when configuration changes", func() {
			By("Creating a HomeAssistantConfiguration")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configResourceName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Getting initial ConfigMap hash")
			var initialHash string
			Eventually(func(g Gomega) {
				generatedCM := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + generatedConfigmapSuffix,
					Namespace: namespace,
				}, generatedCM)).To(Succeed())
				initialHash = generatedCM.Annotations["ha.homeassistant.io/config-hash"]
			}, timeout, interval).Should(Succeed())

			By("Updating configuration")
			Eventually(func() error {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig); err != nil {
					return err
				}
				retrievedConfig.Spec.Configuration = "homeassistant:\n  name: Home Updated\n  timezone: Europe/Warsaw\n"
				return k8sClient.Update(ctx, retrievedConfig)
			}, timeout, interval).Should(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ConfigMap was updated with new hash")
			Eventually(func(g Gomega) {
				generatedCM := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + generatedConfigmapSuffix,
					Namespace: namespace,
				}, generatedCM)).To(Succeed())
				newHash := generatedCM.Annotations["ha.homeassistant.io/config-hash"]
				g.Expect(newHash).NotTo(Equal(initialHash))
				g.Expect(generatedCM.Data["configuration.yaml"]).To(ContainSubstring("Home Updated"))
			}, timeout, interval).Should(Succeed())
		})

		It("should set status conditions on successful reconciliation", func() {
			By("Creating a HomeAssistantConfiguration")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configResourceName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying status condition is set to Ready")
			Eventually(func(g Gomega) {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig)).To(Succeed())
				g.Expect(retrievedConfig.Status.Conditions).NotTo(BeEmpty())
				// Find Ready condition
				var readyCondition *metav1.Condition
				for i := range retrievedConfig.Status.Conditions {
					if retrievedConfig.Status.Conditions[i].Type == "Ready" {
						readyCondition = &retrievedConfig.Status.Conditions[i]
						break
					}
				}
				g.Expect(readyCondition).NotTo(BeNil())
				g.Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(readyCondition.Reason).To(Equal("ConfigurationGenerated"))
			}, timeout, interval).Should(Succeed())
		})

		It("should handle missing HomeAssistant reference gracefully", func() {
			By("Creating a HomeAssistantConfiguration with non-existent HA reference")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configResourceName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "non-existent-ha",
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			// Should requeue after delay, not error
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			By("Verifying status condition indicates error")
			Eventually(func(g Gomega) {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig)).To(Succeed())
				// Find Ready condition
				var readyCondition *metav1.Condition
				for i := range retrievedConfig.Status.Conditions {
					if retrievedConfig.Status.Conditions[i].Type == "Ready" {
						readyCondition = &retrievedConfig.Status.Conditions[i]
						break
					}
				}
				g.Expect(readyCondition).NotTo(BeNil())
				g.Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCondition.Reason).To(Equal("InvalidConfiguration"))
			}, timeout, interval).Should(Succeed())
		})

		It("should update ConfigMap hash when configuration changes", func() {
			By("Creating initial configuration")
			originalConfig := "homeassistant:\n  name: Home\n"
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configResourceName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					Configuration: originalConfig,
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Getting status with original hash")
			var originalHash string
			Eventually(func(g Gomega) {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig)).To(Succeed())
				originalHash = retrievedConfig.Status.ConfigHash
				g.Expect(originalHash).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			By("Changing configuration")
			newConfig := "homeassistant:\n  name: Home\n  timezone: Europe/Warsaw\n"
			Eventually(func() error {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig); err != nil {
					return err
				}
				retrievedConfig.Spec.Configuration = newConfig
				return k8sClient.Update(ctx, retrievedConfig)
			}, timeout, interval).Should(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying hash changed in status")
			Eventually(func(g Gomega) {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig)).To(Succeed())
				newHash := retrievedConfig.Status.ConfigHash
				g.Expect(newHash).NotTo(Equal(originalHash))
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("Controller - Configuration Reload", func() {
		const (
			haName                   = "test-ha-reload"
			configResourceName       = "config-reload"
			namespace                = "default"
			timeout                  = time.Second * 10
			interval                 = time.Millisecond * 250
			generatedConfigmapSuffix = "-configuration"
		)

		BeforeEach(func() {
			// Create HomeAssistant resource for tests
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
					Service: &hav1alpha1.ServiceSpec{
						Port: 8123,
						Type: corev1.ServiceTypeClusterIP,
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			// Create bootstrap token Secret
			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName + "-homeassistant-api-token",
					Namespace: namespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"token": []byte("test-token-123"),
				},
			}
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())

			// Create StatefulSet for pod restart testing
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: appsv1.StatefulSetSpec{
					ServiceName: haName + "-homeassistant",
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "homeassistant"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "homeassistant"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "homeassistant",
									Image: "ghcr.io/home-assistant/home-assistant:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup all resources
			configList := &hav1alpha1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}

			secretList := &corev1.SecretList{}
			_ = k8sClient.List(ctx, secretList, &client.ListOptions{Namespace: namespace})
			for _, secret := range secretList.Items {
				_ = k8sClient.Delete(ctx, &secret)
			}

			stsList := &appsv1.StatefulSetList{}
			_ = k8sClient.List(ctx, stsList, &client.ListOptions{Namespace: namespace})
			for _, sts := range stsList.Items {
				_ = k8sClient.Delete(ctx, &sts)
			}

			cmList := &corev1.ConfigMapList{}
			_ = k8sClient.List(ctx, cmList, &client.ListOptions{Namespace: namespace})
			for _, cm := range cmList.Items {
				_ = k8sClient.Delete(ctx, &cm)
			}

			haList := &hav1alpha1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}

			Eventually(func() int {
				configList := &hav1alpha1.HomeAssistantConfigurationList{}
				_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
				return len(configList.Items)
			}, timeout, interval).Should(Equal(0))
		})

		It("should NOT update ConfigMap annotation when AutoReload is disabled", func() {
			By("Creating initial HomeAssistantConfiguration")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configResourceName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
					// AutoReload false - we only test ConfigMap annotation behavior, not actual hot-reload API calls
					AutoReload: boolPtr(false),
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling to create initial ConfigMap")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify ConfigMap was created WITHOUT hash annotation (new behavior)
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + "-configuration",
					Namespace: namespace,
				}, cm)).To(Succeed())
				// Hash annotation should NOT be set initially
				if cm.Annotations != nil {
					g.Expect(cm.Annotations).NotTo(HaveKey("ha.homeassistant.io/config-hash"))
				}
			}, timeout, interval).Should(Succeed())

			By("Updating configuration with reloadable change (automation)")
			Eventually(func() error {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig); err != nil {
					return err
				}
				retrievedConfig.Spec.Configuration = "homeassistant:\n  name: Home\nautomation: []"
				return k8sClient.Update(ctx, retrievedConfig)
			}, timeout, interval).Should(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ConfigMap content was updated but annotation hash is STILL NOT set (hot-reload path)")
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + "-configuration",
					Namespace: namespace,
				}, cm)).To(Succeed())
				// Content should be updated
				g.Expect(cm.Data["configuration.yaml"]).To(ContainSubstring("automation: []"))
				// Hash annotation should STILL NOT be set (hot-reload doesn't trigger restart)
				if cm.Annotations != nil {
					g.Expect(cm.Annotations).NotTo(
						HaveKey("ha.homeassistant.io/config-hash"),
						"Hash annotation should NOT be set for hot-reload")
				}
			}, timeout, interval).Should(Succeed())

			By("Note: Hash annotation is ONLY updated during restart strategy in performConfigReload()")
		})

		It("should NOT update ConfigMap annotation when autoReload disabled", func() {
			By("Creating HomeAssistantConfiguration with autoReload=false")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configResourceName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
					AutoReload:    boolPtr(false),
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling to create initial ConfigMap")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify ConfigMap was created WITHOUT hash annotation
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + "-configuration",
					Namespace: namespace,
				}, cm)).To(Succeed())
				// Hash annotation should NOT be set initially
				if cm.Annotations != nil {
					g.Expect(cm.Annotations).NotTo(HaveKey("ha.homeassistant.io/config-hash"))
				}
			}, timeout, interval).Should(Succeed())

			By("Updating configuration")
			Eventually(func() error {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig); err != nil {
					return err
				}
				retrievedConfig.Spec.Configuration = updatedConfigYAML
				return k8sClient.Update(ctx, retrievedConfig)
			}, timeout, interval).Should(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ConfigMap content updated but hash annotation STILL NOT set (autoReload disabled)")
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + "-configuration",
					Namespace: namespace,
				}, cm)).To(Succeed())
				// Content should be updated
				g.Expect(cm.Data["configuration.yaml"]).To(ContainSubstring("Home Updated"))
				// Hash annotation should STILL NOT be set (no reload when autoReload disabled)
				if cm.Annotations != nil {
					g.Expect(cm.Annotations).NotTo(
						HaveKey("ha.homeassistant.io/config-hash"),
						"Hash annotation should NOT be set when autoReload disabled")
				}
			}, timeout, interval).Should(Succeed())

			By("Note: autoReload=false skips reload entirely, so hash annotation is never updated")
		})

		It("should update status.lastReloadMethod after reload", func() {
			By("Creating HomeAssistantConfiguration with strategy=restart")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configResourceName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					Configuration:  "homeassistant:\n  name: Home\n",
					ReloadStrategy: hav1alpha1.ConfigurationReloadStrategyRestart,
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling to create initial ConfigMap")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Updating configuration")
			Eventually(func() error {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig); err != nil {
					return err
				}
				retrievedConfig.Spec.Configuration = updatedConfigYAML
				return k8sClient.Update(ctx, retrievedConfig)
			}, timeout, interval).Should(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying status fields populated")
			Eventually(func(g Gomega) {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig)).To(Succeed())
				g.Expect(retrievedConfig.Status.LastReloadTime).NotTo(BeNil())
				g.Expect(retrievedConfig.Status.LastReloadMethod).To(Equal("restart"))
			}, timeout, interval).Should(Succeed())
		})

		It("should respect reloadStrategy=restart and update ConfigMap hash", func() {
			By("Creating HomeAssistantConfiguration with strategy=restart")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configResourceName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					Configuration:  "homeassistant:\n  name: Home\n",
					ReloadStrategy: hav1alpha1.ConfigurationReloadStrategyRestart,
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Capturing initial ConfigMap hash")
			var initialHash string
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + "-configuration",
					Namespace: namespace,
				}, cm)).To(Succeed())
				g.Expect(cm.Annotations).To(HaveKey("ha.homeassistant.io/config-hash"))
				initialHash = cm.Annotations["ha.homeassistant.io/config-hash"]
			}, timeout, interval).Should(Succeed())

			By("Updating to trigger reload")
			Eventually(func() error {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig); err != nil {
					return err
				}
				retrievedConfig.Spec.Configuration = updatedConfigYAML
				return k8sClient.Update(ctx, retrievedConfig)
			}, timeout, interval).Should(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ConfigMap hash updated (Faza 1: HAConfig Controller updates ConfigMap)")
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + "-configuration",
					Namespace: namespace,
				}, cm)).To(Succeed())
				g.Expect(cm.Annotations).To(HaveKey("ha.homeassistant.io/config-hash"))
				newHash := cm.Annotations["ha.homeassistant.io/config-hash"]
				g.Expect(newHash).NotTo(Equal(initialHash), "Hash should change after config update")
			}, timeout, interval).Should(Succeed())

			By("Verifying status shows restart method")
			Eventually(func(g Gomega) {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig)).To(Succeed())
				g.Expect(retrievedConfig.Status.LastReloadMethod).To(Equal("restart"))
			}, timeout, interval).Should(Succeed())

			By("Note: StatefulSet annotation will be synced by HomeAssistant Controller in Faza 2")
		})
	})

	Context("Controller - ConfigMap Operator Exclusivity", func() {
		const (
			haName     = "test-ha-exclusivity"
			configName = "test-config-exclusivity"
		)

		BeforeEach(func() {
			By("Creating HomeAssistant CR")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					Configuration: "homeassistant:\n  name: Original\n",
					AutoReload:    boolPtr(false), // Disable reload to avoid side effects
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Triggering reconciliation to create ConfigMap")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ConfigMap created")
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + "-configuration",
					Namespace: namespace,
				}, cm)).To(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		AfterEach(func() {
			By("Cleaning up test resources")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
			}
			_ = k8sClient.Delete(ctx, config)

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
			}
			_ = k8sClient.Delete(ctx, ha)

			// Wait for cleanup
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, &hav1alpha1.HomeAssistantConfiguration{})
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})

		It("should restore ConfigMap when content is modified externally", func() {
			configMapName := haName + "-configuration"

			By("Verifying original ConfigMap state (no hash annotation initially)")
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm)).To(Succeed())
				// Hash annotation should NOT be set initially
				if cm.Annotations != nil {
					g.Expect(cm.Annotations).NotTo(HaveKey("ha.homeassistant.io/config-hash"))
				}
				g.Expect(cm.Data["configuration.yaml"]).To(ContainSubstring("Original"))
			}, timeout, interval).Should(Succeed())

			By("Modifying ConfigMap content externally (simulating manual edit)")
			Eventually(func() error {
				cm := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm); err != nil {
					return err
				}
				cm.Data["configuration.yaml"] = "homeassistant:\n  name: ManuallyModified\n"
				return k8sClient.Update(ctx, cm)
			}, timeout, interval).Should(Succeed())

			By("Verifying ConfigMap was actually modified")
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm)).To(Succeed())
				g.Expect(cm.Data["configuration.yaml"]).To(ContainSubstring("ManuallyModified"))
			}, timeout, interval).Should(Succeed())

			By("Triggering reconciliation (watch would do this automatically)")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ConfigMap restored to CRD state")
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm)).To(Succeed())
				g.Expect(cm.Data["configuration.yaml"]).To(ContainSubstring("Original"))
				g.Expect(cm.Data["configuration.yaml"]).NotTo(ContainSubstring("ManuallyModified"))
				// Hash annotation should STILL NOT be set (only content is restored)
				if cm.Annotations != nil {
					g.Expect(cm.Annotations).NotTo(HaveKey("ha.homeassistant.io/config-hash"))
				}
			}, timeout, interval).Should(Succeed())
		})

		It("should NOT restore hash annotation when modified externally (only content is managed)", func() {
			configMapName := haName + "-configuration"

			By("Verifying original ConfigMap state (no hash annotation)")
			var originalContent string
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm)).To(Succeed())
				originalContent = cm.Data["configuration.yaml"]
				g.Expect(originalContent).NotTo(BeEmpty())
				// Hash annotation should NOT be set initially
				if cm.Annotations != nil {
					g.Expect(cm.Annotations).NotTo(HaveKey("ha.homeassistant.io/config-hash"))
				}
			}, timeout, interval).Should(Succeed())

			By("Adding ConfigMap hash annotation externally (simulating manual edit)")
			Eventually(func() error {
				cm := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm); err != nil {
					return err
				}
				if cm.Annotations == nil {
					cm.Annotations = make(map[string]string)
				}
				cm.Annotations["ha.homeassistant.io/config-hash"] = "tampered-hash-12345"
				return k8sClient.Update(ctx, cm)
			}, timeout, interval).Should(Succeed())

			By("Verifying annotation was actually modified")
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm)).To(Succeed())
				g.Expect(cm.Annotations["ha.homeassistant.io/config-hash"]).To(Equal("tampered-hash-12345"))
			}, timeout, interval).Should(Succeed())

			By("Triggering reconciliation")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying externally added hash annotation is NOT removed (only content is managed by syncConfigMapFromCRD)")
			Consistently(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm)).To(Succeed())
				// Hash annotation should remain as tampered (not removed/restored)
				g.Expect(cm.Annotations).NotTo(BeNil())
				g.Expect(cm.Annotations["ha.homeassistant.io/config-hash"]).To(Equal("tampered-hash-12345"))
				// But content should match CRD (if it was modified, it would be restored)
				g.Expect(cm.Data["configuration.yaml"]).To(Equal(originalContent))
			}, time.Second*2, interval).Should(Succeed())
		})

		It("should NOT modify ConfigMap owned by different resource", func() {
			By("Creating a ConfigMap owned by something else")
			unrelatedCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unrelated-configmap",
					Namespace: namespace,
					Annotations: map[string]string{
						"ha.homeassistant.io/config-hash": "some-hash",
					},
				},
				Data: map[string]string{
					"configuration.yaml": "homeassistant:\n  name: Unrelated\n",
				},
			}
			Expect(k8sClient.Create(ctx, unrelatedCM)).To(Succeed())

			By("Verifying ConfigMap exists")
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      "unrelated-configmap",
					Namespace: namespace,
				}, cm)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			By("Triggering reconciliation for HomeAssistantConfiguration")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying unrelated ConfigMap unchanged")
			Consistently(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      "unrelated-configmap",
					Namespace: namespace,
				}, cm)).To(Succeed())
				g.Expect(cm.Data["configuration.yaml"]).To(ContainSubstring("Unrelated"))
			}, time.Second*2, interval).Should(Succeed())

			By("Cleaning up unrelated ConfigMap")
			_ = k8sClient.Delete(ctx, unrelatedCM)
		})

		It("should allow CRD updates to change ConfigMap content", func() {
			configMapName := haName + "-configuration"

			By("Capturing original ConfigMap state")
			var originalHash string
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm)).To(Succeed())
				originalHash = cm.Annotations["ha.homeassistant.io/config-hash"]
				g.Expect(cm.Data["configuration.yaml"]).To(ContainSubstring("Original"))
			}, timeout, interval).Should(Succeed())

			By("Updating HomeAssistantConfiguration CRD (legitimate change)")
			Eventually(func() error {
				config := &hav1alpha1.HomeAssistantConfiguration{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, config); err != nil {
					return err
				}
				config.Spec.Configuration = "homeassistant:\n  name: UpdatedViaCRD\n"
				return k8sClient.Update(ctx, config)
			}, timeout, interval).Should(Succeed())

			By("Triggering reconciliation")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ConfigMap content updated but hash annotation unchanged (AutoReload=false)")
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm)).To(Succeed())
				// Content should be updated from CRD
				g.Expect(cm.Data["configuration.yaml"]).To(ContainSubstring("UpdatedViaCRD"))
				g.Expect(cm.Data["configuration.yaml"]).NotTo(ContainSubstring("Original"))
				// Hash annotation should remain unchanged (AutoReload=false means no reload, no hash update)
				newHash := cm.Annotations["ha.homeassistant.io/config-hash"]
				g.Expect(newHash).To(Equal(originalHash), "Hash annotation should NOT change when AutoReload=false")
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("Reconciliation Idempotency", func() {
		const (
			haName                   = "test-ha-idempotent"
			configResourceName       = "config-idempotent"
			namespace                = "default"
			timeout                  = time.Second * 10
			interval                 = time.Millisecond * 250
			generatedConfigmapSuffix = "-configuration"
		)

		BeforeEach(func() {
			// Create HomeAssistant resource
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "stable",
					Service: &hav1alpha1.ServiceSpec{
						Port: 8123,
						Type: corev1.ServiceTypeClusterIP,
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			// Create StatefulSet for Pod testing
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: appsv1.StatefulSetSpec{
					ServiceName: haName + "-homeassistant",
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "homeassistant"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "homeassistant"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "homeassistant",
									Image: "ghcr.io/home-assistant/home-assistant:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup
			configList := &hav1alpha1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}

			stsList := &appsv1.StatefulSetList{}
			_ = k8sClient.List(ctx, stsList, &client.ListOptions{Namespace: namespace})
			for _, sts := range stsList.Items {
				_ = k8sClient.Delete(ctx, &sts)
			}

			cmList := &corev1.ConfigMapList{}
			_ = k8sClient.List(ctx, cmList, &client.ListOptions{Namespace: namespace})
			for _, cm := range cmList.Items {
				_ = k8sClient.Delete(ctx, &cm)
			}

			haList := &hav1alpha1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}

			Eventually(func() int {
				configList := &hav1alpha1.HomeAssistantConfigurationList{}
				_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
				return len(configList.Items)
			}, timeout, interval).Should(Equal(0))
		})

		It("should not modify resources when reconciling unchanged configuration", func() {
			By("Creating a HomeAssistantConfiguration")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configResourceName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					Configuration: "homeassistant:\n  name: Home\n  latitude: 51.5074\n  longitude: -0.1278\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("First reconciliation to create ConfigMap")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Capturing ConfigMap state after first reconciliation")
			var firstConfigMapContent string
			var firstConfigMapHash string
			var firstConfigMapResourceVersion string
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + generatedConfigmapSuffix,
					Namespace: namespace,
				}, cm)).To(Succeed())
				firstConfigMapContent = cm.Data["configuration.yaml"]
				firstConfigMapHash = cm.Annotations["ha.homeassistant.io/config-hash"]
				firstConfigMapResourceVersion = cm.ResourceVersion
				g.Expect(firstConfigMapContent).NotTo(BeEmpty())
				g.Expect(firstConfigMapHash).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			By("Capturing HomeAssistantConfiguration status after first reconciliation")
			var firstConfigStatus hav1alpha1.HomeAssistantConfigurationStatus
			Eventually(func(g Gomega) {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig)).To(Succeed())
				firstConfigStatus = retrievedConfig.Status
				g.Expect(retrievedConfig.Status.ConfigHash).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			By("Capturing StatefulSet state after first reconciliation")
			var firstStsResourceVersion string
			var firstStsAnnotations map[string]string
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				firstStsResourceVersion = sts.ResourceVersion
				firstStsAnnotations = sts.Spec.Template.Annotations
			}, timeout, interval).Should(Succeed())

			By("Second reconciliation (no changes)")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ConfigMap unchanged")
			Consistently(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + generatedConfigmapSuffix,
					Namespace: namespace,
				}, cm)).To(Succeed())
				g.Expect(cm.Data["configuration.yaml"]).To(Equal(firstConfigMapContent))
				g.Expect(cm.Annotations["ha.homeassistant.io/config-hash"]).To(Equal(firstConfigMapHash))
				g.Expect(cm.ResourceVersion).To(Equal(firstConfigMapResourceVersion))
			}, time.Second*2, interval).Should(Succeed())

			By("Verifying HomeAssistantConfiguration status unchanged")
			Consistently(func(g Gomega) {
				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig)).To(Succeed())
				g.Expect(retrievedConfig.Status.ConfigHash).To(Equal(firstConfigStatus.ConfigHash))
				g.Expect(retrievedConfig.Status.LastReloadTime).To(Equal(firstConfigStatus.LastReloadTime))
				g.Expect(retrievedConfig.Status.LastReloadMethod).To(Equal(firstConfigStatus.LastReloadMethod))
			}, time.Second*2, interval).Should(Succeed())

			By("Verifying StatefulSet unchanged")
			Consistently(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.ResourceVersion).To(Equal(firstStsResourceVersion))
				g.Expect(sts.Spec.Template.Annotations).To(Equal(firstStsAnnotations))
			}, time.Second*2, interval).Should(Succeed())

			By("Third reconciliation (multiple calls - still no changes)")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying all resources remain truly unchanged")
			Consistently(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + generatedConfigmapSuffix,
					Namespace: namespace,
				}, cm)).To(Succeed())
				g.Expect(cm.ResourceVersion).To(Equal(firstConfigMapResourceVersion))

				retrievedConfig := &hav1alpha1.HomeAssistantConfiguration{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				}, retrievedConfig)).To(Succeed())
				g.Expect(retrievedConfig.Status.ConfigHash).To(Equal(firstConfigStatus.ConfigHash))

				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.ResourceVersion).To(Equal(firstStsResourceVersion))
			}, time.Second*2, interval).Should(Succeed())
		})

		It("should maintain idempotency with auto-reload enabled", func() {
			By("Creating HomeAssistantConfiguration with autoReload=true")
			config := &hav1alpha1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configResourceName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					Configuration: "automation: []\n",
					AutoReload:    boolPtr(true),
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("First reconciliation")
			reconciler := &HomeAssistantConfigurationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      configResourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Capturing initial state")
			var initialHash string
			var initialStsAnnotation string
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + generatedConfigmapSuffix,
					Namespace: namespace,
				}, cm)).To(Succeed())
				initialHash = cm.Annotations["ha.homeassistant.io/config-hash"]

				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				if sts.Spec.Template.Annotations != nil {
					initialStsAnnotation = sts.Spec.Template.Annotations["ha.homeassistant.io/config-hash"]
				}
			}, timeout, interval).Should(Succeed())

			By("Running 5 consecutive reconciliations without changes")
			for i := 0; i < 5; i++ {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      configResourceName,
						Namespace: namespace,
					},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			By("Verifying state remains unchanged after multiple reconciliations")
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName + generatedConfigmapSuffix,
					Namespace: namespace,
				}, cm)).To(Succeed())
				g.Expect(cm.Annotations["ha.homeassistant.io/config-hash"]).To(Equal(initialHash))

				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				if initialStsAnnotation != "" {
					g.Expect(sts.Spec.Template.Annotations["ha.homeassistant.io/config-hash"]).To(Equal(initialStsAnnotation))
				}
			}, timeout, interval).Should(Succeed())
		})
	})
})

var _ = Describe("needsRestart unit tests", func() {
	Context("Adding a new top-level section", func() {
		It("should require restart when adding prometheus: (regression: retry scenario bug)", func() {
			oldConfig := `homeassistant:
  name: Test
default_config:
http:
  use_x_forwarded_for: true
  trusted_proxies:
    - 10.42.0.0/16`
			newConfig := oldConfig + "\nprometheus:\n"

			result, err := needsRestart(oldConfig, newConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeTrue(), "adding prometheus: should require restart")
		})

		It("should require restart when adding any unknown integration", func() {
			oldConfig := "homeassistant:\n  name: Test\n"
			newConfig := oldConfig + "mqtt:\n  broker: localhost\n"

			result, err := needsRestart(oldConfig, newConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeTrue(), "adding unknown integration should require restart")
		})

		It("should NOT require restart when adding automation: (hot-reloadable)", func() {
			oldConfig := "homeassistant:\n  name: Test\n"
			newConfig := oldConfig + "automation: !include automations.yaml\n"

			result, err := needsRestart(oldConfig, newConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeFalse(), "adding automation: should be hot-reloadable")
		})

		It("should return false when oldConfig equals newConfig (same content)", func() {
			config := `homeassistant:
  name: Test
prometheus:
`
			result, err := needsRestart(config, config)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeFalse(), "identical configs should not require restart")
		})
	})

	Context("Removing a section", func() {
		It("should require restart when removing a section", func() {
			oldConfig := "homeassistant:\n  name: Test\nprometheus:\n"
			newConfig := "homeassistant:\n  name: Test\n"

			result, err := needsRestart(oldConfig, newConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeTrue(), "removing a section should require restart")
		})
	})

	Context("Changing critical homeassistant: keys", func() {
		It("should require restart when changing time_zone", func() {
			oldConfig := "homeassistant:\n  name: Test\n  time_zone: Europe/Warsaw\n"
			newConfig := "homeassistant:\n  name: Test\n  time_zone: Europe/London\n"

			result, err := needsRestart(oldConfig, newConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeTrue(), "changing time_zone should require restart")
		})

		It("should NOT require restart when changing customize (hot-reloadable key)", func() {
			oldConfig := "homeassistant:\n  name: Test\n  customize:\n    light.test:\n      friendly_name: Test\n"
			newConfig := "homeassistant:\n  name: Test\n  customize:\n    light.test:\n      friendly_name: Updated\n"

			result, err := needsRestart(oldConfig, newConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeFalse(), "changing customize should be hot-reloadable")
		})
	})

	Context("Changing http: keys", func() {
		It("should NOT require restart when changing trusted_proxies (hot-reloadable)", func() {
			oldConfig := "http:\n  trusted_proxies:\n    - 10.0.0.0/8\n"
			newConfig := "http:\n  trusted_proxies:\n    - 10.0.0.0/8\n    - 192.168.0.0/16\n"

			result, err := needsRestart(oldConfig, newConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeFalse(), "changing trusted_proxies should be hot-reloadable")
		})

		It("should require restart when changing ssl_certificate (critical key)", func() {
			oldConfig := "http:\n  ssl_certificate: /ssl/cert.pem\n"
			newConfig := "http:\n  ssl_certificate: /ssl/new-cert.pem\n"

			result, err := needsRestart(oldConfig, newConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeTrue(), "changing ssl_certificate should require restart")
		})
	})

	Context("Empty configs", func() {
		It("should require restart when old config is empty and new config has content", func() {
			result, err := needsRestart("", "homeassistant:\n  name: Test\n")
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeTrue(), "adding content to empty config should require restart")
		})
	})

	Context("Malformed YAML", func() {
		It("should require restart and return error on malformed oldConfig", func() {
			oldConfig := "homeassistant:\n  name: Test\n  bad: [unclosed"
			newConfig := "homeassistant:\n  name: Test\n"

			result, err := needsRestart(oldConfig, newConfig)
			Expect(err).To(HaveOccurred())
			Expect(result).To(BeTrue(), "malformed YAML should default to restart (safe fallback)")
		})

		It("should require restart and return error on malformed newConfig", func() {
			oldConfig := "homeassistant:\n  name: Test\n"
			newConfig := "homeassistant:\n  name: Test\n  bad: [unclosed"

			result, err := needsRestart(oldConfig, newConfig)
			Expect(err).To(HaveOccurred())
			Expect(result).To(BeTrue(), "malformed YAML should default to restart (safe fallback)")
		})
	})
})

// Helper functions
func boolPtr(b bool) *bool {
	return &b
}

func int32Ptr(i int32) *int32 {
	return &i
}
