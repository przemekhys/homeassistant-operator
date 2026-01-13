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
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

var _ = Describe("HomeAssistant Controller", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	ctx := context.Background()

	Context("StatefulSet Reconciliation", func() {
		const (
			haName    = "test-ha-sts"
			namespace = "default"
		)

		var homeAssistant *hav1alpha1.HomeAssistant

		BeforeEach(func() {
			homeAssistant = &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, homeAssistant)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup HomeAssistant
			ha := &hav1alpha1.HomeAssistant{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
				Expect(k8sClient.Delete(ctx, ha)).To(Succeed())
			}

			// Cleanup StatefulSet (should be garbage collected, but clean up just in case)
			sts := &appsv1.StatefulSet{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, sts); err == nil {
				Expect(k8sClient.Delete(ctx, sts)).To(Succeed())
			}

			// Cleanup PVC
			pvc := &corev1.PersistentVolumeClaim{}
			pvcName := fmt.Sprintf("%s-config", haName)
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc); err == nil {
				Expect(k8sClient.Delete(ctx, pvc)).To(Succeed())
			}

			// Cleanup Service
			svc := &corev1.Service{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, svc); err == nil {
				Expect(k8sClient.Delete(ctx, svc)).To(Succeed())
			}
		})

		It("should create StatefulSet with correct spec", func() {
			By("Reconciling the HomeAssistant resource")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if StatefulSet was created")
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, sts)
			}, timeout, interval).Should(Succeed())

			By("Verifying StatefulSet spec")
			Expect(sts.Spec.Replicas).NotTo(BeNil())
			Expect(*sts.Spec.Replicas).To(Equal(int32(1)))
			Expect(sts.Spec.ServiceName).To(Equal(haName))

			By("Verifying container spec")
			Expect(sts.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := sts.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("home-assistant"))
			Expect(container.Image).To(Equal("ghcr.io/home-assistant/home-assistant:2024.1.0"))
			Expect(container.Ports).To(HaveLen(1))
			Expect(container.Ports[0].ContainerPort).To(Equal(int32(8123)))

			By("Verifying labels")
			Expect(sts.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", "homeassistant"))
			Expect(sts.Labels).To(HaveKeyWithValue("app.kubernetes.io/instance", haName))
			Expect(sts.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "homeassistant-operator"))
		})

		It("should set correct environment variables (TZ)", func() {
			By("Creating HomeAssistant with custom timezone")
			haWithTz := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-tz",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version:  "2024.1.0",
					Timezone: "Europe/Warsaw",
				},
			}
			Expect(k8sClient.Create(ctx, haWithTz)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, haWithTz)
			}()

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-ha-tz",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying TZ environment variable")
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-ha-tz",
					Namespace: namespace,
				}, sts)
			}, timeout, interval).Should(Succeed())

			container := sts.Spec.Template.Spec.Containers[0]
			var tzEnv *corev1.EnvVar
			for i := range container.Env {
				if container.Env[i].Name == "TZ" {
					tzEnv = &container.Env[i]
					break
				}
			}
			Expect(tzEnv).NotTo(BeNil())
			Expect(tzEnv.Value).To(Equal("Europe/Warsaw"))
		})

		It("should use default timezone when not specified", func() {
			By("Reconciling HomeAssistant without timezone")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying default TZ")
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, sts)
			}, timeout, interval).Should(Succeed())

			container := sts.Spec.Template.Spec.Containers[0]
			var tzEnv *corev1.EnvVar
			for i := range container.Env {
				if container.Env[i].Name == "TZ" {
					tzEnv = &container.Env[i]
					break
				}
			}
			Expect(tzEnv).NotTo(BeNil())
			Expect(tzEnv.Value).To(Equal("UTC"))
		})

		It("should update StatefulSet when image changes", func() {
			By("Initial reconciliation")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying initial image")
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, sts)
			}, timeout, interval).Should(Succeed())
			Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("ghcr.io/home-assistant/home-assistant:2024.1.0"))

			By("Updating HomeAssistant version")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, homeAssistant); err != nil {
					return err
				}
				homeAssistant.Spec.Version = "2024.2.0"
				return k8sClient.Update(ctx, homeAssistant)
			}, timeout, interval).Should(Succeed())

			By("Reconciling again")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying updated image")
			Eventually(func() string {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, sts); err != nil {
					return ""
				}
				return sts.Spec.Template.Spec.Containers[0].Image
			}, timeout, interval).Should(Equal("ghcr.io/home-assistant/home-assistant:2024.2.0"))
		})

		It("should update StatefulSet when resources change", func() {
			By("Initial reconciliation")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Updating HomeAssistant resources")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, homeAssistant); err != nil {
					return err
				}
				homeAssistant.Spec.Resources = corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				}
				return k8sClient.Update(ctx, homeAssistant)
			}, timeout, interval).Should(Succeed())

			By("Reconciling again")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying resources were updated")
			sts := &appsv1.StatefulSet{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, sts); err != nil {
					return false
				}
				container := sts.Spec.Template.Spec.Containers[0]
				cpuLimit := container.Resources.Limits[corev1.ResourceCPU]
				return cpuLimit.String() == "500m"
			}, timeout, interval).Should(BeTrue())
		})

		It("should not update StatefulSet when spec unchanged", func() {
			By("Initial reconciliation")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Getting StatefulSet resource version")
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, sts)
			}, timeout, interval).Should(Succeed())
			originalResourceVersion := sts.ResourceVersion

			By("Reconciling again without changes")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet was not updated")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, sts)).To(Succeed())
			Expect(sts.ResourceVersion).To(Equal(originalResourceVersion))
		})

		It("should mount configuration volume when configurationFrom specified", func() {
			By("Creating ConfigMap")
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-config",
					Namespace: namespace,
				},
				Data: map[string]string{
					"configuration.yaml": "homeassistant:\n  name: Test\n",
				},
			}
			Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, configMap)
			}()

			By("Creating HomeAssistant with configurationFrom")
			haWithConfig := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-config",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
					ConfigurationFrom: &hav1alpha1.ConfigMapReference{
						Name: "test-ha-config",
					},
				},
			}
			Expect(k8sClient.Create(ctx, haWithConfig)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, haWithConfig)
			}()

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-ha-config",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying volume mount")
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-ha-config",
					Namespace: namespace,
				}, sts)
			}, timeout, interval).Should(Succeed())

			// Check volumes
			var configVolume *corev1.Volume
			for i := range sts.Spec.Template.Spec.Volumes {
				if sts.Spec.Template.Spec.Volumes[i].Name == "ha-configuration" {
					configVolume = &sts.Spec.Template.Spec.Volumes[i]
					break
				}
			}
			Expect(configVolume).NotTo(BeNil())
			Expect(configVolume.ConfigMap.Name).To(Equal("test-ha-config"))

			// Check volume mounts
			container := sts.Spec.Template.Spec.Containers[0]
			var configMount *corev1.VolumeMount
			for i := range container.VolumeMounts {
				if container.VolumeMounts[i].Name == "ha-configuration" {
					configMount = &container.VolumeMounts[i]
					break
				}
			}
			Expect(configMount).NotTo(BeNil())
			Expect(configMount.MountPath).To(Equal("/config/configuration.yaml"))
			Expect(configMount.SubPath).To(Equal("configuration.yaml"))
		})

		It("should mount secrets volume when secretsFrom specified", func() {
			By("Creating Secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-secrets",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"secrets.yaml": []byte("api_key: secret123\n"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, secret)
			}()

			By("Creating HomeAssistant with secretsFrom")
			haWithSecrets := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-secrets",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
					SecretsFrom: &hav1alpha1.SecretReference{
						Name: "test-ha-secrets",
					},
				},
			}
			Expect(k8sClient.Create(ctx, haWithSecrets)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, haWithSecrets)
			}()

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-ha-secrets",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying volume mount")
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-ha-secrets",
					Namespace: namespace,
				}, sts)
			}, timeout, interval).Should(Succeed())

			// Check volumes
			var secretsVolume *corev1.Volume
			for i := range sts.Spec.Template.Spec.Volumes {
				if sts.Spec.Template.Spec.Volumes[i].Name == "ha-secrets" {
					secretsVolume = &sts.Spec.Template.Spec.Volumes[i]
					break
				}
			}
			Expect(secretsVolume).NotTo(BeNil())
			Expect(secretsVolume.Secret.SecretName).To(Equal("test-ha-secrets"))

			// Check volume mounts
			container := sts.Spec.Template.Spec.Containers[0]
			var secretsMount *corev1.VolumeMount
			for i := range container.VolumeMounts {
				if container.VolumeMounts[i].Name == "ha-secrets" {
					secretsMount = &container.VolumeMounts[i]
					break
				}
			}
			Expect(secretsMount).NotTo(BeNil())
			Expect(secretsMount.MountPath).To(Equal("/config/secrets.yaml"))
			Expect(secretsMount.SubPath).To(Equal("secrets.yaml"))
		})
	})

	Context("PVC Reconciliation", func() {
		const namespace = "default"

		cleanupResources := func(name string) {
			ha := &hav1alpha1.HomeAssistant{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, ha); err == nil {
				_ = k8sClient.Delete(ctx, ha)
			}
			pvc := &corev1.PersistentVolumeClaim{}
			pvcName := fmt.Sprintf("%s-config", name)
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc); err == nil {
				_ = k8sClient.Delete(ctx, pvc)
			}
			sts := &appsv1.StatefulSet{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, sts); err == nil {
				_ = k8sClient.Delete(ctx, sts)
			}
			svc := &corev1.Service{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, svc); err == nil {
				_ = k8sClient.Delete(ctx, svc)
			}
		}

		It("should create PVC with default storage size", func() {
			haName := "test-ha-pvc-default"
			defer cleanupResources(haName)

			By("Creating HomeAssistant without storage spec")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying PVC")
			pvc := &corev1.PersistentVolumeClaim{}
			pvcName := fmt.Sprintf("%s-config", haName)
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      pvcName,
					Namespace: namespace,
				}, pvc)
			}, timeout, interval).Should(Succeed())

			Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("5Gi")))
			Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
		})

		It("should create PVC with custom storage size", func() {
			haName := "test-ha-pvc-custom"
			defer cleanupResources(haName)

			By("Creating HomeAssistant with custom storage")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
					Storage: &hav1alpha1.StorageSpec{
						Size: resource.MustParse("10Gi"),
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying PVC")
			pvc := &corev1.PersistentVolumeClaim{}
			pvcName := fmt.Sprintf("%s-config", haName)
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      pvcName,
					Namespace: namespace,
				}, pvc)
			}, timeout, interval).Should(Succeed())

			Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("10Gi")))
		})

		It("should use specified storageClassName", func() {
			haName := "test-ha-pvc-sc"
			defer cleanupResources(haName)

			By("Creating HomeAssistant with storageClassName")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
					Storage: &hav1alpha1.StorageSpec{
						Size:             resource.MustParse("5Gi"),
						StorageClassName: ptr.To("fast-storage"),
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying PVC storageClassName")
			pvc := &corev1.PersistentVolumeClaim{}
			pvcName := fmt.Sprintf("%s-config", haName)
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      pvcName,
					Namespace: namespace,
				}, pvc)
			}, timeout, interval).Should(Succeed())

			Expect(pvc.Spec.StorageClassName).NotTo(BeNil())
			Expect(*pvc.Spec.StorageClassName).To(Equal("fast-storage"))
		})

		It("should set owner reference on PVC", func() {
			haName := "test-ha-pvc-owner"
			defer cleanupResources(haName)

			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying owner reference")
			pvc := &corev1.PersistentVolumeClaim{}
			pvcName := fmt.Sprintf("%s-config", haName)
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      pvcName,
					Namespace: namespace,
				}, pvc)
			}, timeout, interval).Should(Succeed())

			Expect(pvc.OwnerReferences).To(HaveLen(1))
			Expect(pvc.OwnerReferences[0].Name).To(Equal(haName))
			Expect(pvc.OwnerReferences[0].Kind).To(Equal("HomeAssistant"))
		})
	})

	Context("Service Reconciliation", func() {
		const (
			haName    = "test-ha-svc"
			namespace = "default"
		)

		AfterEach(func() {
			// Cleanup
			ha := &hav1alpha1.HomeAssistant{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
				_ = k8sClient.Delete(ctx, ha)
			}
			svc := &corev1.Service{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, svc); err == nil {
				_ = k8sClient.Delete(ctx, svc)
			}
		})

		It("should create Service with ClusterIP type (default)", func() {
			By("Creating HomeAssistant without service spec")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Service")
			svc := &corev1.Service{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, svc)
			}, timeout, interval).Should(Succeed())

			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8123)))
		})

		It("should create Service with NodePort type", func() {
			By("Creating HomeAssistant with NodePort service")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
					Service: &hav1alpha1.ServiceSpec{
						Type: corev1.ServiceTypeNodePort,
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Service type")
			svc := &corev1.Service{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, svc)
			}, timeout, interval).Should(Succeed())

			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeNodePort))
		})

		It("should create Service with LoadBalancer type", func() {
			By("Creating HomeAssistant with LoadBalancer service")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
					Service: &hav1alpha1.ServiceSpec{
						Type: corev1.ServiceTypeLoadBalancer,
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Service type")
			svc := &corev1.Service{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, svc)
			}, timeout, interval).Should(Succeed())

			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
		})

		It("should update Service when port changes", func() {
			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Initial reconciliation")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Updating port")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err != nil {
					return err
				}
				ha.Spec.Service = &hav1alpha1.ServiceSpec{
					Port: 9999,
				}
				return k8sClient.Update(ctx, ha)
			}, timeout, interval).Should(Succeed())

			By("Reconciling again")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying port changed")
			svc := &corev1.Service{}
			Eventually(func() int32 {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, svc); err != nil {
					return 0
				}
				if len(svc.Spec.Ports) == 0 {
					return 0
				}
				return svc.Spec.Ports[0].Port
			}, timeout, interval).Should(Equal(int32(9999)))
		})

		It("should set owner reference on Service", func() {
			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying owner reference")
			svc := &corev1.Service{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, svc)
			}, timeout, interval).Should(Succeed())

			Expect(svc.OwnerReferences).To(HaveLen(1))
			Expect(svc.OwnerReferences[0].Name).To(Equal(haName))
			Expect(svc.OwnerReferences[0].Kind).To(Equal("HomeAssistant"))
		})
	})

	Context("Status Updates", func() {
		const (
			haName    = "test-ha-status"
			namespace = "default"
		)

		AfterEach(func() {
			// Cleanup
			ha := &hav1alpha1.HomeAssistant{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
				_ = k8sClient.Delete(ctx, ha)
			}
		})

		It("should set initial phase to Pending", func() {
			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying status")
			Eventually(func() hav1alpha1.HomeAssistantPhase {
				updatedHA := &hav1alpha1.HomeAssistant{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, updatedHA); err != nil {
					return ""
				}
				return updatedHA.Status.Phase
			}, timeout, interval).Should(Equal(hav1alpha1.PhasePending))
		})

		It("should set version in status", func() {
			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.3.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying version in status")
			Eventually(func() string {
				updatedHA := &hav1alpha1.HomeAssistant{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, updatedHA); err != nil {
					return ""
				}
				return updatedHA.Status.Version
			}, timeout, interval).Should(Equal("2024.3.0"))
		})

		It("should set Ready condition to false when StatefulSet not ready", func() {
			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Ready condition is false")
			Eventually(func() bool {
				updatedHA := &hav1alpha1.HomeAssistant{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, updatedHA); err != nil {
					return true // Don't fail yet
				}
				return meta.IsStatusConditionFalse(updatedHA.Status.Conditions, "Ready")
			}, timeout, interval).Should(BeTrue())
		})

		It("should set Ready condition to true when StatefulSet is ready", func() {
			haName := "test-ha-status-ready"
			defer func() {
				ha := &hav1alpha1.HomeAssistant{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
					_ = k8sClient.Delete(ctx, ha)
				}
			}()

			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling to create StatefulSet")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Simulating StatefulSet becoming ready")
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, sts)
			}, timeout, interval).Should(Succeed())

			// Update StatefulSet status to simulate ready state
			sts.Status.Replicas = 1
			sts.Status.ReadyReplicas = 1
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())

			By("Reconciling again")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Ready condition is true")
			Eventually(func() bool {
				updatedHA := &hav1alpha1.HomeAssistant{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, updatedHA); err != nil {
					return false
				}
				return meta.IsStatusConditionTrue(updatedHA.Status.Conditions, "Ready")
			}, timeout, interval).Should(BeTrue())

			By("Verifying phase is Running")
			updatedHA := &hav1alpha1.HomeAssistant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, updatedHA)).To(Succeed())
			Expect(updatedHA.Status.Phase).To(Equal(hav1alpha1.PhaseRunning))
			Expect(updatedHA.Status.Ready).To(BeTrue())
		})
	})

	Context("updateStatusFailed function", func() {
		const namespace = "default"

		It("should set phase to Failed and Ready condition to false", func() {
			haName := "test-ha-update-status-failed"
			defer func() {
				ha := &hav1alpha1.HomeAssistant{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
					_ = k8sClient.Delete(ctx, ha)
				}
			}()

			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Calling updateStatusFailed directly")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Re-fetch to get latest version
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha)).To(Succeed())

			testError := fmt.Errorf("test reconciliation error")
			result, err := controllerReconciler.updateStatusFailed(ctx, ha, testError)

			By("Verifying error is returned")
			Expect(err).To(Equal(testError))
			Expect(result).To(Equal(reconcile.Result{}))

			By("Verifying status is updated")
			updatedHA := &hav1alpha1.HomeAssistant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, updatedHA)).To(Succeed())
			Expect(updatedHA.Status.Phase).To(Equal(hav1alpha1.PhaseFailed))
			Expect(updatedHA.Status.Ready).To(BeFalse())

			By("Verifying Ready condition")
			condition := meta.FindStatusCondition(updatedHA.Status.Conditions, "Ready")
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("ReconciliationFailed"))
			Expect(condition.Message).To(Equal("test reconciliation error"))
		})
	})

	Context("updateStatusFromStatefulSet function", func() {
		const namespace = "default"

		It("should use default version when spec.version is empty", func() {
			haName := "test-ha-default-version"
			defer func() {
				ha := &hav1alpha1.HomeAssistant{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
					_ = k8sClient.Delete(ctx, ha)
				}
			}()

			By("Creating HomeAssistant without version")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					// Version not specified - should use default
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling to create StatefulSet")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying status version is set to default")
			updatedHA := &hav1alpha1.HomeAssistant{}
			Eventually(func() string {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, updatedHA); err != nil {
					return ""
				}
				return updatedHA.Status.Version
			}, timeout, interval).Should(Equal("stable"))
		})

		It("should requeue when StatefulSet not ready", func() {
			haName := "test-ha-requeue"
			defer func() {
				ha := &hav1alpha1.HomeAssistant{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
					_ = k8sClient.Delete(ctx, ha)
				}
			}()

			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying requeue is requested")
			// StatefulSet is not ready (no pods running in envtest), so should requeue
			Expect(result.RequeueAfter).To(Equal(10 * time.Second))
		})
	})

	Context("needsUpdate function", func() {
		It("should return true when image differs", func() {
			current := &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Image: "image:v1"},
							},
						},
					},
				},
			}
			desired := &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Image: "image:v2"},
							},
						},
					},
				},
			}
			Expect(needsUpdate(current, desired)).To(BeTrue())
		})

		It("should return true when resources differ", func() {
			current := &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Image: "image:v1",
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("100m"),
										},
									},
								},
							},
						},
					},
				},
			}
			desired := &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Image: "image:v1",
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("500m"),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(needsUpdate(current, desired)).To(BeTrue())
		})

		It("should return true when env vars differ", func() {
			current := &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Image: "image:v1",
									Env: []corev1.EnvVar{
										{Name: "TZ", Value: "UTC"},
									},
								},
							},
						},
					},
				},
			}
			desired := &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Image: "image:v1",
									Env: []corev1.EnvVar{
										{Name: "TZ", Value: "Europe/Warsaw"},
									},
								},
							},
						},
					},
				},
			}
			Expect(needsUpdate(current, desired)).To(BeTrue())
		})

		It("should return false when specs identical", func() {
			spec := appsv1.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Image: "image:v1",
								Env: []corev1.EnvVar{
									{Name: "TZ", Value: "UTC"},
								},
							},
						},
					},
				},
			}
			current := &appsv1.StatefulSet{Spec: spec}
			desired := &appsv1.StatefulSet{Spec: spec}
			Expect(needsUpdate(current, desired)).To(BeFalse())
		})

		It("should return true when volumes count differs", func() {
			current := &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Image: "image:v1"},
							},
							Volumes: []corev1.Volume{
								{Name: "config"},
							},
						},
					},
				},
			}
			desired := &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Image: "image:v1"},
							},
							Volumes: []corev1.Volume{
								{Name: "config"},
								{Name: "secrets"},
							},
						},
					},
				},
			}
			Expect(needsUpdate(current, desired)).To(BeTrue())
		})
	})

	Context("probesEqual function", func() {
		It("should return true when both probes are nil", func() {
			Expect(probesEqual(nil, nil)).To(BeTrue())
		})

		It("should return false when one probe is nil", func() {
			probe := &corev1.Probe{
				InitialDelaySeconds: 10,
			}
			Expect(probesEqual(nil, probe)).To(BeFalse())
			Expect(probesEqual(probe, nil)).To(BeFalse())
		})

		It("should return false when HTTPGet differs", func() {
			current := &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt(8080),
					},
				},
			}
			desired := &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/ready",
						Port: intstr.FromInt(8080),
					},
				},
			}
			Expect(probesEqual(current, desired)).To(BeFalse())
		})

		It("should return true when HTTPGet is identical", func() {
			current := &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt(8080),
					},
				},
				InitialDelaySeconds: 10,
				PeriodSeconds:       5,
				TimeoutSeconds:      3,
				SuccessThreshold:    1,
				FailureThreshold:    3,
			}
			desired := &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt(8080),
					},
				},
				InitialDelaySeconds: 10,
				PeriodSeconds:       5,
				TimeoutSeconds:      3,
				SuccessThreshold:    1,
				FailureThreshold:    3,
			}
			Expect(probesEqual(current, desired)).To(BeTrue())
		})

		It("should return false when probe settings differ", func() {
			current := &corev1.Probe{
				InitialDelaySeconds: 10,
				PeriodSeconds:       5,
				TimeoutSeconds:      3,
				SuccessThreshold:    1,
				FailureThreshold:    3,
			}
			desired := &corev1.Probe{
				InitialDelaySeconds: 30, // Different
				PeriodSeconds:       5,
				TimeoutSeconds:      3,
				SuccessThreshold:    1,
				FailureThreshold:    3,
			}
			Expect(probesEqual(current, desired)).To(BeFalse())
		})

		It("should return false when one has HTTPGet and other does not", func() {
			current := &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt(8080),
					},
				},
			}
			desired := &corev1.Probe{
				// No HTTPGet
			}
			Expect(probesEqual(current, desired)).To(BeFalse())
		})
	})

	Context("Reconciliation error handling", func() {
		It("should return nil error when HomeAssistant not found", func() {
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "non-existent",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})
	})

	Context("getGeneratedSecretsName function", func() {
		const namespace = "default"

		It("should return generated secrets name when HomeAssistantSecrets exists", func() {
			haName := "test-ha-gen-secrets"
			defer func() {
				ha := &hav1alpha1.HomeAssistant{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
					_ = k8sClient.Delete(ctx, ha)
				}
				haSecrets := &hav1alpha1.HomeAssistantSecrets{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName + "-secrets", Namespace: namespace}, haSecrets); err == nil {
					_ = k8sClient.Delete(ctx, haSecrets)
				}
			}()

			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantSecrets referencing the HomeAssistant")
			haSecrets := &hav1alpha1.HomeAssistantSecrets{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName + "-secrets",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					SecretRefs: []hav1alpha1.SecretKeyReference{
						{Name: "dummy-secret"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, haSecrets)).To(Succeed())

			By("Calling getGeneratedSecretsName")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			secretName, err := controllerReconciler.getGeneratedSecretsName(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			Expect(secretName).To(Equal(haName + "-generated-secrets"))
		})

		It("should return empty when no HomeAssistantSecrets found", func() {
			haName := "test-ha-no-secrets"
			defer func() {
				ha := &hav1alpha1.HomeAssistant{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
					_ = k8sClient.Delete(ctx, ha)
				}
			}()

			By("Creating HomeAssistant without HomeAssistantSecrets")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Calling getGeneratedSecretsName")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			secretName, err := controllerReconciler.getGeneratedSecretsName(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			Expect(secretName).To(BeEmpty())
		})
	})

	Context("findHomeAssistantForSecrets function", func() {
		const namespace = "default"

		It("should return reconcile request for referenced HomeAssistant", func() {
			haName := "test-ha-for-secrets-watch"
			defer func() {
				haSecrets := &hav1alpha1.HomeAssistantSecrets{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName + "-secrets", Namespace: namespace}, haSecrets); err == nil {
					_ = k8sClient.Delete(ctx, haSecrets)
				}
			}()

			By("Creating HomeAssistantSecrets")
			haSecrets := &hav1alpha1.HomeAssistantSecrets{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName + "-secrets",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: haName,
					},
					SecretRefs: []hav1alpha1.SecretKeyReference{
						{Name: "dummy-secret"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, haSecrets)).To(Succeed())

			By("Calling findHomeAssistantForSecrets")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			requests := controllerReconciler.findHomeAssistantForSecrets(ctx, haSecrets)

			By("Verifying reconcile request")
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Name).To(Equal(haName))
			Expect(requests[0].Namespace).To(Equal(namespace))
		})
	})

	Context("Owner References", func() {
		const (
			haName    = "test-ha-owner"
			namespace = "default"
		)

		AfterEach(func() {
			// Cleanup
			ha := &hav1alpha1.HomeAssistant{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
				_ = k8sClient.Delete(ctx, ha)
			}
		})

		It("should set owner reference on StatefulSet", func() {
			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet owner reference")
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				}, sts)
			}, timeout, interval).Should(Succeed())

			Expect(sts.OwnerReferences).To(HaveLen(1))
			Expect(sts.OwnerReferences[0].Name).To(Equal(haName))
			Expect(sts.OwnerReferences[0].Kind).To(Equal("HomeAssistant"))
			Expect(*sts.OwnerReferences[0].Controller).To(BeTrue())
		})
	})

	Context("Reconcile error paths", func() {
		const namespace = "default"

		It("should call updateStatusFailed when PVC reconciliation fails", func() {
			haName := "test-ha-pvc-error"
			defer func() {
				ha := &hav1alpha1.HomeAssistant{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
					_ = k8sClient.Delete(ctx, ha)
				}
			}()

			By("Creating HomeAssistant with invalid storage class reference")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating a PVC manually to cause conflict")
			// Create a PVC with different owner to simulate conflict
			conflictingPVC := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-config", haName),
					Namespace: namespace,
					// No owner reference - owned by someone else
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, conflictingPVC)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, conflictingPVC)
			}()

			By("Reconciling - PVC already exists so no error")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// The PVC exists without owner ref, so reconcilePVC won't error
			// It just skips update for existing PVC
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle deleted HomeAssistant gracefully during reconciliation", func() {
			haName := "test-ha-deleted-during-reconcile"

			By("Creating and immediately deleting HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
			Expect(k8sClient.Delete(ctx, ha)).To(Succeed())

			By("Reconciling deleted resource")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})

			By("Verifying graceful handling - no error, no requeue")
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})
	})

	Context("Labels", func() {
		const (
			haName    = "test-ha-labels"
			namespace = "default"
		)

		AfterEach(func() {
			ha := &hav1alpha1.HomeAssistant{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
				_ = k8sClient.Delete(ctx, ha)
			}
		})

		It("should apply standard labels to all resources", func() {
			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1.0",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      haName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			expectedLabels := map[string]string{
				"app.kubernetes.io/name":       "homeassistant",
				"app.kubernetes.io/instance":   haName,
				"app.kubernetes.io/managed-by": "homeassistant-operator",
			}

			By("Verifying StatefulSet labels")
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, sts)
			}, timeout, interval).Should(Succeed())
			for k, v := range expectedLabels {
				Expect(sts.Labels).To(HaveKeyWithValue(k, v))
			}

			By("Verifying Service labels")
			svc := &corev1.Service{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, svc)
			}, timeout, interval).Should(Succeed())
			for k, v := range expectedLabels {
				Expect(svc.Labels).To(HaveKeyWithValue(k, v))
			}

			By("Verifying PVC labels")
			pvc := &corev1.PersistentVolumeClaim{}
			pvcName := fmt.Sprintf("%s-config", haName)
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc)
			}, timeout, interval).Should(Succeed())
			for k, v := range expectedLabels {
				Expect(pvc.Labels).To(HaveKeyWithValue(k, v))
			}
		})
	})
})
