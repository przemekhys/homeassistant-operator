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
	"os"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

var _ = Describe("HomeAssistant Controller", func() {
	Context("When reconciling a HomeAssistant resource", func() {
		const (
			resourceName            = "test-homeassistant"
			namespace               = "default"
			timeout                 = time.Second * 10
			interval                = time.Millisecond * 250
			configurationVolumeName = "ha-configuration"
		)

		AfterEach(func() {
			// Cleanup HomeAssistantConfiguration resources
			configList := &hav1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}

			// Cleanup all HomeAssistant resources
			haList := &hav1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}

			// Cleanup all ConfigMaps
			cmList := &corev1.ConfigMapList{}
			_ = k8sClient.List(ctx, cmList, &client.ListOptions{Namespace: namespace})
			for _, cm := range cmList.Items {
				_ = k8sClient.Delete(ctx, &cm)
			}

			// Wait for resources to be deleted
			Eventually(func() int {
				haList := &hav1.HomeAssistantList{}
				_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
				return len(haList.Items)
			}, timeout, interval).Should(Equal(0))
		})

		It("should create StatefulSet when HomeAssistant is created", func() {
			By("Creating a new HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "2024.1",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet was created")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.Template.Spec.Containers).To(HaveLen(1))
				g.Expect(sts.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring("2024.1"))
			}, timeout, interval).Should(Succeed())
		})

		It("should create Service when HomeAssistant is created", func() {
			By("Creating a new HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Service was created")
			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, svc)).To(Succeed())
				g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
				g.Expect(svc.Spec.Ports).To(HaveLen(1))
				g.Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8123)))
			}, timeout, interval).Should(Succeed())
		})

		It("should create PVC when HomeAssistant is created", func() {
			By("Creating a new HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying PVC was created")
			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName + "-data",
					Namespace: namespace,
				}, pvc)).To(Succeed())
				storageSize := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
				g.Expect(storageSize.String()).To(Equal("5Gi"))
			}, timeout, interval).Should(Succeed())
		})

		It("should update StatefulSet image when version is updated", func() {
			By("Creating a new HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "2024.1",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying initial image version")
			sts := &appsv1.StatefulSet{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring("2024.1"))
			}, timeout, interval).Should(Succeed())

			By("Updating HomeAssistant version")
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, ha)
				if err != nil {
					return err
				}
				ha.Spec.Version = "2024.2"
				return k8sClient.Update(ctx, ha)
			}, timeout, interval).Should(Succeed())

			By("Reconciling after update")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying image was updated")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring("2024.2"))
			}, timeout, interval).Should(Succeed())
		})

		It("should update StatefulSet resources when resources are updated", func() {
			By("Creating a new HomeAssistant with initial resources")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying initial resources")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("100m"))
			}, timeout, interval).Should(Succeed())

			By("Updating HomeAssistant resources")
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, ha)
				if err != nil {
					return err
				}
				ha.Spec.Resources = corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				}
				return k8sClient.Update(ctx, ha)
			}, timeout, interval).Should(Succeed())

			By("Reconciling after update")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying resources were updated")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("200m"))
				g.Expect(sts.Spec.Template.Spec.Containers[0].Resources.Requests.Memory().String()).To(Equal("512Mi"))
			}, timeout, interval).Should(Succeed())
		})

		It("should require HomeAssistantConfiguration to be created", func() {
			By("Creating a HomeAssistant without HomeAssistantConfiguration")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Reconciling the resource should wait for HomeAssistantConfiguration")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0), "Should requeue to wait for HomeAssistantConfiguration")

			By("Creating HomeAssistantConfiguration")
			config := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling again should now succeed")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet was created")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("should add Secret volume when secretsFrom is specified", func() {
			By("Creating a Secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"secrets.yaml": []byte("test_key: test_value"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, secret)
			}()

			By("Creating a HomeAssistant with secretsFrom")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
					SecretsFrom: &hav1.SecretReference{
						Name: "test-secret",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Secret volume was added")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())

				// Check volumes
				found := false
				for _, vol := range sts.Spec.Template.Spec.Volumes {
					if vol.Name == "ha-secrets" && vol.Secret != nil && vol.Secret.SecretName == "test-secret" {
						found = true
						break
					}
				}
				g.Expect(found).To(BeTrue(), "Secret volume should be added")

				// Check volume mounts
				mountFound := false
				for _, mount := range sts.Spec.Template.Spec.Containers[0].VolumeMounts {
					if mount.Name == "ha-secrets" && mount.MountPath == "/config/secrets.yaml" {
						mountFound = true
						break
					}
				}
				g.Expect(mountFound).To(BeTrue(), "Secret volume mount should be added")
			}, timeout, interval).Should(Succeed())
		})

		It("should not update StatefulSet when no changes are made (idempotency)", func() {
			By("Creating a new HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Getting initial StatefulSet resource version")
			sts := &appsv1.StatefulSet{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
			}, timeout, interval).Should(Succeed())
			initialResourceVersion := sts.ResourceVersion

			By("Reconciling again without changes")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet was not updated")
			Consistently(func() string {
				sts := &appsv1.StatefulSet{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts); err != nil {
					return ""
				}
				return sts.ResourceVersion
			}, time.Second*2, interval).Should(Equal(initialResourceVersion))
		})

		It("should set owner references on all child resources", func() {
			By("Creating a new HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet has owner reference")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.OwnerReferences).To(HaveLen(1))
				g.Expect(sts.OwnerReferences[0].Kind).To(Equal("HomeAssistant"))
				g.Expect(sts.OwnerReferences[0].Name).To(Equal(resourceName))
			}, timeout, interval).Should(Succeed())

			By("Verifying Service has owner reference")
			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, svc)).To(Succeed())
				g.Expect(svc.OwnerReferences).To(HaveLen(1))
				g.Expect(svc.OwnerReferences[0].Kind).To(Equal("HomeAssistant"))
			}, timeout, interval).Should(Succeed())

			By("Verifying PVC has owner reference")
			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName + "-data",
					Namespace: namespace,
				}, pvc)).To(Succeed())
				g.Expect(pvc.OwnerReferences).To(HaveLen(1))
				g.Expect(pvc.OwnerReferences[0].Kind).To(Equal("HomeAssistant"))
			}, timeout, interval).Should(Succeed())
		})

		It("should update status conditions correctly", func() {
			By("Creating a new HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying status condition is set")
			Eventually(func(g Gomega) {
				updatedHA := &hav1.HomeAssistant{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, updatedHA)).To(Succeed())

				condition := meta.FindStatusCondition(updatedHA.Status.Conditions, "Ready")
				g.Expect(condition).NotTo(BeNil())
				// Initially not ready because StatefulSet has no ready replicas in test
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Reason).To(Equal("StatefulSetNotReady"))
			}, timeout, interval).Should(Succeed())
		})

		It("should set custom timezone as TZ environment variable", func() {
			By("Creating a HomeAssistant with custom timezone")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version:  "stable",
					Timezone: "Europe/Warsaw",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying TZ environment variable is set")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())

				found := false
				for _, env := range sts.Spec.Template.Spec.Containers[0].Env {
					if env.Name == "TZ" && env.Value == "Europe/Warsaw" {
						found = true
						break
					}
				}
				g.Expect(found).To(BeTrue(), "TZ environment variable should be set to Europe/Warsaw")
			}, timeout, interval).Should(Succeed())
		})

		It("should use default timezone UTC when not specified", func() {
			By("Creating a HomeAssistant without timezone")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying TZ defaults to UTC")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())

				found := false
				for _, env := range sts.Spec.Template.Spec.Containers[0].Env {
					if env.Name == "TZ" && env.Value == "UTC" {
						found = true
						break
					}
				}
				g.Expect(found).To(BeTrue(), "TZ environment variable should default to UTC")
			}, timeout, interval).Should(Succeed())
		})

		It("should use custom port when specified in Service", func() {
			By("Creating a HomeAssistant with custom port")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
					Service: &hav1.ServiceSpec{
						Port: 9000,
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Service port matches")
			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, svc)).To(Succeed())
				g.Expect(svc.Spec.Ports).To(HaveLen(1))
				g.Expect(svc.Spec.Ports[0].Port).To(Equal(int32(9000)))
			}, timeout, interval).Should(Succeed())
		})

		It("should create NodePort service when service type is NodePort", func() {
			By("Creating a HomeAssistant with NodePort service")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
					Service: &hav1.ServiceSpec{
						Type:     corev1.ServiceTypeNodePort,
						NodePort: 30123,
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying NodePort is assigned")
			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, svc)).To(Succeed())
				g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeNodePort))
				g.Expect(svc.Spec.Ports).To(HaveLen(1))
				g.Expect(svc.Spec.Ports[0].NodePort).To(Equal(int32(30123)))
			}, timeout, interval).Should(Succeed())
		})

		It("should use StatefulSets with proper replicas", func() {
			By("Creating a new HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet has 1 replica")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.Replicas).To(Equal(ptr.To(int32(1))))
			}, timeout, interval).Should(Succeed())
		})

		It("should automatically mount generated ConfigMap from HomeAssistantConfiguration", func() {
			By("Creating HomeAssistant resource")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration that references the HomeAssistant")
			config := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-auto",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling the HomeAssistant (should auto-detect generated ConfigMap)")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet mounts the generated ConfigMap")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())

				// Check volumes
				var hasConfigVolume bool
				for _, vol := range sts.Spec.Template.Spec.Volumes {
					if vol.Name == configurationVolumeName && vol.ConfigMap != nil {
						hasConfigVolume = vol.ConfigMap.Name == resourceName+"-configuration"
						break
					}
				}
				g.Expect(hasConfigVolume).To(BeTrue(),
					"StatefulSet should mount ConfigMap volume with name <ha-name>-configuration")

				// Check volume mounts
				container := sts.Spec.Template.Spec.Containers[0]
				var hasConfigMount bool
				for _, mount := range container.VolumeMounts {
					if mount.Name == configurationVolumeName && mount.MountPath == "/config/configuration.yaml" {
						hasConfigMount = true
						break
					}
				}
				g.Expect(hasConfigMount).To(BeTrue(), "Container should mount configuration.yaml at /config/configuration.yaml")
			}, timeout, interval).Should(Succeed())
		})

		It("should mount generated ConfigMap when HomeAssistantConfiguration exists", func() {
			By("Creating HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration that references the HomeAssistant")
			config := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "config-mount-test",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())

			By("Reconciling the HomeAssistant")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet mounts the generated ConfigMap")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())

				// Check volumes - should use generated ConfigMap
				var hasGeneratedConfigVolume bool
				for _, vol := range sts.Spec.Template.Spec.Volumes {
					if vol.Name == configurationVolumeName && vol.ConfigMap != nil {
						hasGeneratedConfigVolume = vol.ConfigMap.Name == resourceName+"-configuration"
						break
					}
				}
				g.Expect(hasGeneratedConfigVolume).To(BeTrue(), "Should mount HomeAssistantConfiguration generated ConfigMap")
			}, timeout, interval).Should(Succeed())
		})

		It("should preserve existing pod template annotations when building StatefulSet", func() {
			// Use unique name to avoid conflicts with other tests
			testName := resourceName + "-preserve"

			By("Creating a new HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "2024.1",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: testName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Creating a StatefulSet with existing annotations")
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name":       "homeassistant",
						"app.kubernetes.io/instance":   testName,
						"app.kubernetes.io/managed-by": "homeassistant-operator",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name":       "homeassistant",
							"app.kubernetes.io/instance":   testName,
							"app.kubernetes.io/managed-by": "homeassistant-operator",
						},
					},
					ServiceName: testName,
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app.kubernetes.io/name":       "homeassistant",
								"app.kubernetes.io/instance":   testName,
								"app.kubernetes.io/managed-by": "homeassistant-operator",
							},
							Annotations: map[string]string{
								"ha.homeassistant.io/config-hash": "existing-hash-abc123",
								"some-other-annotation":           "some-value",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "home-assistant",
									Image: "ghcr.io/home-assistant/home-assistant:2024.1",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			By("Building desired StatefulSet via reconciler")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			desired, err := reconciler.buildStatefulSet(ctx, ha)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying annotations are preserved")
			Expect(desired.Spec.Template.Annotations).To(HaveKey("ha.homeassistant.io/config-hash"))
			Expect(desired.Spec.Template.Annotations["ha.homeassistant.io/config-hash"]).To(Equal("existing-hash-abc123"))
			Expect(desired.Spec.Template.Annotations).To(HaveKey("some-other-annotation"))
			Expect(desired.Spec.Template.Annotations["some-other-annotation"]).To(Equal("some-value"))
		})

		It("should not trigger update when config hash is unchanged", func() {
			// Use unique name to avoid conflicts
			testName := resourceName + "-nochange"

			By("Creating HomeAssistant and HomeAssistantConfiguration")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "2024.1",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: testName,
					},
					Configuration: "automation: []\nscript: []\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Creating ConfigMap with hash annotation (simulating HomeAssistantConfiguration controller)")
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName + "-configuration",
					Namespace: namespace,
					Annotations: map[string]string{
						"ha.homeassistant.io/config-hash": "test-hash-abc123",
					},
				},
				Data: map[string]string{
					"configuration.yaml": "automation: []\nscript: []\n",
				},
			}
			Expect(k8sClient.Create(ctx, configMap)).To(Succeed())

			By("Reconciling HomeAssistant to create StatefulSet")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      testName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for StatefulSet to be created with config hash")
			var initialHash string
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      testName,
					Namespace: namespace,
				}, sts)).To(Succeed())

				g.Expect(sts.Spec.Template.Annotations).NotTo(BeNil())
				hash, exists := sts.Spec.Template.Annotations["ha.homeassistant.io/config-hash"]
				g.Expect(exists).To(BeTrue(), "Config hash annotation should exist")
				initialHash = hash
			}, timeout, interval).Should(Succeed())

			By("Reconciling again without changing configuration")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      testName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying hash remains unchanged and no update triggered")
			Consistently(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      testName,
					Namespace: namespace,
				}, sts)).To(Succeed())

				currentHash := sts.Spec.Template.Annotations["ha.homeassistant.io/config-hash"]
				g.Expect(currentHash).To(Equal(initialHash), "Hash should remain unchanged")
			}, time.Second*2, interval).Should(Succeed())
		})
	})

	Context("When hostNetwork is configured", func() {
		const (
			testName  = "test-hostnet"
			namespace = "default"
			timeout   = time.Second * 10
			interval  = time.Millisecond * 250
		)

		AfterEach(func() {
			configList := &hav1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}
			haList := &hav1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}
			cmList := &corev1.ConfigMapList{}
			_ = k8sClient.List(ctx, cmList, &client.ListOptions{Namespace: namespace})
			for _, cm := range cmList.Items {
				_ = k8sClient.Delete(ctx, &cm)
			}
			Eventually(func() int {
				haList := &hav1.HomeAssistantList{}
				_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
				return len(haList.Items)
			}, timeout, interval).Should(Equal(0))
			Eventually(func() int {
				configList := &hav1.HomeAssistantConfigurationList{}
				_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
				return len(configList.Items)
			}, timeout, interval).Should(Equal(0))
			Eventually(func() int {
				cmList := &corev1.ConfigMapList{}
				_ = k8sClient.List(ctx, cmList, &client.ListOptions{Namespace: namespace})
				return len(cmList.Items)
			}, timeout, interval).Should(Equal(0))
		})

		It("should set HostNetwork and DNSPolicy when hostNetwork is true", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version:     "2024.1",
					HostNetwork: ptr.To(true),
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{Name: testName},
					Configuration:    "homeassistant:\n  name: Test\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: testName, Namespace: namespace,
			}, sts)).To(Succeed())

			Expect(sts.Spec.Template.Spec.HostNetwork).To(BeTrue())
			Expect(sts.Spec.Template.Spec.DNSPolicy).To(Equal(corev1.DNSClusterFirstWithHostNet))
		})

		It("should not set HostNetwork when hostNetwork is false", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version:     "2024.1",
					HostNetwork: ptr.To(false),
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{Name: testName},
					Configuration:    "homeassistant:\n  name: Test\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: testName, Namespace: namespace,
			}, sts)).To(Succeed())

			Expect(sts.Spec.Template.Spec.HostNetwork).To(BeFalse())
			Expect(sts.Spec.Template.Spec.DNSPolicy).To(Equal(corev1.DNSClusterFirst))
		})

		It("should not set HostNetwork when hostNetwork is nil", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "2024.1",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{Name: testName},
					Configuration:    "homeassistant:\n  name: Test\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			controllerReconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: testName, Namespace: namespace,
			}, sts)).To(Succeed())

			Expect(sts.Spec.Template.Spec.HostNetwork).To(BeFalse())
			Expect(sts.Spec.Template.Spec.DNSPolicy).To(Equal(corev1.DNSClusterFirst))
		})

		It("should clear hostNetwork and reset DNSPolicy when toggled from true to false", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version:     "2024.1",
					HostNetwork: ptr.To(true),
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{Name: testName},
					Configuration:    "homeassistant:\n  name: Test\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: testName, Namespace: namespace,
			}, sts)).To(Succeed())
			Expect(sts.Spec.Template.Spec.HostNetwork).To(BeTrue())
			Expect(sts.Spec.Template.Spec.DNSPolicy).To(Equal(corev1.DNSClusterFirstWithHostNet))

			// Toggle hostNetwork off — re-fetch before update to get current resourceVersion
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: testName, Namespace: namespace,
			}, ha)).To(Succeed())
			ha.Spec.HostNetwork = ptr.To(false)
			Expect(k8sClient.Update(ctx, ha)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: testName, Namespace: namespace,
			}, sts)).To(Succeed())
			Expect(sts.Spec.Template.Spec.HostNetwork).To(BeFalse())
			Expect(sts.Spec.Template.Spec.DNSPolicy).To(Equal(corev1.DNSClusterFirst))
		})
	})

	Context("When Spec.AdditionalVolumes is configured", func() {
		const (
			resourceName    = "test-hass"
			pvcName         = "test-pvc"
			volumeName      = "test-volume"
			volumeMountPath = "/foo/bar"
			namespace       = "default"
			timeout         = time.Second * 10
			interval        = time.Millisecond * 250
		)

		AfterEach(func() {
			// Cleanup HomeAssistantConfiguration resources
			configList := &hav1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}

			// Cleanup all HomeAssistant resources
			haList := &hav1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}

			// Cleanup all ConfigMaps
			cmList := &corev1.ConfigMapList{}
			_ = k8sClient.List(ctx, cmList, &client.ListOptions{Namespace: namespace})
			for _, cm := range cmList.Items {
				_ = k8sClient.Delete(ctx, &cm)
			}

			// Wait for resources to be deleted
			Eventually(func() int {
				haList := &hav1.HomeAssistantList{}
				_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
				return len(haList.Items)
			}, timeout, interval).Should(Equal(0))
		})

		It("should render the volume configs in the StatefulSet", func() {
			By("Defining a volume spec and mount")
			volume := corev1.Volume{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					},
				},
			}
			volumeMount := corev1.VolumeMount{
				Name:      volumeName,
				MountPath: volumeMountPath,
			}

			By("Creating a new HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "2024.1",
					AdditionalVolumes: &hav1.AdditionalVolumesSpec{
						Volumes:      []corev1.Volume{volume},
						VolumeMounts: []corev1.VolumeMount{volumeMount},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet was created with the configured volumes")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.Template.Spec.Volumes).To(ContainElement(volume))
				g.Expect(sts.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(volumeMount))
			}, timeout, interval).Should(Succeed())
		})

		It("should update the StatefulSet on changes", func() {
			By("Defining a volume spec and mount")
			volume := corev1.Volume{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
						ReadOnly:  false,
					},
				},
			}
			volumeMount := corev1.VolumeMount{
				Name:      volumeName,
				MountPath: volumeMountPath,
			}

			By("Creating a new HomeAssistant")
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantSpec{
					Version: "2024.1",
					AdditionalVolumes: &hav1.AdditionalVolumesSpec{
						Volumes:      []corev1.Volume{volume},
						VolumeMounts: []corev1.VolumeMount{volumeMount},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			By("Creating HomeAssistantConfiguration")
			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-config",
					Namespace: namespace,
				},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{
						Name: resourceName,
					},
					Configuration: "homeassistant:\n  name: Home\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			By("Reconciling the resource")
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet was created with the configured volumes")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.Template.Spec.Volumes).To(ContainElement(volume))
				g.Expect(sts.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(volumeMount))
			}, timeout, interval).Should(Succeed())

			By("Changing the volume config")
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, ha)).To(Succeed())
			Expect(ha.Spec.AdditionalVolumes.Volumes).To(ConsistOf(volume))

			volume.PersistentVolumeClaim.ReadOnly = true
			ha.Spec.AdditionalVolumes.Volumes = []corev1.Volume{volume}
			Expect(k8sClient.Update(ctx, ha)).To(Succeed())

			By("Reconciling the updated config")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet was updated to match the new volume config")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.Template.Spec.Volumes).To(ContainElement(volume))
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("When spec.alpha.networkPolicy is configured", func() {
		const (
			testName  = "test-netpol"
			namespace = "default"
			timeout   = time.Second * 10
			interval  = time.Millisecond * 250
		)

		AfterEach(func() {
			configList := &hav1.HomeAssistantConfigurationList{}
			_ = k8sClient.List(ctx, configList, &client.ListOptions{Namespace: namespace})
			for _, config := range configList.Items {
				_ = k8sClient.Delete(ctx, &config)
			}
			haList := &hav1.HomeAssistantList{}
			_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
			for _, ha := range haList.Items {
				_ = k8sClient.Delete(ctx, &ha)
			}
			cmList := &corev1.ConfigMapList{}
			_ = k8sClient.List(ctx, cmList, &client.ListOptions{Namespace: namespace})
			for _, cm := range cmList.Items {
				_ = k8sClient.Delete(ctx, &cm)
			}
			// envtest has no garbage collector, so owner-referenced NetworkPolicies
			// do not cascade-delete. Remove them explicitly for test isolation.
			npList := &networkingv1.NetworkPolicyList{}
			_ = k8sClient.List(ctx, npList, &client.ListOptions{Namespace: namespace})
			for _, np := range npList.Items {
				_ = k8sClient.Delete(ctx, &np)
			}
			Eventually(func() int {
				haList := &hav1.HomeAssistantList{}
				_ = k8sClient.List(ctx, haList, &client.ListOptions{Namespace: namespace})
				return len(haList.Items)
			}, timeout, interval).Should(Equal(0))
		})

		It("should not create NetworkPolicy when spec.alpha is unset", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: namespace},
				Spec:       hav1.HomeAssistantSpec{Version: "stable"},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: testName + "-config", Namespace: namespace},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{Name: testName},
					Configuration:    "homeassistant:\n  name: Test\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			reconciler := &HomeAssistantReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			np := &networkingv1.NetworkPolicy{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: testName, Namespace: namespace}, np)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})

		It("should create NetworkPolicy with owner reference when enabled", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: namespace},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
					Alpha: &hav1.AlphaSpec{
						NetworkPolicy: &hav1.NetworkPolicyAlphaSpec{Enabled: true},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: testName + "-config", Namespace: namespace},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{Name: testName},
					Configuration:    "homeassistant:\n  name: Test\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			reconciler := &HomeAssistantReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				np := &networkingv1.NetworkPolicy{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name: testName, Namespace: namespace,
				}, np)).To(Succeed())

				g.Expect(np.OwnerReferences).To(HaveLen(1))
				g.Expect(np.OwnerReferences[0].Kind).To(Equal("HomeAssistant"))
				g.Expect(np.OwnerReferences[0].Name).To(Equal(testName))

				g.Expect(np.Spec.PolicyTypes).To(ConsistOf(networkingv1.PolicyTypeIngress))
				g.Expect(np.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue(labelAppInstance, testName))
				g.Expect(np.Spec.Ingress).To(HaveLen(1))
				g.Expect(np.Spec.Ingress[0].Ports).To(HaveLen(1))
				g.Expect(np.Spec.Ingress[0].Ports[0].Port.IntValue()).To(Equal(8123))
			}, timeout, interval).Should(Succeed())
		})

		It("should include an operator-namespace ingress peer when OPERATOR_NAMESPACE is set", func() {
			prev, hadPrev := os.LookupEnv("OPERATOR_NAMESPACE")
			Expect(os.Setenv("OPERATOR_NAMESPACE", "homeassistant-operator-system")).To(Succeed())
			DeferCleanup(func() {
				if hadPrev {
					Expect(os.Setenv("OPERATOR_NAMESPACE", prev)).To(Succeed())
				} else {
					Expect(os.Unsetenv("OPERATOR_NAMESPACE")).To(Succeed())
				}
			})

			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: namespace},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
					Alpha: &hav1.AlphaSpec{
						NetworkPolicy: &hav1.NetworkPolicyAlphaSpec{Enabled: true},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: testName + "-config", Namespace: namespace},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{Name: testName},
					Configuration:    "homeassistant:\n  name: Test\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			reconciler := &HomeAssistantReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				np := &networkingv1.NetworkPolicy{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name: testName, Namespace: namespace,
				}, np)).To(Succeed())

				g.Expect(np.Spec.Ingress).To(HaveLen(1))
				peers := np.Spec.Ingress[0].From
				g.Expect(peers).To(HaveLen(2))

				g.Expect(peers).To(ContainElement(
					networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}},
				))
				g.Expect(peers).To(ContainElement(
					networkingv1.NetworkPolicyPeer{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								corev1.LabelMetadataName: "homeassistant-operator-system",
							},
						},
					},
				))
			}, timeout, interval).Should(Succeed())
		})

		It("should delete NetworkPolicy when toggled from enabled to disabled", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: namespace},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
					Alpha: &hav1.AlphaSpec{
						NetworkPolicy: &hav1.NetworkPolicyAlphaSpec{Enabled: true},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: testName + "-config", Namespace: namespace},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{Name: testName},
					Configuration:    "homeassistant:\n  name: Test\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			reconciler := &HomeAssistantReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name: testName, Namespace: namespace,
				}, &networkingv1.NetworkPolicy{})
			}, timeout, interval).Should(Succeed())

			// Toggle off — re-fetch before update to get current resourceVersion
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: testName, Namespace: namespace,
			}, ha)).To(Succeed())
			ha.Spec.Alpha.NetworkPolicy.Enabled = false
			Expect(k8sClient.Update(ctx, ha)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name: testName, Namespace: namespace,
				}, &networkingv1.NetworkPolicy{})
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})

		It("should not delete a pre-existing NetworkPolicy it does not own", func() {
			// A user-managed NetworkPolicy sharing the HA name must never be touched.
			foreign := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: namespace},
				Spec: networkingv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				},
			}
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())

			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: namespace},
				Spec: hav1.HomeAssistantSpec{
					Version: "stable",
					// Alpha unset → enabled == false → reconcile takes the delete path.
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())

			haConfig := &hav1.HomeAssistantConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: testName + "-config", Namespace: namespace},
				Spec: hav1.HomeAssistantConfigurationSpec{
					HomeAssistantRef: hav1.HomeAssistantReference{Name: testName},
					Configuration:    "homeassistant:\n  name: Test\n",
				},
			}
			Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

			reconciler := &HomeAssistantReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Foreign policy must still exist and carry no owner reference.
			fetched := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: testName, Namespace: namespace,
			}, fetched)).To(Succeed())
			Expect(fetched.OwnerReferences).To(BeEmpty())
		})
	})

	Context("unban script multi-document YAML parsing", func() {
		var script string

		BeforeEach(func() {
			if _, err := exec.LookPath("python3"); err != nil {
				Skip("python3 not available")
			}
			r := &HomeAssistantReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "script-parse-test", Namespace: "default"},
				Spec:       hav1.HomeAssistantSpec{Version: "2024.1.0"},
			}
			script = r.buildUnbanInitContainer(ha).Command[2]
		})

		runUnbanScript := func(operatorIP, fileContent string) (string, error) {
			f, err := os.CreateTemp("", "ip_bans_*.yaml")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = os.Remove(f.Name()) })
			_, err = f.WriteString(fileContent)
			Expect(err).NotTo(HaveOccurred())
			Expect(f.Close()).NotTo(HaveOccurred())
			cmd := exec.Command("python3", "-c", script)
			cmd.Env = append(os.Environ(), "OPERATOR_IP="+operatorIP, "UNBAN_IP_BANS_PATH="+f.Name())
			_, runErr := cmd.Output()
			data, readErr := os.ReadFile(f.Name())
			Expect(readErr).NotTo(HaveOccurred())
			return string(data), runErr
		}

		It("removes operator IP from multi-doc YAML produced by HA appending to {}", func() {
			// HA appends ban entries to a file containing '{}' without a '---' separator;
			// yaml.safe_load crashes on this input — verify the {} prefix is stripped first.
			content := "{}\n\n10.42.1.5:\n  banned_at: '2026-07-03T12:11:50+00:00'\n" +
				"1.2.3.4:\n  banned_at: '2026-07-03T09:00:00+00:00'\n"
			result, err := runUnbanScript("10.42.1.5", content)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).NotTo(ContainSubstring("10.42.1.5"))
			Expect(result).To(ContainSubstring("1.2.3.4"))
		})

		It("exits cleanly when file contains only {}", func() {
			// A file reset to '{}' with no bans must not cause a crash or modify the file.
			content := "{}"
			result, err := runUnbanScript("10.42.1.5", content)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal("{}"))
		})

		It("strips repeated {} prefixes before parsing", func() {
			// HA may append after multiple resets, producing {}\n{}\n<ip>: ...
			content := "{}\n{}\n10.42.1.5:\n  banned_at: '2026-07-03T12:11:50+00:00'\n"
			result, err := runUnbanScript("10.42.1.5", content)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).NotTo(ContainSubstring("10.42.1.5"))
		})

		It("exits cleanly on unparseable YAML content", func() {
			// Corrupted or otherwise invalid YAML must not crash the init-container.
			content := "key: [unclosed bracket\n"
			_, err := runUnbanScript("10.42.1.5", content)
			Expect(err).NotTo(HaveOccurred())
		})

		It("does not modify file when operator IP is not present", func() {
			// Verifies the ip-not-in-d early-exit: other IPs must remain untouched.
			content := "1.2.3.4:\n  banned_at: '2026-07-03T09:00:00+00:00'\n"
			result, err := runUnbanScript("10.42.1.5", content)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(content))
		})
	})

	Context("Unban init-container injection", func() {
		var reconciler *HomeAssistantReconciler

		BeforeEach(func() {
			reconciler = &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should include unban-operator-ip init-container when POD_IP is set", func() {
			prev, hadPrev := os.LookupEnv("POD_IP")
			Expect(os.Setenv("POD_IP", "10.42.1.99")).To(Succeed())
			DeferCleanup(func() {
				if hadPrev {
					Expect(os.Setenv("POD_IP", prev)).To(Succeed())
				} else {
					Expect(os.Unsetenv("POD_IP")).To(Succeed())
				}
			})

			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "unban-test", Namespace: "default"},
				Spec:       hav1.HomeAssistantSpec{Version: "2024.1.0"},
			}
			containers, err := reconciler.buildInitContainers(ctx, ha)
			Expect(err).NotTo(HaveOccurred())

			names := make([]string, len(containers))
			for i, c := range containers {
				names[i] = c.Name
			}
			Expect(names).To(ContainElement("unban-operator-ip"))
			Expect(names).To(ContainElement("config-init"))

			for _, c := range containers {
				if c.Name == "config-init" {
					Expect(c.Args).To(HaveLen(1))
					Expect(c.Args[0]).To(ContainSubstring("mkdir -p /config/python_scripts"),
						"python_script's own setup() never registers its reload service if "+
							"this directory is missing at HA's first boot")
				}
			}
		})

		It("should NOT include unban-operator-ip init-container when POD_IP is empty", func() {
			prev, hadPrev := os.LookupEnv("POD_IP")
			Expect(os.Unsetenv("POD_IP")).To(Succeed())
			DeferCleanup(func() {
				if hadPrev {
					Expect(os.Setenv("POD_IP", prev)).To(Succeed())
				}
			})

			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "unban-test-no-ip", Namespace: "default"},
				Spec:       hav1.HomeAssistantSpec{Version: "2024.1.0"},
			}
			containers, err := reconciler.buildInitContainers(ctx, ha)
			Expect(err).NotTo(HaveOccurred())

			for _, c := range containers {
				Expect(c.Name).NotTo(Equal("unban-operator-ip"))
			}
		})

		It("should use the HA image for unban init-container", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "unban-image-test", Namespace: "default"},
				Spec:       hav1.HomeAssistantSpec{Version: "2024.12.0"},
			}
			c := reconciler.buildUnbanInitContainer(ha)

			Expect(c.Image).To(ContainSubstring("home-assistant"))
			Expect(c.Image).To(ContainSubstring("2024.12.0"))
		})

		It("should read operator IP from ConfigMap env var (not hardcoded in script)", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "unban-cm-test", Namespace: "default"},
				Spec:       hav1.HomeAssistantSpec{Version: "2024.1.0"},
			}
			c := reconciler.buildUnbanInitContainer(ha)

			Expect(c.Command).To(ContainElement("python3"))
			script := c.Command[2]
			// Script must NOT contain any hardcoded IP; it reads OPERATOR_IP from env.
			Expect(script).To(ContainSubstring("OPERATOR_IP"))
			Expect(script).NotTo(MatchRegexp(`\d+\.\d+\.\d+\.\d+`))

			// Env var must source from the operator-IP ConfigMap.
			Expect(c.Env).To(HaveLen(1))
			Expect(c.Env[0].Name).To(Equal("OPERATOR_IP"))
			Expect(c.Env[0].ValueFrom).NotTo(BeNil())
			Expect(c.Env[0].ValueFrom.ConfigMapKeyRef).NotTo(BeNil())
			Expect(c.Env[0].ValueFrom.ConfigMapKeyRef.Name).To(Equal(ha.Name + operatorIPConfigMapSuffix))
			Expect(c.Env[0].ValueFrom.ConfigMapKeyRef.Key).To(Equal(operatorIPConfigMapKey))
		})

		It("should mount the config volume in unban init-container", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "unban-vol-test", Namespace: "default"},
				Spec:       hav1.HomeAssistantSpec{Version: "2024.1.0"},
			}
			c := reconciler.buildUnbanInitContainer(ha)

			Expect(c.VolumeMounts).To(HaveLen(1))
			Expect(c.VolumeMounts[0].Name).To(Equal("config"))
			Expect(c.VolumeMounts[0].MountPath).To(Equal("/config"))
		})
	})

	Context("Community repository sidecar/init-container injection", func() {
		var reconciler *HomeAssistantReconciler

		BeforeEach(func() {
			reconciler = &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		AfterEach(func() {
			list := &hav1alpha1.HomeAssistantCommunityRepositoryList{}
			_ = k8sClient.List(ctx, list, &client.ListOptions{Namespace: "default"})
			for i := range list.Items {
				_ = k8sClient.Delete(ctx, &list.Items[i])
			}
		})

		It("omits the sidecar/init-container/ConfigMap volume when no CommunityRepository targets this instance", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "cr-inject-none", Namespace: "default"},
				Spec:       hav1.HomeAssistantSpec{Version: "2024.1.0"},
			}

			initContainers, err := reconciler.buildInitContainers(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			for _, c := range initContainers {
				Expect(c.Name).NotTo(Equal("community-repository-init"))
			}

			sts, err := reconciler.buildStatefulSet(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			for _, c := range sts.Spec.Template.Spec.Containers {
				Expect(c.Name).NotTo(Equal("community-repository-sidecar"))
			}
			for _, v := range sts.Spec.Template.Spec.Volumes {
				Expect(v.Name).NotTo(Equal("community-repositories"))
			}
		})

		It("injects the sidecar/init-container/ConfigMap volume when a CommunityRepository targets this instance", func() {
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "cr-inject-some", Namespace: "default"},
				Spec:       hav1.HomeAssistantSpec{Version: "2024.1.0"},
			}

			repo := &hav1alpha1.HomeAssistantCommunityRepository{
				ObjectMeta: metav1.ObjectMeta{Name: "cr-inject-some-theme", Namespace: "default"},
				Spec: hav1alpha1.HomeAssistantCommunityRepositorySpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: ha.Name},
					Category:         hav1alpha1.CategoryTheme,
					Repository:       "acme/some-theme",
					Ref:              "v1.0.0",
				},
			}
			Expect(k8sClient.Create(ctx, repo)).To(Succeed())

			built, err := reconciler.buildInitContainers(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			initContainerNames := make([]string, 0, len(built))
			for _, c := range built {
				initContainerNames = append(initContainerNames, c.Name)
			}
			Expect(initContainerNames).To(ContainElement("community-repository-init"))

			sts, err := reconciler.buildStatefulSet(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			containerNames := make([]string, 0, len(sts.Spec.Template.Spec.Containers))
			for _, c := range sts.Spec.Template.Spec.Containers {
				containerNames = append(containerNames, c.Name)
			}
			Expect(containerNames).To(ContainElement("community-repository-sidecar"))

			volumeNames := make([]string, 0, len(sts.Spec.Template.Spec.Volumes))
			for _, v := range sts.Spec.Template.Spec.Volumes {
				volumeNames = append(volumeNames, v.Name)
			}
			Expect(volumeNames).To(ContainElement("community-repositories"))
		})
	})
})
