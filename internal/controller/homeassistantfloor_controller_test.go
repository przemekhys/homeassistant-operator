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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

var _ = Describe("HomeAssistantFloor Controller", func() {
	var (
		ctx        context.Context
		ns         string
		haName     string
		mockServer *httptest.Server
		upgrader   = websocket.Upgrader{}
		floors     []map[string]interface{}
	)

	cleanupHA := func() {
		ha := &hav1alpha1.HomeAssistant{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: ns}, ha); err == nil {
			_ = k8sClient.Delete(ctx, ha)
		}
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName + "-api-token", Namespace: ns}, secret); err == nil {
			_ = k8sClient.Delete(ctx, secret)
		}
	}

	setupHA := func() {
		ha := &hav1alpha1.HomeAssistant{
			ObjectMeta: metav1.ObjectMeta{Name: haName, Namespace: ns},
			Spec: hav1alpha1.HomeAssistantSpec{
				Version: "2026.3",
			},
		}
		Expect(k8sClient.Create(ctx, ha)).To(Succeed())

		// Set bootstrap status with token secret name
		ha.Status.Phase = "Running"
		ha.Status.Bootstrap = &hav1alpha1.BootstrapStatus{
			APITokenSecretName: haName + "-api-token",
		}
		Expect(k8sClient.Status().Update(ctx, ha)).To(Succeed())

		// Create token secret
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      haName + "-api-token",
				Namespace: ns,
			},
			Data: map[string][]byte{
				"token": []byte("test-token"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	}

	setupMockWS := func() {
		mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()

			// Auth flow
			_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
			var authMsg map[string]interface{}
			_ = conn.ReadJSON(&authMsg)
			_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

			// Read command
			var cmd map[string]interface{}
			_ = conn.ReadJSON(&cmd)

			cmdType, _ := cmd["type"].(string)
			switch cmdType {
			case "config/floor_registry/list":
				_ = conn.WriteJSON(map[string]interface{}{
					"id": 1, "type": "result", "success": true,
					"result": floors,
				})
			case "config/floor_registry/create":
				created := map[string]interface{}{
					"floor_id": "new-floor-id",
					"name":     cmd["name"],
					"level":    cmd["level"],
					"icon":     cmd["icon"],
				}
				floors = append(floors, created)
				_ = conn.WriteJSON(map[string]interface{}{
					"id": 1, "type": "result", "success": true,
					"result": created,
				})
			case "config/floor_registry/update":
				_ = conn.WriteJSON(map[string]interface{}{
					"id": 1, "type": "result", "success": true,
					"result": map[string]interface{}{
						"floor_id": cmd["floor_id"],
						"name":     cmd["name"],
					},
				})
			case "config/floor_registry/delete":
				_ = conn.WriteJSON(map[string]interface{}{
					"id": 1, "type": "result", "success": true,
					"result": nil,
				})
			}
		}))
	}

	BeforeEach(func() {
		ctx = context.Background()
		ns = "default"
		haName = "test-ha-floor"
		floors = nil
	})

	AfterEach(func() {
		if mockServer != nil {
			mockServer.Close()
		}
		cleanupHA()
	})

	Context("When HomeAssistant is not found", func() {
		It("should set HANotReady condition", func() {
			floor := &hav1alpha1.HomeAssistantFloor{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "floor-no-ha",
					Namespace: ns,
				},
				Spec: hav1alpha1.HomeAssistantFloorSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: "nonexistent"},
					Name:             "Ground",
				},
			}
			Expect(k8sClient.Create(ctx, floor)).To(Succeed())
			defer func() {
				// Remove finalizer for cleanup
				f := &hav1alpha1.HomeAssistantFloor{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: floor.Name, Namespace: ns}, f)
				f.Finalizers = nil
				_ = k8sClient.Update(ctx, f)
				_ = k8sClient.Delete(ctx, f)
			}()

			reconciler := &HomeAssistantFloorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: floor.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile checks HA ref
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: floor.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			// Verify condition
			updated := &hav1alpha1.HomeAssistantFloor{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: floor.Name, Namespace: ns}, updated)).To(Succeed())
			Expect(updated.Status.Conditions).To(HaveLen(1))
			Expect(updated.Status.Conditions[0].Reason).To(Equal("HANotReady"))
		})
	})

	Context("When creating a floor via WebSocket", func() {
		It("should create floor and set Ready condition", func() {
			setupHA()
			setupMockWS()

			floor := &hav1alpha1.HomeAssistantFloor{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ground-floor",
					Namespace: ns,
				},
				Spec: hav1alpha1.HomeAssistantFloorSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Name:             "Ground Floor",
					Level:            intPtr(0),
					Icon:             "mdi:home-floor-0",
				},
			}
			Expect(k8sClient.Create(ctx, floor)).To(Succeed())
			defer func() {
				f := &hav1alpha1.HomeAssistantFloor{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: floor.Name, Namespace: ns}, f)
				f.Finalizers = nil
				_ = k8sClient.Update(ctx, f)
				_ = k8sClient.Delete(ctx, f)
			}()

			wsURL := "ws" + strings.TrimPrefix(mockServer.URL, "http")
			reconciler := &HomeAssistantFloorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				NewHAClient: func(_ string) *haclient.Client {
					return haclient.NewClient(wsURL)
				},
			}

			// First reconcile: add finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: floor.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile: create floor
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: floor.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify
			updated := &hav1alpha1.HomeAssistantFloor{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: floor.Name, Namespace: ns}, updated)).To(Succeed())
			Expect(updated.Status.FloorID).To(Equal("new-floor-id"))
			Expect(updated.Status.Conditions).To(HaveLen(1))
			Expect(updated.Status.Conditions[0].Status).To(Equal(metav1.ConditionTrue))
			Expect(updated.Status.Conditions[0].Reason).To(Equal("FloorReady"))
		})
	})

	Context("When adopting an existing floor", func() {
		It("should adopt floor by name and set floorID", func() {
			setupHA()

			// Pre-populate floors list
			floors = []map[string]interface{}{
				{"floor_id": "existing-id", "name": "Basement", "level": json.Number("-1"), "icon": ""},
			}
			setupMockWS()

			floor := &hav1alpha1.HomeAssistantFloor{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "basement",
					Namespace: ns,
				},
				Spec: hav1alpha1.HomeAssistantFloorSpec{
					HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
					Name:             "Basement",
				},
			}
			Expect(k8sClient.Create(ctx, floor)).To(Succeed())
			defer func() {
				f := &hav1alpha1.HomeAssistantFloor{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: floor.Name, Namespace: ns}, f)
				f.Finalizers = nil
				_ = k8sClient.Update(ctx, f)
				_ = k8sClient.Delete(ctx, f)
			}()

			wsURL := "ws" + strings.TrimPrefix(mockServer.URL, "http")
			reconciler := &HomeAssistantFloorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				NewHAClient: func(_ string) *haclient.Client {
					return haclient.NewClient(wsURL)
				},
			}

			// Finalizer
			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: floor.Name, Namespace: ns},
			})

			// Adopt
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: floor.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &hav1alpha1.HomeAssistantFloor{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: floor.Name, Namespace: ns}, updated)).To(Succeed())
			Expect(updated.Status.FloorID).To(Equal("existing-id"))
		})
	})
})

func intPtr(i int) *int {
	return &i
}
