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

	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

var _ = Describe("Backup Controller", func() {
	const (
		haName    = "test-ha-backup"
		namespace = "default"
		timeout   = time.Second * 10
		interval  = time.Millisecond * 250
	)

	var (
		reconciler *HomeAssistantReconciler
		wsServer   *httptest.Server
		upgrader   = websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	)

	// wsHandler creates a mock WS handler that handles auth and returns configurable responses
	wsHandler := func(
		backupConfig map[string]interface{},
		updateCallback func(map[string]json.RawMessage),
	) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/websocket" {
				http.NotFound(w, r)
				return
			}
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

			// Read command and respond
			var cmd map[string]json.RawMessage
			if err := conn.ReadJSON(&cmd); err != nil {
				return
			}

			var cmdType string
			_ = json.Unmarshal(cmd["type"], &cmdType)

			switch cmdType {
			case "backup/config/info":
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      1,
					"type":    "result",
					"success": true,
					"result":  backupConfig,
				})
			case "backup/config/update":
				if updateCallback != nil {
					updateCallback(cmd)
				}
				_ = conn.WriteJSON(map[string]interface{}{
					"id":      1,
					"type":    "result",
					"success": true,
					"result":  nil,
				})
			}
		}
	}

	// createHA creates a HomeAssistant CR with optional backup spec
	createHA := func(backup *hav1alpha1.BackupSpec) {
		ha := &hav1alpha1.HomeAssistant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      haName,
				Namespace: namespace,
			},
			Spec: hav1alpha1.HomeAssistantSpec{
				Version: "2025.3",
				Bootstrap: &hav1alpha1.BootstrapSpec{
					Enabled:        true,
					CreateAPIToken: true,
				},
				Backup: backup,
			},
		}
		Expect(k8sClient.Create(ctx, ha)).To(Succeed())

		// Set bootstrap status with token secret name
		Eventually(func() error {
			updated := &hav1alpha1.HomeAssistant{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, updated); err != nil {
				return err
			}
			updated.Status.Bootstrap = &hav1alpha1.BootstrapStatus{
				Completed:          true,
				APITokenReady:      true,
				APITokenSecretName: haName + "-api-token",
			}
			return k8sClient.Status().Update(ctx, updated)
		}, timeout, interval).Should(Succeed())

		// Create API token secret
		tokenSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      haName + "-api-token",
				Namespace: namespace,
			},
			Data: map[string][]byte{
				"token": []byte("test-backup-token"),
			},
		}
		Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())

		// Create required HomeAssistantConfiguration
		haConfig := &hav1alpha1.HomeAssistantConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name:      haName + "-config",
				Namespace: namespace,
			},
			Spec: hav1alpha1.HomeAssistantConfigurationSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Configuration:    "homeassistant:\n  name: Test\n",
			},
		}
		Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())
	}

	cleanupAll := func() {
		// Delete HA
		ha := &hav1alpha1.HomeAssistant{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha); err == nil {
			_ = k8sClient.Delete(ctx, ha)
		}
		// Delete token secret
		secret := &corev1.Secret{}
		key := types.NamespacedName{Name: haName + "-api-token", Namespace: namespace}
		if err := k8sClient.Get(ctx, key, secret); err == nil {
			_ = k8sClient.Delete(ctx, secret)
		}
		// Delete HAConfig
		config := &hav1alpha1.HomeAssistantConfiguration{}
		cfgKey := types.NamespacedName{Name: haName + "-config", Namespace: namespace}
		if err := k8sClient.Get(ctx, cfgKey, config); err == nil {
			_ = k8sClient.Delete(ctx, config)
		}
		// Delete ConfigMaps
		cmList := &corev1.ConfigMapList{}
		_ = k8sClient.List(ctx, cmList, &client.ListOptions{Namespace: namespace})
		for _, cm := range cmList.Items {
			_ = k8sClient.Delete(ctx, &cm)
		}
		// Wait for HA to be deleted
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, &hav1alpha1.HomeAssistant{})
			return err != nil
		}, timeout, interval).Should(BeTrue())
	}

	AfterEach(func() {
		if wsServer != nil {
			wsServer.Close()
			wsServer = nil
		}
		cleanupAll()
	})

	It("should configure backup when spec.backup.enabled", func() {
		var receivedUpdate map[string]json.RawMessage

		wsServer = httptest.NewServer(wsHandler(
			map[string]interface{}{
				"schedule":      map[string]interface{}{"recurrence": "never", "time": nil},
				"retention":     map[string]interface{}{"copies": nil, "days": nil},
				"create_backup": map[string]interface{}{"include_database": true},
			},
			func(cmd map[string]json.RawMessage) {
				receivedUpdate = cmd
			},
		))

		wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
		reconciler = &HomeAssistantReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			NewHAClient: func(_ string) *haclient.Client { return haclient.NewClient(wsURL) },
		}

		createHA(&hav1alpha1.BackupSpec{
			Enabled:         true,
			Recurrence:      "daily",
			Time:            "03:00:00",
			RetentionCopies: ptr.To[int32](7),
			IncludeDatabase: ptr.To(true),
		})

		// Reconcile
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: haName, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		// Verify update was sent
		Expect(receivedUpdate).NotTo(BeNil())
		var cmdType string
		Expect(json.Unmarshal(receivedUpdate["type"], &cmdType)).To(Succeed())
		Expect(cmdType).To(Equal("backup/config/update"))

		// Verify condition
		ha := &hav1alpha1.HomeAssistant{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha)).To(Succeed())
		cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupConfigured)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal(reasonBackupConfigured))
	})

	It("should skip backup config when not enabled", func() {
		wsServer = httptest.NewServer(wsHandler(nil, nil))
		wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
		reconciler = &HomeAssistantReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			NewHAClient: func(_ string) *haclient.Client { return haclient.NewClient(wsURL) },
		}

		createHA(nil) // no backup spec

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: haName, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		// Verify no BackupConfigured condition
		ha := &hav1alpha1.HomeAssistant{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha)).To(Succeed())
		cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupConfigured)
		Expect(cond).To(BeNil())
	})

	It("should skip backup config when no API token", func() {
		wsServer = httptest.NewServer(wsHandler(nil, nil))
		wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
		reconciler = &HomeAssistantReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			NewHAClient: func(_ string) *haclient.Client { return haclient.NewClient(wsURL) },
		}

		// Create HA with backup but WITHOUT token secret
		ha := &hav1alpha1.HomeAssistant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      haName,
				Namespace: namespace,
			},
			Spec: hav1alpha1.HomeAssistantSpec{
				Version: "2025.3",
				Backup:  &hav1alpha1.BackupSpec{Enabled: true, Recurrence: "daily"},
			},
		}
		Expect(k8sClient.Create(ctx, ha)).To(Succeed())

		// Create required HAConfig
		haConfig := &hav1alpha1.HomeAssistantConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name:      haName + "-config",
				Namespace: namespace,
			},
			Spec: hav1alpha1.HomeAssistantConfigurationSpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Configuration:    "homeassistant:\n  name: Test\n",
			},
		}
		Expect(k8sClient.Create(ctx, haConfig)).To(Succeed())

		result, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: haName, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(30 * time.Second))

		// Verify condition shows token not available
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha)).To(Succeed())
		cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupConfigured)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(reasonBackupTokenNotAvailable))
	})

	It("should be idempotent when config already matches", func() {
		var updateCalled bool

		wsServer = httptest.NewServer(wsHandler(
			// Current config already matches desired
			map[string]interface{}{
				"schedule":      map[string]interface{}{"recurrence": "daily", "time": "03:00:00"},
				"retention":     map[string]interface{}{"copies": 5, "days": nil},
				"create_backup": map[string]interface{}{"include_database": true, "agent_ids": []string{"backup.local"}},
			},
			func(cmd map[string]json.RawMessage) {
				updateCalled = true
			},
		))

		wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
		reconciler = &HomeAssistantReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			NewHAClient: func(_ string) *haclient.Client { return haclient.NewClient(wsURL) },
		}

		createHA(&hav1alpha1.BackupSpec{
			Enabled:         true,
			Recurrence:      "daily",
			Time:            "03:00:00",
			RetentionCopies: ptr.To[int32](5),
			IncludeDatabase: ptr.To(true),
		})

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: haName, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		// update should NOT have been called
		Expect(updateCalled).To(BeFalse())

		// Condition should still be True
		ha := &hav1alpha1.HomeAssistant{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha)).To(Succeed())
		cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupConfigured)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
	})

	It("should handle WebSocket failure gracefully", func() {
		// WS server that always fails on backup/config/info
		wsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/websocket" {
				http.NotFound(w, r)
				return
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()

			_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
			var authMsg map[string]interface{}
			_ = conn.ReadJSON(&authMsg)
			_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

			var cmd map[string]interface{}
			_ = conn.ReadJSON(&cmd)

			_ = conn.WriteJSON(map[string]interface{}{
				"id":      cmd["id"],
				"type":    "result",
				"success": false,
				"error":   map[string]interface{}{"code": "unknown_error", "message": "backup not loaded"},
			})
		}))

		wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
		reconciler = &HomeAssistantReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			NewHAClient: func(_ string) *haclient.Client { return haclient.NewClient(wsURL) },
		}

		createHA(&hav1alpha1.BackupSpec{
			Enabled:    true,
			Recurrence: "daily",
		})

		result, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: haName, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(30 * time.Second))

		// Condition should be False
		ha := &hav1alpha1.HomeAssistant{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha)).To(Succeed())
		cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupConfigured)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(reasonBackupConfigFailed))
	})

	It("should clear BackupConfigured condition when backup disabled", func() {
		wsServer = httptest.NewServer(wsHandler(
			map[string]interface{}{
				"schedule":      map[string]interface{}{"recurrence": "never", "time": nil},
				"retention":     map[string]interface{}{"copies": nil, "days": nil},
				"create_backup": map[string]interface{}{"include_database": true},
			},
			nil,
		))

		wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
		reconciler = &HomeAssistantReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			NewHAClient: func(_ string) *haclient.Client { return haclient.NewClient(wsURL) },
		}

		// Create HA with backup enabled first
		createHA(&hav1alpha1.BackupSpec{
			Enabled:    true,
			Recurrence: "daily",
		})

		// First reconcile — sets BackupConfigured=True
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: haName, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		// Verify condition is set
		ha := &hav1alpha1.HomeAssistant{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha)).To(Succeed())
		Expect(meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupConfigured)).NotTo(BeNil())

		// Now disable backup
		ha.Spec.Backup = nil
		Expect(k8sClient.Update(ctx, ha)).To(Succeed())

		// Reconcile again
		_, err = reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: haName, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		// Verify condition is removed
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: haName, Namespace: namespace}, ha)).To(Succeed())
		cond := meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupConfigured)
		Expect(cond).To(BeNil())
	})

	It("should detect config drift and reconfigure", func() {
		var updateCount int

		wsServer = httptest.NewServer(wsHandler(
			// Current: recurrence=daily, but we want mon
			map[string]interface{}{
				"schedule":      map[string]interface{}{"recurrence": "daily", "time": nil},
				"retention":     map[string]interface{}{"copies": nil, "days": nil},
				"create_backup": map[string]interface{}{"include_database": true},
			},
			func(cmd map[string]json.RawMessage) {
				updateCount++
			},
		))

		wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
		reconciler = &HomeAssistantReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			NewHAClient: func(_ string) *haclient.Client { return haclient.NewClient(wsURL) },
		}

		createHA(&hav1alpha1.BackupSpec{
			Enabled:    true,
			Recurrence: "mon", // different from current "daily"
		})

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: haName, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		// Update should have been called (drift detected)
		Expect(updateCount).To(Equal(1))
	})
})
