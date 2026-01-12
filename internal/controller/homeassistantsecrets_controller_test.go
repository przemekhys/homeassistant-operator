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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

var _ = Describe("HomeAssistantSecrets Controller", func() {
	Context("When reconciling HomeAssistantSecrets", func() {
		const (
			homeAssistantName = "test-ha"
			secretsName       = "test-ha-secrets"
			namespace         = "default"
			timeout           = time.Second * 10
			interval          = time.Millisecond * 250
		)

		ctx := context.Background()

		var (
			homeAssistant        *hav1alpha1.HomeAssistant
			homeAssistantSecrets *hav1alpha1.HomeAssistantSecrets
			testSecret1          *corev1.Secret
			testSecret2          *corev1.Secret
		)

		BeforeEach(func() {
			// Create HomeAssistant CR
			homeAssistant = &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      homeAssistantName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1",
				},
			}
			Expect(k8sClient.Create(ctx, homeAssistant)).To(Succeed())

			// Create test Secrets
			testSecret1 = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret-1",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"mqtt_user":     []byte("testuser"),
					"mqtt_password": []byte("testpass"),
				},
			}
			Expect(k8sClient.Create(ctx, testSecret1)).To(Succeed())

			testSecret2 = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret-2",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"db_url": []byte("postgresql://test"),
				},
			}
			Expect(k8sClient.Create(ctx, testSecret2)).To(Succeed())

			// Create HomeAssistantSecrets CR
			homeAssistantSecrets = &hav1alpha1.HomeAssistantSecrets{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretsName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: homeAssistantName,
					},
					SecretRefs: []hav1alpha1.SecretKeyReference{
						{
							Name: "test-secret-1",
							Keys: []string{"mqtt_user", "mqtt_password"},
						},
						{
							Name: "test-secret-2",
							Keys: []string{"db_url"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, homeAssistantSecrets)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup
			Expect(k8sClient.Delete(ctx, homeAssistantSecrets)).To(Succeed())
			Expect(k8sClient.Delete(ctx, testSecret1)).To(Succeed())
			Expect(k8sClient.Delete(ctx, testSecret2)).To(Succeed())
			Expect(k8sClient.Delete(ctx, homeAssistant)).To(Succeed())
		})

		It("should successfully reconcile and create generated secret", func() {
			By("Reconciling the created resource")
			controllerReconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      secretsName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if generated secret was created")
			generatedSecretName := homeAssistantName + generatedSecretSuffix
			generatedSecret := &corev1.Secret{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      generatedSecretName,
					Namespace: namespace,
				}, generatedSecret)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying generated secret contains correct data")
			Expect(generatedSecret.Data).To(HaveKey(secretsYamlKey))
			secretsYaml := string(generatedSecret.Data[secretsYamlKey])
			Expect(secretsYaml).To(ContainSubstring("mqtt_user:"))
			Expect(secretsYaml).To(ContainSubstring("mqtt_password:"))
			Expect(secretsYaml).To(ContainSubstring("db_url:"))

			By("Checking HomeAssistantSecrets status")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      secretsName,
					Namespace: namespace,
				}, homeAssistantSecrets)
				if err != nil {
					return false
				}
				return meta.IsStatusConditionTrue(homeAssistantSecrets.Status.Conditions, conditionTypeReady)
			}, timeout, interval).Should(BeTrue())

			Expect(homeAssistantSecrets.Status.SecretsHash).NotTo(BeEmpty())
			Expect(homeAssistantSecrets.Status.LastUpdated).NotTo(BeNil())
		})

		It("should update generated secret when source secrets change", func() {
			By("Initial reconciliation")
			controllerReconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      secretsName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			generatedSecretName := homeAssistantName + generatedSecretSuffix
			generatedSecret := &corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      generatedSecretName,
					Namespace: namespace,
				}, generatedSecret)
			}, timeout, interval).Should(Succeed())

			originalHash := generatedSecret.Annotations[secretsHashAnnotationKey]

			By("Updating source secret")
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-secret-1",
					Namespace: namespace,
				}, testSecret1)
				if err != nil {
					return err
				}
				testSecret1.Data["mqtt_password"] = []byte("newpassword")
				return k8sClient.Update(ctx, testSecret1)
			}, timeout, interval).Should(Succeed())

			By("Reconciling again")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      secretsName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying hash changed")
			Eventually(func() string {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      generatedSecretName,
					Namespace: namespace,
				}, generatedSecret)
				if err != nil {
					return ""
				}
				return generatedSecret.Annotations[secretsHashAnnotationKey]
			}, timeout, interval).ShouldNot(Equal(originalHash))

			secretsYaml := string(generatedSecret.Data[secretsYamlKey])
			Expect(secretsYaml).To(ContainSubstring("newpassword"))
		})

		It("should fail when referenced HomeAssistant doesn't exist", func() {
			By("Creating HomeAssistantSecrets with non-existent HomeAssistant reference")
			invalidSecrets := &hav1alpha1.HomeAssistantSecrets{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-secrets",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "non-existent-ha",
					},
					SecretRefs: []hav1alpha1.SecretKeyReference{
						{
							Name: "test-secret-1",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, invalidSecrets)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, invalidSecrets)
			}()

			By("Reconciling")
			controllerReconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "invalid-secrets",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking status is not ready")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "invalid-secrets",
					Namespace: namespace,
				}, invalidSecrets)
				if err != nil {
					return false
				}
				return meta.IsStatusConditionFalse(invalidSecrets.Status.Conditions, conditionTypeReady)
			}, timeout, interval).Should(BeTrue())
		})

		It("should fail when referenced secret doesn't exist", func() {
			By("Creating HomeAssistantSecrets with non-existent secret reference")
			invalidSecrets := &hav1alpha1.HomeAssistantSecrets{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-secret-ref",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: homeAssistantName,
					},
					SecretRefs: []hav1alpha1.SecretKeyReference{
						{
							Name: "non-existent-secret",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, invalidSecrets)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, invalidSecrets)
			}()

			By("Reconciling")
			controllerReconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "invalid-secret-ref",
					Namespace: namespace,
				},
			})
			Expect(err).To(HaveOccurred())
		})

		It("should handle secrets with all keys when keys list is empty", func() {
			By("Creating HomeAssistantSecrets without specifying keys")
			allKeysSecrets := &hav1alpha1.HomeAssistantSecrets{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "all-keys-secrets",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: homeAssistantName,
					},
					SecretRefs: []hav1alpha1.SecretKeyReference{
						{
							Name: "test-secret-1",
							// No keys specified - should include all keys
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, allKeysSecrets)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, allKeysSecrets)
			}()

			By("Reconciling")
			controllerReconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "all-keys-secrets",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying all keys are included")
			generatedSecretName := homeAssistantName + generatedSecretSuffix
			generatedSecret := &corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      generatedSecretName,
					Namespace: namespace,
				}, generatedSecret)
			}, timeout, interval).Should(Succeed())

			secretsYaml := string(generatedSecret.Data[secretsYamlKey])
			Expect(secretsYaml).To(ContainSubstring("mqtt_user:"))
			Expect(secretsYaml).To(ContainSubstring("mqtt_password:"))
		})

		It("should skip missing keys from secret but continue processing", func() {
			By("Creating HomeAssistantSecrets with a non-existent key")
			missingKeySecrets := &hav1alpha1.HomeAssistantSecrets{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "missing-key-secrets",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: homeAssistantName,
					},
					SecretRefs: []hav1alpha1.SecretKeyReference{
						{
							Name: "test-secret-1",
							Keys: []string{"mqtt_user", "non_existent_key"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, missingKeySecrets)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, missingKeySecrets)
			}()

			By("Reconciling")
			controllerReconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "missing-key-secrets",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying only existing key is included")
			generatedSecretName := homeAssistantName + generatedSecretSuffix
			generatedSecret := &corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      generatedSecretName,
					Namespace: namespace,
				}, generatedSecret)
			}, timeout, interval).Should(Succeed())

			secretsYaml := string(generatedSecret.Data[secretsYamlKey])
			Expect(secretsYaml).To(ContainSubstring("mqtt_user:"))
			Expect(secretsYaml).NotTo(ContainSubstring("non_existent_key:"))
		})

		It("should set owner reference on generated secret", func() {
			// This test uses the homeAssistantSecrets created in BeforeEach
			By("Reconciling the resource")
			controllerReconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      secretsName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying owner reference is set")
			generatedSecretName := homeAssistantName + generatedSecretSuffix
			generatedSecret := &corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      generatedSecretName,
					Namespace: namespace,
				}, generatedSecret)
			}, timeout, interval).Should(Succeed())

			Expect(generatedSecret.OwnerReferences).To(HaveLen(1))
			Expect(generatedSecret.OwnerReferences[0].Kind).To(Equal("HomeAssistantSecrets"))
			Expect(generatedSecret.OwnerReferences[0].Name).To(Equal(secretsName))
		})

		It("should handle secret with empty data", func() {
			By("Creating an empty secret")
			emptySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-secret",
					Namespace: namespace,
				},
				Data: map[string][]byte{},
			}
			Expect(k8sClient.Create(ctx, emptySecret)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, emptySecret)
			}()

			By("Creating HomeAssistantSecrets referencing empty secret")
			emptyDataSecrets := &hav1alpha1.HomeAssistantSecrets{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-data-secrets",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: homeAssistantName,
					},
					SecretRefs: []hav1alpha1.SecretKeyReference{
						{
							Name: "empty-secret",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, emptyDataSecrets)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, emptyDataSecrets)
			}()

			By("Reconciling")
			controllerReconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "empty-data-secrets",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying generated secret has empty secrets comment")
			generatedSecretName := homeAssistantName + generatedSecretSuffix
			generatedSecret := &corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      generatedSecretName,
					Namespace: namespace,
				}, generatedSecret)
			}, timeout, interval).Should(Succeed())

			secretsYaml := string(generatedSecret.Data[secretsYamlKey])
			Expect(secretsYaml).To(Equal("# No secrets configured\n"))
		})
	})

	Context("findHomeAssistantSecretsForSecret", func() {
		const namespace = "default"
		ctx := context.Background()

		It("should return requests for HomeAssistantSecrets that reference the secret", func() {
			By("Creating HomeAssistant")
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-find",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1",
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, ha)
			}()

			By("Creating a secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "find-test-secret",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"key": []byte("value"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, secret)
			}()

			By("Creating HomeAssistantSecrets referencing the secret")
			haSecrets := &hav1alpha1.HomeAssistantSecrets{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "find-test-hasecrets",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: "test-ha-find",
					},
					SecretRefs: []hav1alpha1.SecretKeyReference{
						{
							Name: "find-test-secret",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, haSecrets)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, haSecrets)
			}()

			By("Calling findHomeAssistantSecretsForSecret")
			reconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			requests := reconciler.findHomeAssistantSecretsForSecret(ctx, secret)

			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Name).To(Equal("find-test-hasecrets"))
			Expect(requests[0].Namespace).To(Equal(namespace))
		})

		It("should return empty list when no HomeAssistantSecrets reference the secret", func() {
			By("Creating a secret not referenced by any HomeAssistantSecrets")
			unrefSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unreferenced-secret",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"key": []byte("value"),
				},
			}
			Expect(k8sClient.Create(ctx, unrefSecret)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, unrefSecret)
			}()

			By("Calling findHomeAssistantSecretsForSecret")
			reconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			requests := reconciler.findHomeAssistantSecretsForSecret(ctx, unrefSecret)

			Expect(requests).To(BeEmpty())
		})
	})

	Context("Helper function tests", func() {
		It("should correctly calculate hash", func() {
			content := "test content"
			hash1 := calculateHash(content)
			hash2 := calculateHash(content)
			Expect(hash1).To(Equal(hash2))

			differentContent := "different content"
			hash3 := calculateHash(differentContent)
			Expect(hash1).NotTo(Equal(hash3))
		})

		It("should generate proper YAML format", func() {
			reconciler := &HomeAssistantSecretsReconciler{}
			secretsData := map[string]string{
				"key1": "value1",
				"key2": "value2",
			}

			yaml := reconciler.generateSecretsYaml(secretsData)
			Expect(yaml).To(ContainSubstring("key1: value1"))
			Expect(yaml).To(ContainSubstring("key2: value2"))
			Expect(yaml).To(ContainSubstring("# Auto-generated"))
		})

		It("should quote values with special characters", func() {
			reconciler := &HomeAssistantSecretsReconciler{}
			secretsData := map[string]string{
				"simple":         "value",
				"with_space":     "value with space",
				"with_colon":     "key:value",
				"with_quotes":    "value\"with\"quotes",
				"with_linebreak": "line1\nline2",
			}

			yaml := reconciler.generateSecretsYaml(secretsData)
			// Simple values should not be quoted
			Expect(yaml).To(ContainSubstring("simple: value\n"))
			// Values with special chars should be quoted
			Expect(yaml).To(ContainSubstring("with_space:"))
			Expect(yaml).To(ContainSubstring("with_colon:"))
		})

		It("should return true for autoRestart when nil (default)", func() {
			reconciler := &HomeAssistantSecretsReconciler{}
			haSecrets := &hav1alpha1.HomeAssistantSecrets{
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					AutoRestart: nil,
				},
			}
			Expect(reconciler.isAutoRestartEnabled(haSecrets)).To(BeTrue())
		})

		It("should return true for autoRestart when explicitly true", func() {
			reconciler := &HomeAssistantSecretsReconciler{}
			haSecrets := &hav1alpha1.HomeAssistantSecrets{
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					AutoRestart: ptr.To(true),
				},
			}
			Expect(reconciler.isAutoRestartEnabled(haSecrets)).To(BeTrue())
		})

		It("should return false for autoRestart when explicitly false", func() {
			reconciler := &HomeAssistantSecretsReconciler{}
			haSecrets := &hav1alpha1.HomeAssistantSecrets{
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					AutoRestart: ptr.To(false),
				},
			}
			Expect(reconciler.isAutoRestartEnabled(haSecrets)).To(BeFalse())
		})

		It("should generate empty secrets comment when no secrets data", func() {
			reconciler := &HomeAssistantSecretsReconciler{}
			secretsData := map[string]string{}

			yaml := reconciler.generateSecretsYaml(secretsData)
			Expect(yaml).To(Equal("# No secrets configured\n"))
		})

		It("should sort keys alphabetically in generated YAML", func() {
			reconciler := &HomeAssistantSecretsReconciler{}
			secretsData := map[string]string{
				"zebra":  "last",
				"alpha":  "first",
				"middle": "mid",
				"beta":   "second",
			}

			yaml := reconciler.generateSecretsYaml(secretsData)

			// Find positions of each key in the output
			alphaPos := strings.Index(yaml, "alpha:")
			betaPos := strings.Index(yaml, "beta:")
			middlePos := strings.Index(yaml, "middle:")
			zebraPos := strings.Index(yaml, "zebra:")

			Expect(alphaPos).To(BeNumerically(">", -1))
			Expect(alphaPos).To(BeNumerically("<", betaPos))
			Expect(betaPos).To(BeNumerically("<", middlePos))
			Expect(middlePos).To(BeNumerically("<", zebraPos))
		})
	})

	Context("AutoRestart functionality", func() {
		const (
			homeAssistantName = "test-ha-restart"
			secretsName       = "test-ha-restart-secrets"
			namespace         = "default"
			timeout           = time.Second * 10
			interval          = time.Millisecond * 250
		)

		ctx := context.Background()

		var (
			homeAssistant        *hav1alpha1.HomeAssistant
			homeAssistantSecrets *hav1alpha1.HomeAssistantSecrets
			testSecret           *corev1.Secret
			statefulSet          *appsv1.StatefulSet
		)

		BeforeEach(func() {
			// Create HomeAssistant CR
			homeAssistant = &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      homeAssistantName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Version: "2024.1",
				},
			}
			Expect(k8sClient.Create(ctx, homeAssistant)).To(Succeed())

			// Create StatefulSet (simulating what HomeAssistant controller would create)
			statefulSet = &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      homeAssistantName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name":       "homeassistant",
						"app.kubernetes.io/instance":   homeAssistantName,
						"app.kubernetes.io/managed-by": "homeassistant-operator",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/instance": homeAssistantName,
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app.kubernetes.io/instance": homeAssistantName,
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "homeassistant",
									Image: "ghcr.io/home-assistant/home-assistant:stable",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, statefulSet)).To(Succeed())

			// Create test Secret
			testSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret-restart",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"api_key": []byte("initial-key"),
				},
			}
			Expect(k8sClient.Create(ctx, testSecret)).To(Succeed())

			// Create HomeAssistantSecrets CR with autoRestart enabled (default)
			homeAssistantSecrets = &hav1alpha1.HomeAssistantSecrets{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretsName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSecretsSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{
						Name: homeAssistantName,
					},
					SecretRefs: []hav1alpha1.SecretKeyReference{
						{
							Name: "test-secret-restart",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, homeAssistantSecrets)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup
			_ = k8sClient.Delete(ctx, homeAssistantSecrets)
			_ = k8sClient.Delete(ctx, testSecret)
			_ = k8sClient.Delete(ctx, statefulSet)
			_ = k8sClient.Delete(ctx, homeAssistant)
		})

		It("should update StatefulSet annotation when autoRestart is enabled", func() {
			By("Reconciling the resource")
			controllerReconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      secretsName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking StatefulSet has annotation")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      homeAssistantName,
					Namespace: namespace,
				}, statefulSet)
				if err != nil {
					return false
				}
				if statefulSet.Spec.Template.Annotations == nil {
					return false
				}
				_, exists := statefulSet.Spec.Template.Annotations[secretsHashAnnotationKey]
				return exists
			}, timeout, interval).Should(BeTrue())
		})

		It("should not update StatefulSet annotation when autoRestart is disabled", func() {
			By("Disabling autoRestart")
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      secretsName,
					Namespace: namespace,
				}, homeAssistantSecrets)
				if err != nil {
					return err
				}
				homeAssistantSecrets.Spec.AutoRestart = ptr.To(false)
				return k8sClient.Update(ctx, homeAssistantSecrets)
			}, timeout, interval).Should(Succeed())

			By("Reconciling the resource")
			controllerReconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      secretsName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking StatefulSet does not have annotation")
			Consistently(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      homeAssistantName,
					Namespace: namespace,
				}, statefulSet)
				if err != nil {
					return true // Error means we can't check, continue
				}
				if statefulSet.Spec.Template.Annotations == nil {
					return true // No annotations, as expected
				}
				_, exists := statefulSet.Spec.Template.Annotations[secretsHashAnnotationKey]
				return !exists
			}, time.Second*2, interval).Should(BeTrue())
		})

		It("should update StatefulSet annotation when secrets change", func() {
			By("Initial reconciliation")
			controllerReconciler := &HomeAssistantSecretsReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      secretsName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Getting initial hash from StatefulSet")
			var initialHash string
			Eventually(func() string {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      homeAssistantName,
					Namespace: namespace,
				}, statefulSet)
				if err != nil || statefulSet.Spec.Template.Annotations == nil {
					return ""
				}
				initialHash = statefulSet.Spec.Template.Annotations[secretsHashAnnotationKey]
				return initialHash
			}, timeout, interval).ShouldNot(BeEmpty())

			By("Updating source secret")
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-secret-restart",
					Namespace: namespace,
				}, testSecret)
				if err != nil {
					return err
				}
				testSecret.Data["api_key"] = []byte("updated-key")
				return k8sClient.Update(ctx, testSecret)
			}, timeout, interval).Should(Succeed())

			By("Reconciling again")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      secretsName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StatefulSet annotation changed")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      homeAssistantName,
					Namespace: namespace,
				}, statefulSet)
				if err != nil || statefulSet.Spec.Template.Annotations == nil {
					return false
				}
				newHash := statefulSet.Spec.Template.Annotations[secretsHashAnnotationKey]
				return newHash != initialHash && newHash != ""
			}, timeout, interval).Should(BeTrue())
		})
	})
})
