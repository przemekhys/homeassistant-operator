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
	"k8s.io/apimachinery/pkg/api/meta"
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

	reconcileDashboard := func(reconciler *HomeAssistantDashboardReconciler, name string) reconcile.Result {
		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		return result
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
		for _, name := range []string{
			"inline-dashboard", "configmap-dashboard", "delete-dashboard", "unavailable-dashboard",
			"rejected-save-dashboard", "missing-configmap-dashboard", "invalid-yaml-dashboard", "conflict-dashboard",
		} {
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

	It("updates dashboard metadata when the Home Assistant title drifts", func() {
		setupHA()
		dashboard := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "inline-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Expected Title", URLPath: "inline-dashboard", Inline: "views: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())
		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name)
		reconcileDashboard(reconciler, dashboard.Name)

		mockHA.setDashboardTitle("Drifted Title")
		reconcileDashboard(reconciler, dashboard.Name)

		Expect(mockHA.updateCount()).To(Equal(1))
	})

	It("does not adopt a named dashboard that already uses the requested path", func() {
		setupHA()
		mockHA.addDashboard("external-dashboard", "conflict-dashboard", "External Dashboard")
		dashboard := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "conflict-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Managed Dashboard", URLPath: "conflict-dashboard", Inline: "views: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())
		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name)
		result := reconcileDashboard(reconciler, dashboard.Name)

		Expect(result.RequeueAfter).To(Equal(dashboardRetry))
		Expect(mockHA.updateCount()).To(Equal(0))
		updated := &hav1alpha1.HomeAssistantDashboard{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dashboard.Name, Namespace: ns}, updated)).To(Succeed())
		condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
		Expect(condition).To(HaveField("Reason", "DashboardPathConflict"))
	})

	It("restores the desired config when the Home Assistant dashboard drifts", func() {
		setupHA()
		dashboard := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "inline-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Inline Dashboard", URLPath: "inline-dashboard",
				Inline: "title: Desired\nviews: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())
		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name)
		reconcileDashboard(reconciler, dashboard.Name)

		mockHA.setConfig(map[string]interface{}{"title": "Drifted", "views": []interface{}{}})
		reconcileDashboard(reconciler, dashboard.Name)

		Expect(mockHA.saveCount()).To(Equal(2))
		Expect(mockHA.lastSavedConfig()["title"]).To(Equal("Desired"))
	})

	It("creates the default dashboard only when explicitly selected", func() {
		setupHA()
		dashboard := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "inline-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Home",
				DefaultDashboard: true,
				Inline:           "views: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())
		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name)
		reconcileDashboard(reconciler, dashboard.Name)

		Expect(mockHA.createCount()).To(Equal(1))
		updated := &hav1alpha1.HomeAssistantDashboard{}
		key := types.NamespacedName{Name: dashboard.Name, Namespace: ns}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.URLPath).To(Equal("lovelace"))
	})

	It("rejects an implicit default dashboard and ambiguous sources", func() {
		implicitDefault := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "inline-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Home",
				URLPath:          "lovelace",
				Inline:           "views: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, implicitDefault)).NotTo(Succeed())

		ambiguous := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "configmap-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Ambiguous",
				URLPath:          "ambiguous",
				Inline:           "views: []\n",
				ConfigMapKeyRef:  &hav1alpha1.ConfigMapKeyReference{Name: "dashboard-config", Key: "dashboard.yaml"},
			},
		}
		Expect(k8sClient.Create(ctx, ambiguous)).NotTo(Succeed())
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
		changed := configMap.DeepCopy()
		changed.Data["unrelated.yaml"] = "title: Changed unrelated content\nviews: []\n"
		Expect(reconciler.findDashboardsForChangedConfigMap(ctx, configMap, changed)).To(BeEmpty())
		changed.Data["dashboard.yaml"] = "title: Updated selected content\nviews: []\n"
		Expect(reconciler.findDashboardsForChangedConfigMap(ctx, configMap, changed)).To(ConsistOf(
			reconcile.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: ns}},
		))

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

	It("deletes the Home Assistant dashboard before removing its finalizer", func() {
		setupHA()
		dashboard := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "delete-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Delete Dashboard", URLPath: "delete-dashboard", Inline: "views: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())
		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name)
		reconcileDashboard(reconciler, dashboard.Name)

		Expect(k8sClient.Delete(ctx, dashboard)).To(Succeed())
		reconcileDashboard(reconciler, dashboard.Name)
		Expect(mockHA.deleteCount()).To(Equal(1))
		Eventually(func() error {
			key := types.NamespacedName{Name: dashboard.Name, Namespace: ns}
			return k8sClient.Get(ctx, key, &hav1alpha1.HomeAssistantDashboard{})
		}).Should(HaveOccurred())
	})

	It("retries and recovers when Home Assistant is unavailable", func() {
		setupHA()
		dashboard := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "unavailable-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Unavailable Dashboard", URLPath: "unavailable-dashboard", Inline: "views: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())
		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name)
		mockHA.setUnavailable(true)
		result := reconcileDashboard(reconciler, dashboard.Name)
		Expect(result.RequeueAfter).To(Equal(dashboardRetry))

		updated := &hav1alpha1.HomeAssistantDashboard{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dashboard.Name, Namespace: ns}, updated)).To(Succeed())
		ready := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
		Expect(ready).To(HaveField("Reason", "HomeAssistantUnavailable"))

		mockHA.setUnavailable(false)
		reconcileDashboard(reconciler, dashboard.Name)
		Expect(mockHA.saveCount()).To(Equal(1))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dashboard.Name, Namespace: ns}, updated)).To(Succeed())
		ready = meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
		Expect(ready).To(HaveField("Status", metav1.ConditionTrue))
	})

	It("retries a rejected dashboard save without recording a successful hash", func() {
		setupHA()
		mockHA.setRejectSave(true)
		dashboard := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "rejected-save-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Rejected Save", URLPath: "rejected-save-dashboard", Inline: "views: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())
		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name)
		result := reconcileDashboard(reconciler, dashboard.Name)
		Expect(result.RequeueAfter).To(Equal(dashboardRetry))

		updated := &hav1alpha1.HomeAssistantDashboard{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dashboard.Name, Namespace: ns}, updated)).To(Succeed())
		Expect(updated.Status.SourceHash).To(BeEmpty())
		Expect(updated.Status.DashboardID).To(Equal("dashboard-1"))
		ready := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
		Expect(ready).To(HaveField("Reason", "DashboardWriteFailed"))

		mockHA.setRejectSave(false)
		reconcileDashboard(reconciler, dashboard.Name)
		Expect(mockHA.saveCount()).To(Equal(2))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dashboard.Name, Namespace: ns}, updated)).To(Succeed())
		Expect(updated.Status.SourceHash).NotTo(BeEmpty())
	})

	It("retains its finalizer and records a missing HomeAssistant during deletion", func() {
		setupHA()
		dashboard := &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "delete-dashboard", Namespace: ns},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Title:            "Delete Dashboard", URLPath: "delete-dashboard", Inline: "views: []\n",
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())
		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name)
		reconcileDashboard(reconciler, dashboard.Name)
		ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: haName, Namespace: ns}}
		Expect(k8sClient.Delete(ctx, ha)).To(Succeed())
		Expect(k8sClient.Delete(ctx, dashboard)).To(Succeed())

		result := reconcileDashboard(reconciler, dashboard.Name)
		Expect(result.RequeueAfter).To(Equal(dashboardRetry))
		updated := &hav1alpha1.HomeAssistantDashboard{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dashboard.Name, Namespace: ns}, updated)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(updated, dashboardFinalizer)).To(BeTrue())
		condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
		Expect(condition).To(HaveField("Reason", "HomeAssistantNotFound"))
	})

	DescribeTable("retries invalid ConfigMap sources", func(dashboard *hav1alpha1.HomeAssistantDashboard, reason string) {
		if dashboard.Name == "invalid-yaml-dashboard" {
			Expect(k8sClient.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "dashboard-config", Namespace: ns},
				Data:       map[string]string{"invalid.yaml": "views: ["},
			})).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())
		reconciler := newReconciler()
		reconcileDashboard(reconciler, dashboard.Name)
		result := reconcileDashboard(reconciler, dashboard.Name)
		Expect(result.RequeueAfter).To(Equal(dashboardRetry))

		updated := &hav1alpha1.HomeAssistantDashboard{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dashboard.Name, Namespace: ns}, updated)).To(Succeed())
		condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeReady)
		Expect(condition).To(HaveField("Reason", "SourceInvalid"))
		Expect(condition.Message).To(ContainSubstring(reason))
	},
		Entry("when the ConfigMap is missing", &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "missing-configmap-dashboard", Namespace: "default"},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: "unused"},
				Title:            "Missing ConfigMap", URLPath: "missing-configmap-dashboard",
				ConfigMapKeyRef: &hav1alpha1.ConfigMapKeyReference{Name: "missing", Key: "dashboard.yaml"},
			},
		}, "failed to get ConfigMap missing"),
		Entry("when the selected ConfigMap value is invalid YAML", &hav1alpha1.HomeAssistantDashboard{
			ObjectMeta: metav1.ObjectMeta{Name: "invalid-yaml-dashboard", Namespace: "default"},
			Spec: hav1alpha1.HomeAssistantDashboardSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: "unused"},
				Title:            "Invalid YAML", URLPath: "invalid-yaml-dashboard",
				ConfigMapKeyRef: &hav1alpha1.ConfigMapKeyReference{Name: "dashboard-config", Key: "invalid.yaml"},
			},
		}, "invalid dashboard YAML"),
	)
})

