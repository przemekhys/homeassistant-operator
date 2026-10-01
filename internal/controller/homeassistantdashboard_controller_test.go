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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

var _ = Describe("HomeAssistantDashboard Controller", func() {
	var (
		ctx        context.Context
		ns         string
		haName     string
		mockServer *httptest.Server
		mockHA     *dashboardMockHA
	)

	setupHA := func() {
		ha := &hav1.HomeAssistant{
			ObjectMeta: metav1.ObjectMeta{Name: haName, Namespace: ns},
			Spec:       hav1.HomeAssistantSpec{Version: "2026.3"},
		}
		Expect(k8sClient.Create(ctx, ha)).To(Succeed())
		ha.Status.Phase = "Running"
		ha.Status.Bootstrap = &hav1.BootstrapStatus{APITokenSecretName: haName + "-api-token"}
		Expect(k8sClient.Status().Update(ctx, ha)).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: haName + "-api-token", Namespace: ns},
			Data:       map[string][]byte{"token": []byte("test-token")},
		})).To(Succeed())
	}

	newReconciler := func() *HomeAssistantDashboardReconciler {
		return &HomeAssistantDashboardReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			NewHAClient: func(_ string) *haclient.Client {
				return haclient.NewClient(mockHA.url())
			},
		}
	}

	reconcileDashboard := func(reconciler *HomeAssistantDashboardReconciler, name string) {
		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeEach(func() {
		ctx = context.Background()
		ns = "default"
		haName = "test-ha-dashboard"
		mockHA = newDashboardMockHA()
		mockServer = mockHA.server
	})

	AfterEach(func() {
		if mockServer != nil {
			mockServer.Close()
		}
		for _, name := range []string{"inline-dashboard", "configmap-dashboard"} {
			dashboard := &hav1alpha1.HomeAssistantDashboard{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, dashboard); err == nil {
				controllerutil.RemoveFinalizer(dashboard, dashboardFinalizer)
				_ = k8sClient.Update(ctx, dashboard)
				_ = k8sClient.Delete(ctx, dashboard)
			}
		}
		for _, name := range []string{"dashboard-config"} {
			cm := &corev1.ConfigMap{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, cm); err == nil {
				_ = k8sClient.Delete(ctx, cm)
			}
		}
		for _, name := range []string{haName, haName + "-api-token"} {
			if name == haName {
				_ = k8sClient.Delete(ctx, &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}})
			} else {
				_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}})
			}
		}
	})

	It("creates an inline dashboard, saves its content, and records ready status", func() {
		setupHA()
		dashboard := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "inline-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Inline Dashboard",
				URLPath:          "inline-dashboard",
				Inline:           "title: Inline Dashboard\nviews: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())

		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name) // Adds the finalizer.
		reconcileDashboard(reconciler, dashboard.Name)

		Expect(mockHA.createCount()).To(Equal(1))
		Expect(mockHA.saveCount()).To(Equal(1))
		Expect(mockHA.lastSavedConfig()["title"]).To(Equal("Inline Dashboard"))
		updated := &hav1alpha1.HomeAssistantDashboard{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dashboard.Name, Namespace: ns}, updated)).To(Succeed())
		Expect(updated.Status.DashboardID).To(Equal("dashboard-1"))
		Expect(updated.Status.URLPath).To(Equal("inline-dashboard"))
		Expect(updated.Status.SourceHash).NotTo(BeEmpty())
		Expect(updated.Status.Conditions).To(ContainElement(HaveField("Reason", "DashboardConfigured")))
	})

	It("updates and saves an inline dashboard when its content changes", func() {
		setupHA()
		dashboard := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "inline-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Inline Dashboard",
				URLPath:          "inline-dashboard",
				Inline:           "title: Before\nviews: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())
		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name)
		reconcileDashboard(reconciler, dashboard.Name)

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dashboard.Name, Namespace: ns}, dashboard)).To(Succeed())
		dashboard.Spec.Inline = "title: After\nviews: []\n"
		Expect(k8sClient.Update(ctx, dashboard)).To(Succeed())
		reconcileDashboard(reconciler, dashboard.Name)

		Expect(mockHA.updateCount()).To(Equal(1))
		Expect(mockHA.saveCount()).To(Equal(2))
		Expect(mockHA.lastSavedConfig()["title"]).To(Equal("After"))
	})

	It("resolves only the selected ConfigMap key and does not rewrite for unrelated changes", func() {
		setupHA()
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "dashboard-config", Namespace: ns},
			Data: map[string]string{
				"dashboard.yaml": "title: Selected\nviews: []\n",
				"unrelated.yaml": "title: Unrelated\nviews: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
		dashboard := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "configmap-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "ConfigMap Dashboard",
				URLPath:          "configmap-dashboard",
				ConfigMapKeyRef:  &hav1alpha1.ConfigMapKeyReference{Name: configMap.Name, Key: "dashboard.yaml"},
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())
		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name)
		reconcileDashboard(reconciler, dashboard.Name)

		configMap.Data["unrelated.yaml"] = "title: Changed unrelated content\nviews: []\n"
		Expect(k8sClient.Update(ctx, configMap)).To(Succeed())
		reconcileDashboard(reconciler, dashboard.Name)
		Expect(mockHA.saveCount()).To(Equal(1))
		Expect(mockHA.updateCount()).To(Equal(0))

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMap.Name, Namespace: ns}, configMap)).To(Succeed())
		configMap.Data["dashboard.yaml"] = "title: Updated selected content\nviews: []\n"
		Expect(k8sClient.Update(ctx, configMap)).To(Succeed())
		reconcileDashboard(reconciler, dashboard.Name)
		Expect(mockHA.saveCount()).To(Equal(2))
		Expect(mockHA.lastSavedConfig()["title"]).To(Equal("Updated selected content"))
	})
})

