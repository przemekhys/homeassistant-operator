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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

var _ = Describe("Bootstrap Controller", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	ctx := context.Background()

	Context("Helper Functions", func() {
		It("getOrDefault should return value when not empty", func() {
			result := getOrDefault("custom", "default")
			Expect(result).To(Equal("custom"))
		})

		It("getOrDefault should return default when empty", func() {
			result := getOrDefault("", "default")
			Expect(result).To(Equal("default"))
		})

		It("buildHomeAssistantURL should build correct URL with default port", func() {
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha",
					Namespace: "default",
				},
			}

			url := reconciler.buildHomeAssistantURL(ha)
			Expect(url).To(Equal("http://test-ha.default.svc.cluster.local:8123"))
		})

		It("buildHomeAssistantURL should build correct URL with custom port", func() {
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha",
					Namespace: "production",
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Service: &hav1alpha1.ServiceSpec{
						Port: 9999,
					},
				},
			}

			url := reconciler.buildHomeAssistantURL(ha)
			Expect(url).To(Equal("http://test-ha.production.svc.cluster.local:9999"))
		})

		It("getApiTokenSecretName should return custom name when specified", func() {
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha",
					Namespace: "default",
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						ApiTokenSecretName: "my-custom-token-secret",
					},
				},
			}

			name := reconciler.getApiTokenSecretName(ha)
			Expect(name).To(Equal("my-custom-token-secret"))
		})

		It("getApiTokenSecretName should return default name when not specified", func() {
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha",
					Namespace: "default",
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{},
				},
			}

			name := reconciler.getApiTokenSecretName(ha)
			Expect(name).To(Equal("test-ha-homeassistant-api-token"))
		})

		It("getApiTokenSecretName should return default name when bootstrap is nil", func() {
			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-home",
					Namespace: "default",
				},
			}

			name := reconciler.getApiTokenSecretName(ha)
			Expect(name).To(Equal("my-home-homeassistant-api-token"))
		})
	})

	Context("validateBootstrapConfig", func() {
		var reconciler *HomeAssistantReconciler

		BeforeEach(func() {
			reconciler = &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should return error when credentials is nil", func() {
			ha := &hav1alpha1.HomeAssistant{
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Enabled:     true,
						Credentials: nil,
					},
				},
			}

			err := reconciler.validateBootstrapConfig(ha)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("secretRef is required"))
		})

		It("should return error when secretRef is nil", func() {
			ha := &hav1alpha1.HomeAssistant{
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Enabled:     true,
						Credentials: &hav1alpha1.BootstrapCredentials{},
					},
				},
			}

			err := reconciler.validateBootstrapConfig(ha)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("secretRef is required"))
		})

		It("should return error when secret name is empty", func() {
			ha := &hav1alpha1.HomeAssistant{
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Enabled: true,
						Credentials: &hav1alpha1.BootstrapCredentials{
							SecretRef: &hav1alpha1.CredentialsSecretRef{
								Name: "",
							},
						},
					},
				},
			}

			err := reconciler.validateBootstrapConfig(ha)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be empty"))
		})

		It("should return nil when config is valid", func() {
			ha := &hav1alpha1.HomeAssistant{
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Enabled: true,
						Credentials: &hav1alpha1.BootstrapCredentials{
							SecretRef: &hav1alpha1.CredentialsSecretRef{
								Name: "my-credentials",
							},
						},
					},
				},
			}

			err := reconciler.validateBootstrapConfig(ha)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("getBootstrapCredentials", func() {
		const namespace = "default"

		var reconciler *HomeAssistantReconciler

		BeforeEach(func() {
			reconciler = &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should return error when secret not found", func() {
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Credentials: &hav1alpha1.BootstrapCredentials{
							SecretRef: &hav1alpha1.CredentialsSecretRef{
								Name: "non-existent-secret",
							},
						},
					},
				},
			}

			_, _, err := reconciler.getBootstrapCredentials(ctx, ha)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})

		It("should return error when username key missing", func() {
			// Create secret without username
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-creds-no-user",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"password": []byte("secret123"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, secret)
			}()

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Credentials: &hav1alpha1.BootstrapCredentials{
							SecretRef: &hav1alpha1.CredentialsSecretRef{
								Name: "test-creds-no-user",
							},
						},
					},
				},
			}

			_, _, err := reconciler.getBootstrapCredentials(ctx, ha)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("username"))
		})

		It("should return error when password key missing", func() {
			// Create secret without password
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-creds-no-pass",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"username": []byte("admin"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, secret)
			}()

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Credentials: &hav1alpha1.BootstrapCredentials{
							SecretRef: &hav1alpha1.CredentialsSecretRef{
								Name: "test-creds-no-pass",
							},
						},
					},
				},
			}

			_, _, err := reconciler.getBootstrapCredentials(ctx, ha)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("password"))
		})

		It("should return credentials with default keys", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-creds-default",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("secret123"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, secret)
			}()

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Credentials: &hav1alpha1.BootstrapCredentials{
							SecretRef: &hav1alpha1.CredentialsSecretRef{
								Name: "test-creds-default",
							},
						},
					},
				},
			}

			username, password, err := reconciler.getBootstrapCredentials(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			Expect(username).To(Equal("admin"))
			Expect(password).To(Equal("secret123"))
		})

		It("should return credentials with custom keys", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-creds-custom",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"my-user": []byte("custom-admin"),
					"my-pass": []byte("custom-secret"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, secret)
			}()

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Credentials: &hav1alpha1.BootstrapCredentials{
							SecretRef: &hav1alpha1.CredentialsSecretRef{
								Name:        "test-creds-custom",
								UsernameKey: "my-user",
								PasswordKey: "my-pass",
							},
						},
					},
				},
			}

			username, password, err := reconciler.getBootstrapCredentials(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			Expect(username).To(Equal("custom-admin"))
			Expect(password).To(Equal("custom-secret"))
		})
	})

	Context("buildCoreConfigRequest", func() {
		var reconciler *HomeAssistantReconciler

		BeforeEach(func() {
			reconciler = &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should return nil when bootstrap is nil", func() {
			ha := &hav1alpha1.HomeAssistant{
				Spec: hav1alpha1.HomeAssistantSpec{},
			}

			req := reconciler.buildCoreConfigRequest(ha)
			Expect(req).To(BeNil())
		})

		It("should return nil when location is nil", func() {
			ha := &hav1alpha1.HomeAssistant{
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{},
				},
			}

			req := reconciler.buildCoreConfigRequest(ha)
			Expect(req).To(BeNil())
		})

		It("should build request with location name only", func() {
			ha := &hav1alpha1.HomeAssistant{
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Location: &hav1alpha1.LocationConfig{
							Name: "Home",
						},
					},
				},
			}

			req := reconciler.buildCoreConfigRequest(ha)
			Expect(req).NotTo(BeNil())
			Expect(req.LocationName).To(Equal("Home"))
			Expect(req.UnitSystem).To(Equal("metric")) // default
		})

		It("should build request with all location fields", func() {
			elevation := 100
			ha := &hav1alpha1.HomeAssistant{
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Location: &hav1alpha1.LocationConfig{
							Name:       "Warsaw",
							Latitude:   "52.2297",
							Longitude:  "21.0122",
							Elevation:  &elevation,
							UnitSystem: "metric",
							Currency:   "PLN",
							TimeZone:   "Europe/Warsaw",
						},
					},
				},
			}

			req := reconciler.buildCoreConfigRequest(ha)
			Expect(req).NotTo(BeNil())
			Expect(req.LocationName).To(Equal("Warsaw"))
			Expect(req.Latitude).To(BeNumerically("~", 52.2297, 0.0001))
			Expect(req.Longitude).To(BeNumerically("~", 21.0122, 0.0001))
			Expect(req.Elevation).To(Equal(100))
			Expect(req.UnitSystem).To(Equal("metric"))
			Expect(req.Currency).To(Equal("PLN"))
			Expect(req.TimeZone).To(Equal("Europe/Warsaw"))
		})

		It("should use spec.timezone when location.timeZone not set", func() {
			ha := &hav1alpha1.HomeAssistant{
				Spec: hav1alpha1.HomeAssistantSpec{
					Timezone: "America/New_York",
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Location: &hav1alpha1.LocationConfig{
							Name: "NYC",
						},
					},
				},
			}

			req := reconciler.buildCoreConfigRequest(ha)
			Expect(req).NotTo(BeNil())
			Expect(req.TimeZone).To(Equal("America/New_York"))
		})

		It("should prefer location.timeZone over spec.timezone", func() {
			ha := &hav1alpha1.HomeAssistant{
				Spec: hav1alpha1.HomeAssistantSpec{
					Timezone: "America/New_York",
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Location: &hav1alpha1.LocationConfig{
							Name:     "London",
							TimeZone: "Europe/London",
						},
					},
				},
			}

			req := reconciler.buildCoreConfigRequest(ha)
			Expect(req).NotTo(BeNil())
			Expect(req.TimeZone).To(Equal("Europe/London"))
		})
	})

	Context("handleBootstrapError", func() {
		const namespace = "default"

		var (
			reconciler    *HomeAssistantReconciler
			homeAssistant *hav1alpha1.HomeAssistant
		)

		BeforeEach(func() {
			reconciler = &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			homeAssistant = &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-bootstrap-err",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Enabled: true,
					},
				},
			}
			Expect(k8sClient.Create(ctx, homeAssistant)).To(Succeed())

			// Initialize bootstrap status
			homeAssistant.Status.Bootstrap = &hav1alpha1.BootstrapStatus{}
			Expect(k8sClient.Status().Update(ctx, homeAssistant)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, homeAssistant)
		})

		It("should set NotReady status for not ready error", func() {
			err := &haclient.Error{Type: haclient.ErrorTypeNotReady, Message: "HA not responding"}

			result, resultErr := reconciler.handleBootstrapError(ctx, homeAssistant, err)
			Expect(resultErr).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(bootstrapHealthCheckRetry))

			// Verify status was updated
			updatedHA := &hav1alpha1.HomeAssistant{}
			Eventually(func() string {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      homeAssistant.Name,
					Namespace: namespace,
				}, updatedHA); err != nil {
					return ""
				}
				if updatedHA.Status.Bootstrap == nil {
					return ""
				}
				return updatedHA.Status.Bootstrap.Message
			}, timeout, interval).Should(ContainSubstring("not ready"))
		})

		It("should set AlreadyDone status for onboarding done error", func() {
			err := &haclient.Error{Type: haclient.ErrorTypeOnboardingDone, Message: "already completed"}

			result, resultErr := reconciler.handleBootstrapError(ctx, homeAssistant, err)
			Expect(resultErr).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero()) // No requeue when completed

			// Verify status was updated
			updatedHA := &hav1alpha1.HomeAssistant{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      homeAssistant.Name,
					Namespace: namespace,
				}, updatedHA); err != nil {
					return false
				}
				if updatedHA.Status.Bootstrap == nil {
					return false
				}
				return updatedHA.Status.Bootstrap.Completed
			}, timeout, interval).Should(BeTrue())
		})

		It("should set Failed status for other errors", func() {
			err := &haclient.Error{Type: haclient.ErrorTypeHTTP, Message: "connection refused"}

			result, resultErr := reconciler.handleBootstrapError(ctx, homeAssistant, err)
			Expect(resultErr).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(bootstrapRetryInterval))

			// Verify status was updated
			updatedHA := &hav1alpha1.HomeAssistant{}
			Eventually(func() string {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      homeAssistant.Name,
					Namespace: namespace,
				}, updatedHA); err != nil {
					return ""
				}
				if updatedHA.Status.Bootstrap == nil {
					return ""
				}
				return updatedHA.Status.Bootstrap.Message
			}, timeout, interval).Should(ContainSubstring("Bootstrap failed"))
		})
	})

	Context("createAPITokenSecret", func() {
		const namespace = "default"

		var reconciler *HomeAssistantReconciler

		BeforeEach(func() {
			reconciler = &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should create new secret with token", func() {
			haName := "test-ha-token-create"

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						ApiTokenSecretName: "my-api-token",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, ha)
			}()

			err := reconciler.createAPITokenSecret(ctx, ha, "test-token-value")
			Expect(err).NotTo(HaveOccurred())

			// Verify secret was created
			secret := &corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "my-api-token",
					Namespace: namespace,
				}, secret)
			}, timeout, interval).Should(Succeed())

			Expect(secret.Data).To(HaveKey("token"))
			Expect(string(secret.Data["token"])).To(Equal("test-token-value"))
			Expect(secret.OwnerReferences).To(HaveLen(1))
			Expect(secret.OwnerReferences[0].Name).To(Equal(haName))

			// Cleanup
			_ = k8sClient.Delete(ctx, secret)
		})

		It("should update existing secret", func() {
			haName := "test-ha-token-update"

			// Create existing secret
			existingSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "update-api-token",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"token": []byte("old-token"),
				},
			}
			Expect(k8sClient.Create(ctx, existingSecret)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, existingSecret)
			}()

			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      haName,
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						ApiTokenSecretName: "update-api-token",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, ha)
			}()

			err := reconciler.createAPITokenSecret(ctx, ha, "new-token-value")
			Expect(err).NotTo(HaveOccurred())

			// Verify secret was updated
			updatedSecret := &corev1.Secret{}
			Eventually(func() string {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "update-api-token",
					Namespace: namespace,
				}, updatedSecret); err != nil {
					return ""
				}
				return string(updatedSecret.Data["token"])
			}, timeout, interval).Should(Equal("new-token-value"))
		})
	})

	Context("reconcileBootstrap", func() {
		const namespace = "default"

		It("should return immediately when bootstrap is disabled", func() {
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-no-bootstrap",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: nil, // Bootstrap disabled
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, ha)
			}()

			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			result, err := reconciler.reconcileBootstrap(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})

		It("should return immediately when bootstrap already completed", func() {
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-bootstrap-done",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Enabled: true,
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, ha)
			}()

			// Update status separately (status is a subresource)
			ha.Status.Bootstrap = &hav1alpha1.BootstrapStatus{
				Completed: true,
			}
			Expect(k8sClient.Status().Update(ctx, ha)).To(Succeed())

			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			result, err := reconciler.reconcileBootstrap(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})

		It("should fail with missing credentials", func() {
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-bootstrap-no-creds",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Enabled:     true,
						Credentials: nil, // Missing credentials
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, ha)
			}()

			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			result, err := reconciler.reconcileBootstrap(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(bootstrapRetryInterval))

			// Verify condition was set
			updatedHA := &hav1alpha1.HomeAssistant{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      ha.Name,
					Namespace: namespace,
				}, updatedHA); err != nil {
					return false
				}
				return meta.IsStatusConditionFalse(updatedHA.Status.Conditions, "BootstrapReady")
			}, timeout, interval).Should(BeTrue())
		})

		It("should fail when credentials secret not found", func() {
			ha := &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-bootstrap-no-secret",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Enabled: true,
						Credentials: &hav1alpha1.BootstrapCredentials{
							SecretRef: &hav1alpha1.CredentialsSecretRef{
								Name: "non-existent-credentials",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, ha)
			}()

			reconciler := &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			result, err := reconciler.reconcileBootstrap(ctx, ha)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(bootstrapRetryInterval))

			// Verify condition was set
			updatedHA := &hav1alpha1.HomeAssistant{}
			Eventually(func() string {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      ha.Name,
					Namespace: namespace,
				}, updatedHA); err != nil {
					return ""
				}
				cond := meta.FindStatusCondition(updatedHA.Status.Conditions, "BootstrapReady")
				if cond == nil {
					return ""
				}
				return cond.Message
			}, timeout, interval).Should(ContainSubstring("not found"))
		})
	})

	Context("updateBootstrapStatus", func() {
		const namespace = "default"

		var (
			reconciler    *HomeAssistantReconciler
			homeAssistant *hav1alpha1.HomeAssistant
		)

		BeforeEach(func() {
			reconciler = &HomeAssistantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			homeAssistant = &hav1alpha1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ha-status-update",
					Namespace: namespace,
				},
				Spec: hav1alpha1.HomeAssistantSpec{
					Bootstrap: &hav1alpha1.BootstrapSpec{
						Enabled: true,
					},
				},
			}
			Expect(k8sClient.Create(ctx, homeAssistant)).To(Succeed())

			// Initialize bootstrap status
			homeAssistant.Status.Bootstrap = &hav1alpha1.BootstrapStatus{}
			Expect(k8sClient.Status().Update(ctx, homeAssistant)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, homeAssistant)
		})

		It("should set BootstrapReady condition to True when completed", func() {
			result, err := reconciler.updateBootstrapStatus(ctx, homeAssistant, reasonBootstrapCompleted, "Done", true, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			// Verify condition
			updatedHA := &hav1alpha1.HomeAssistant{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      homeAssistant.Name,
					Namespace: namespace,
				}, updatedHA); err != nil {
					return false
				}
				return meta.IsStatusConditionTrue(updatedHA.Status.Conditions, "BootstrapReady")
			}, timeout, interval).Should(BeTrue())

			// Verify bootstrap status fields
			Expect(updatedHA.Status.Bootstrap.Completed).To(BeTrue())
			Expect(updatedHA.Status.Bootstrap.ApiTokenReady).To(BeTrue())
			Expect(updatedHA.Status.Bootstrap.LastAttempt).NotTo(BeNil())
		})

		It("should set BootstrapReady condition to False when not completed", func() {
			result, err := reconciler.updateBootstrapStatus(ctx, homeAssistant, reasonBootstrapNotReady, "Not ready", false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(bootstrapHealthCheckRetry))

			// Verify condition
			updatedHA := &hav1alpha1.HomeAssistant{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      homeAssistant.Name,
					Namespace: namespace,
				}, updatedHA); err != nil {
					return false
				}
				return meta.IsStatusConditionFalse(updatedHA.Status.Conditions, "BootstrapReady")
			}, timeout, interval).Should(BeTrue())
		})

		It("should set ApiTokenReady only when tokenCreated is true", func() {
			// completed=true, tokenCreated=false (e.g., onboarding already done)
			_, err := reconciler.updateBootstrapStatus(ctx, homeAssistant, reasonBootstrapAlreadyDone, "Already done", true, false)
			Expect(err).NotTo(HaveOccurred())

			// Verify ApiTokenReady is false
			updatedHA := &hav1alpha1.HomeAssistant{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      homeAssistant.Name,
					Namespace: namespace,
				}, updatedHA); err != nil {
					return false
				}
				return updatedHA.Status.Bootstrap.Completed
			}, timeout, interval).Should(BeTrue())

			Expect(updatedHA.Status.Bootstrap.ApiTokenReady).To(BeFalse())
		})
	})
})