type dashboardMockHA struct {
	server      *httptest.Server
	mu          sync.Mutex
	dashboards  []map[string]interface{}
	creates     int
	updates     int
	saves       int
	deletes     int
	unavailable bool
	rejectSave  bool
	config      map[string]interface{}
	lastConfig  map[string]interface{}
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
		result, success := mock.handle(command)
		response := map[string]interface{}{"id": command["id"], "type": "result", "success": success}
		if success {
			response["result"] = result
		} else {
			response["error"] = map[string]interface{}{"message": "dashboard save rejected"}
		}
		_ = conn.WriteJSON(response)
	}))
	return mock
}

func (m *dashboardMockHA) url() string { return "ws" + strings.TrimPrefix(m.server.URL, "http") }

func (m *dashboardMockHA) handle(command map[string]interface{}) (interface{}, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unavailable {
		return nil, false
	}
	switch command["type"] {
	case "lovelace/dashboards/list":
		return m.dashboards, true
	case "lovelace/dashboards/create":
		m.creates++
		dashboard := map[string]interface{}{
			"id": "dashboard-1", "url_path": command["url_path"], "mode": "storage", "title": command["title"],
		}
		m.dashboards = append(m.dashboards, dashboard)
		return dashboard, true
	case "lovelace/dashboards/update":
		m.updates++
		for _, dashboard := range m.dashboards {
			if dashboard["id"] == command["dashboard_id"] {
				dashboard["title"] = command["title"]
			}
		}
		return map[string]interface{}{
			"id": command["dashboard_id"], "url_path": "", "mode": "storage", "title": command["title"],
		}, true
	case "lovelace/config":
		return m.config, true
	case "lovelace/config/save":
		m.saves++
		if m.rejectSave {
			return nil, false
		}
		m.lastConfig, _ = command["config"].(map[string]interface{})
		m.config = m.lastConfig
		return map[string]interface{}{}, true
	case "lovelace/dashboards/delete":
		m.deletes++
		return map[string]interface{}{}, true
	default:
		return map[string]interface{}{}, true
	}
}

func (m *dashboardMockHA) createCount() int { m.mu.Lock(); defer m.mu.Unlock(); return m.creates }
func (m *dashboardMockHA) updateCount() int { m.mu.Lock(); defer m.mu.Unlock(); return m.updates }
func (m *dashboardMockHA) saveCount() int   { m.mu.Lock(); defer m.mu.Unlock(); return m.saves }
func (m *dashboardMockHA) deleteCount() int { m.mu.Lock(); defer m.mu.Unlock(); return m.deletes }

func (m *dashboardMockHA) setUnavailable(unavailable bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unavailable = unavailable
}

func (m *dashboardMockHA) setRejectSave(reject bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rejectSave = reject
}

func (m *dashboardMockHA) setConfig(config map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = config
}

func (m *dashboardMockHA) setDashboardTitle(title string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.dashboards) > 0 {
		m.dashboards[0]["title"] = title
	}
}

func (m *dashboardMockHA) addDashboard(id, urlPath, title string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dashboards = append(m.dashboards, map[string]interface{}{
		"id": id, "url_path": urlPath, "mode": "storage", "title": title,
	})
}

func (m *dashboardMockHA) lastSavedConfig() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastConfig
}