type dashboardMockHA struct {
	server     *httptest.Server
	mu         sync.Mutex
	dashboards []map[string]interface{}
	creates    int
	updates    int
	saves      int
	lastConfig map[string]interface{}
}

func newDashboardMockHA() *dashboardMockHA {
	mock := &dashboardMockHA{}
	upgrader := websocket.Upgrader{}
	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
		var auth map[string]interface{}
		if conn.ReadJSON(&auth) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})
		var command map[string]interface{}
		if conn.ReadJSON(&command) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]interface{}{
			"id": command["id"], "type": "result", "success": true, "result": mock.handle(command),
		})
	}))
	return mock
}

func (m *dashboardMockHA) url() string { return "ws" + strings.TrimPrefix(m.server.URL, "http") }

func (m *dashboardMockHA) handle(command map[string]interface{}) interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch command["type"] {
	case "lovelace/dashboards/list":
		return m.dashboards
	case "lovelace/dashboards/create":
		m.creates++
		dashboard := map[string]interface{}{
			"id": "dashboard-1", "url_path": command["url_path"], "mode": "storage", "title": command["title"],
		}
		m.dashboards = append(m.dashboards, dashboard)
		return dashboard
	case "lovelace/dashboards/update":
		m.updates++
		return map[string]interface{}{
			"id": command["dashboard_id"], "url_path": "", "mode": "storage", "title": command["title"],
		}
	case "lovelace/config/save":
		m.saves++
		m.lastConfig, _ = command["config"].(map[string]interface{})
		return map[string]interface{}{}
	default:
		return map[string]interface{}{}
	}
}

func (m *dashboardMockHA) createCount() int { m.mu.Lock(); defer m.mu.Unlock(); return m.creates }
func (m *dashboardMockHA) updateCount() int { m.mu.Lock(); defer m.mu.Unlock(); return m.updates }
func (m *dashboardMockHA) saveCount() int   { m.mu.Lock(); defer m.mu.Unlock(); return m.saves }

func (m *dashboardMockHA) lastSavedConfig() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastConfig
}
