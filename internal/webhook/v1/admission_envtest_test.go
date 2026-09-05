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

package v1

import (
	"context"
	"crypto/tls"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

func TestGatewayClassNameAdmission(t *testing.T) {
	k8sClient, _, cleanup := setupWebhookTestEnv(t)
	defer cleanup()

	tests := []struct {
		name                    string
		gatewayClassName        string
		includeGatewayClassName bool
		wantAccepted            bool
		wantDefault             string
	}{
		{name: "valid", gatewayClassName: "traefik", includeGatewayClassName: true, wantAccepted: true},
		{name: "maximum length", gatewayClassName: strings.Repeat("a", 253),
			includeGatewayClassName: true, wantAccepted: true},
		{name: "explicit empty", includeGatewayClassName: true, wantAccepted: false},
		{name: "malformed", gatewayClassName: "Traefik", includeGatewayClassName: true, wantAccepted: false},
		{name: "over maximum length", gatewayClassName: strings.Repeat("a", 254),
			includeGatewayClassName: true, wantAccepted: false},
		{name: "omission applies default", wantAccepted: true, wantDefault: "traefik"},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			gateway := map[string]interface{}{
				"enabled":       true,
				"host":          "ha.example.com",
				"manageGateway": true,
			}
			if tt.includeGatewayClassName {
				gateway["gatewayClassName"] = tt.gatewayClassName
			}
			obj := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": hav1.GroupVersion.String(),
				"kind":       "HomeAssistant",
				"metadata": map[string]interface{}{
					"name":      fmt.Sprintf("gateway-class-%d", i),
					"namespace": "default",
				},
				"spec": map[string]interface{}{
					"gateway": gateway,
				},
			}}

			err := k8sClient.Create(context.Background(), obj)
			if tt.wantAccepted {
				g.Expect(err).NotTo(HaveOccurred())
				if tt.wantDefault != "" {
					className, found, nestedErr := unstructured.NestedString(obj.Object,
						"spec", "gateway", "gatewayClassName")
					g.Expect(nestedErr).NotTo(HaveOccurred())
					g.Expect(found).To(BeTrue())
					g.Expect(className).To(Equal(tt.wantDefault))
				}
			} else {
				g.Expect(err).To(HaveOccurred())
			}
		})
	}
}

// setupWebhookTestEnv spins up a real envtest API server, a real
// ValidatingWebhookConfiguration, and a real webhook server (the same wiring
// cmd/main.go uses via SetupHomeAssistantWebhookWithManager), then waits for
// the webhook server to accept TLS connections before returning a client
// pointed at it. Callers must invoke the returned cleanup function (e.g. via
// defer) to stop the manager and the envtest environment.
func setupWebhookTestEnv(t *testing.T) (client.Client, *rest.Config, func()) {
	t.Helper()
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())

	g.Expect(hav1.AddToScheme(scheme.Scheme)).To(Succeed())

	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "..", "..", "config", "webhook")},
		},
	}

	cfg, err := testEnv.Start()
	g.Expect(err).NotTo(HaveOccurred())

	whOpts := testEnv.WebhookInstallOptions
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    whOpts.LocalServingHost,
			Port:    whOpts.LocalServingPort,
			CertDir: whOpts.LocalServingCertDir,
		}),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(SetupHomeAssistantWebhookWithManager(mgr)).To(Succeed())
	g.Expect(SetupHomeAssistantAutomationWebhookWithManager(mgr)).To(Succeed())
	g.Expect(SetupHomeAssistantSceneWebhookWithManager(mgr)).To(Succeed())
	g.Expect(SetupHomeAssistantScriptWebhookWithManager(mgr)).To(Succeed())
	g.Expect(SetupHomeAssistantConfigurationWebhookWithManager(mgr)).To(Succeed())

	go func() {
		_ = mgr.Start(ctx)
	}()

	// Standard kubebuilder pattern: wait until the webhook server actually
	// accepts TLS connections before exercising it.
	g.Eventually(func(g Gomega) {
		conn, err := tls.Dial("tcp",
			fmt.Sprintf("%s:%d", whOpts.LocalServingHost, whOpts.LocalServingPort),
			&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test-only
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(conn.Close()).To(Succeed())
	}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	g.Expect(err).NotTo(HaveOccurred())

	cleanup := func() {
		cancel()
		g.Expect(testEnv.Stop()).To(Succeed())
	}
	return k8sClient, cfg, cleanup
}

// TestAdmissionWebhookRejectsInvalidIngressTLS exercises the real HTTP admission
// path end to end. The unit tests in homeassistant_webhook_test.go only
// exercise validateHomeAssistantTLS as a pure function and would not catch a
// registration/wiring bug.
func TestAdmissionWebhookRejectsInvalidIngressTLS(t *testing.T) {
	g := NewWithT(t)
	k8sClient, _, cleanup := setupWebhookTestEnv(t)
	defer cleanup()

	bad := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-bad", Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{
			Ingress: &hav1.IngressSpec{
				Enabled: true,
				TLS:     &hav1.IngressTLSSpec{Enabled: true},
			},
		},
	}

	err := k8sClient.Create(context.Background(), bad)
	g.Expect(err).To(HaveOccurred(), "webhook should reject ingress TLS without issuerRef/secretName")
	g.Expect(err.Error()).To(ContainSubstring("requires secretName or issuerRef"))
}

// TestAdmissionWebhookRejectsInvalidGatewayFilter exercises the real HTTP
// admission path end to end for spec.gateway.filters, the same way
// TestAdmissionWebhookRejectsInvalidIngressTLS does for ingress TLS. The unit
// tests in homeassistant_webhook_test.go only exercise validateGatewayFilters
// as a pure function and would not catch a registration/wiring bug.
func TestAdmissionWebhookRejectsInvalidGatewayFilter(t *testing.T) {
	g := NewWithT(t)
	k8sClient, _, cleanup := setupWebhookTestEnv(t)
	defer cleanup()

	// type is a valid enum value (passes the CRD's own OpenAPI schema check),
	// but the required requestHeaderModifier sub-object is missing — only the
	// webhook's own validateGatewayFilters catches this, since the CRD schema
	// treats every sub-object as optional (there is no CEL cross-field rule).
	bad := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-bad-filter", Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{
			Gateway: &hav1.GatewaySpec{
				Filters: []hav1.HTTPRouteFilter{{Type: "RequestHeaderModifier"}},
			},
		},
	}

	err := k8sClient.Create(context.Background(), bad)
	g.Expect(err).To(HaveOccurred(), "webhook should reject a filter missing its declared type's sub-object")
	g.Expect(err.Error()).To(ContainSubstring("requestHeaderModifier is required"))
}

// TestAdmissionWebhookRejectsNonexistentPriorityClass exercises the real HTTP
// admission path end to end for spec.scheduling.priorityClassName. This is
// the one validateScheduling case that genuinely needs a real API server
// (validateNodeSelector's cases are covered as pure-function unit tests in
// homeassistant_webhook_test.go instead) — it must actually list real
// PriorityClass objects, which only exists here, not in a table test.
func TestAdmissionWebhookRejectsNonexistentPriorityClass(t *testing.T) {
	g := NewWithT(t)
	k8sClient, _, cleanup := setupWebhookTestEnv(t)
	defer cleanup()

	bad := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-bad-priorityclass", Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{
			Scheduling: &hav1.SchedulingSpec{PriorityClassName: "does-not-exist"},
		},
	}

	err := k8sClient.Create(context.Background(), bad)
	g.Expect(err).To(HaveOccurred(), "webhook should reject a priorityClassName naming a nonexistent PriorityClass")
	g.Expect(err.Error()).To(ContainSubstring("does-not-exist"))
}

// TestAdmissionWebhookAcceptsExistingPriorityClass is the accepting
// counterpart to TestAdmissionWebhookRejectsNonexistentPriorityClass: a
// priorityClassName naming a real PriorityClass must be admitted.
func TestAdmissionWebhookAcceptsExistingPriorityClass(t *testing.T) {
	g := NewWithT(t)
	k8sClient, _, cleanup := setupWebhookTestEnv(t)
	defer cleanup()

	pc := &schedulingv1.PriorityClass{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-critical-test"},
		Value:      1000000,
	}
	g.Expect(k8sClient.Create(context.Background(), pc)).To(Succeed())

	good := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-good-priorityclass", Namespace: "default"},
		Spec: hav1.HomeAssistantSpec{
			Scheduling: &hav1.SchedulingSpec{PriorityClassName: "ha-critical-test"},
		},
	}

	g.Expect(k8sClient.Create(context.Background(), good)).To(Succeed())
}

// TestAdmissionWebhookAutomationIdentifierCollision exercises the real HTTP
// admission path for HomeAssistantAutomationCustomValidator end to end. The
// unit tests in homeassistantautomation_webhook_test.go exercise the same
// logic against a fake client and would not catch a registration/wiring bug
// or a real List against objects actually persisted in etcd.
func TestAdmissionWebhookAutomationIdentifierCollision(t *testing.T) {
	g := NewWithT(t)
	k8sClient, _, cleanup := setupWebhookTestEnv(t)
	defer cleanup()
	ctx := context.Background()
	triggers := []hav1.AutomationTrigger{
		{RawExtension: runtime.RawExtension{Raw: []byte(`{"platform":"time","at":"07:00:00"}`)}},
	}
	actions := []hav1.AutomationAction{
		{RawExtension: runtime.RawExtension{Raw: []byte(`{"service":"light.turn_on"}`)}},
	}

	t.Run("explicit id collision with existing sibling is rejected and names it", func(t *testing.T) {
		first := &hav1.HomeAssistantAutomation{
			ObjectMeta: metav1.ObjectMeta{Name: "first-automation", Namespace: "default"},
			Spec: hav1.HomeAssistantAutomationSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "morning_lights",
				Alias:            "Morning lights",
				Triggers:         triggers,
				Actions:          actions,
			},
		}
		g.Expect(k8sClient.Create(ctx, first)).To(Succeed())

		second := first.DeepCopy()
		second.ObjectMeta = metav1.ObjectMeta{Name: "second-automation", Namespace: "default"}
		err := k8sClient.Create(ctx, second)
		g.Expect(err).To(HaveOccurred(), "webhook should reject a colliding effective id")
		g.Expect(err.Error()).To(ContainSubstring("first-automation"))
	})

	t.Run("name-fallback id collision is rejected", func(t *testing.T) {
		first := &hav1.HomeAssistantAutomation{
			ObjectMeta: metav1.ObjectMeta{Name: "sharedname", Namespace: "default"},
			Spec: hav1.HomeAssistantAutomationSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				Alias:            "First",
				Triggers:         triggers,
				Actions:          actions,
			},
		}
		g.Expect(k8sClient.Create(ctx, first)).To(Succeed())

		second := &hav1.HomeAssistantAutomation{
			ObjectMeta: metav1.ObjectMeta{Name: "second-with-explicit-id", Namespace: "default"},
			Spec: hav1.HomeAssistantAutomationSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "sharedname",
				Alias:            "Second",
				Triggers:         triggers,
				Actions:          actions,
			},
		}
		err := k8sClient.Create(ctx, second)
		g.Expect(err).To(HaveOccurred(), "webhook should catch a name-fallback collision, not just an explicit spec.id match")
		g.Expect(err.Error()).To(ContainSubstring("sharedname"))
	})

	t.Run("same id, different HomeAssistant instance admits", func(t *testing.T) {
		other := &hav1.HomeAssistantAutomation{
			ObjectMeta: metav1.ObjectMeta{Name: "other-instance-automation", Namespace: "default"},
			Spec: hav1.HomeAssistantAutomationSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "away"},
				ID:               "morning_lights",
				Alias:            "Also morning lights, different instance",
				Triggers:         triggers,
				Actions:          actions,
			},
		}
		g.Expect(k8sClient.Create(ctx, other)).To(Succeed())
	})

	t.Run("sibling marked for deletion is not a conflict", func(t *testing.T) {
		deleting := &hav1.HomeAssistantAutomation{
			ObjectMeta: metav1.ObjectMeta{
				Name: "deleting-automation", Namespace: "default",
				Finalizers: []string{"ha.homeassistant.io/test-hold"},
			},
			Spec: hav1.HomeAssistantAutomationSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "recycled_id",
				Alias:            "Being deleted",
				Triggers:         triggers,
				Actions:          actions,
			},
		}
		g.Expect(k8sClient.Create(ctx, deleting)).To(Succeed())
		g.Expect(k8sClient.Delete(ctx, deleting)).To(Succeed())

		replacement := &hav1.HomeAssistantAutomation{
			ObjectMeta: metav1.ObjectMeta{Name: "replacement-automation", Namespace: "default"},
			Spec: hav1.HomeAssistantAutomationSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "recycled_id",
				Alias:            "Replacement",
				Triggers:         triggers,
				Actions:          actions,
			},
		}
		g.Expect(k8sClient.Create(ctx, replacement)).To(Succeed())
	})

	t.Run("update does not conflict with its own prior state", func(t *testing.T) {
		self := &hav1.HomeAssistantAutomation{
			ObjectMeta: metav1.ObjectMeta{Name: "self-update-automation", Namespace: "default"},
			Spec: hav1.HomeAssistantAutomationSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "self_update_id",
				Alias:            "Self",
				Triggers:         triggers,
				Actions:          actions,
			},
		}
		g.Expect(k8sClient.Create(ctx, self)).To(Succeed())
		self.Spec.Alias = "Self, renamed"
		g.Expect(k8sClient.Update(ctx, self)).To(Succeed())
	})
}

// TestAdmissionWebhookSceneIdentifierCollision is the HomeAssistantScene
// equivalent of TestAdmissionWebhookAutomationIdentifierCollision.
func TestAdmissionWebhookSceneIdentifierCollision(t *testing.T) {
	g := NewWithT(t)
	k8sClient, _, cleanup := setupWebhookTestEnv(t)
	defer cleanup()
	ctx := context.Background()
	entities := []hav1.SceneEntity{{EntityID: "light.living_room", State: "on"}}

	t.Run("explicit id collision with existing sibling is rejected and names it", func(t *testing.T) {
		first := &hav1.HomeAssistantScene{
			ObjectMeta: metav1.ObjectMeta{Name: "first-scene", Namespace: "default"},
			Spec: hav1.HomeAssistantSceneSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "movie_night",
				Entities:         entities,
			},
		}
		g.Expect(k8sClient.Create(ctx, first)).To(Succeed())

		second := first.DeepCopy()
		second.ObjectMeta = metav1.ObjectMeta{Name: "second-scene", Namespace: "default"}
		err := k8sClient.Create(ctx, second)
		g.Expect(err).To(HaveOccurred(), "webhook should reject a colliding effective id")
		g.Expect(err.Error()).To(ContainSubstring("first-scene"))
	})

	t.Run("name-fallback id collision is rejected", func(t *testing.T) {
		first := &hav1.HomeAssistantScene{
			ObjectMeta: metav1.ObjectMeta{Name: "sharedscenename", Namespace: "default"},
			Spec: hav1.HomeAssistantSceneSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				Entities:         entities,
			},
		}
		g.Expect(k8sClient.Create(ctx, first)).To(Succeed())

		second := &hav1.HomeAssistantScene{
			ObjectMeta: metav1.ObjectMeta{Name: "second-scene-explicit", Namespace: "default"},
			Spec: hav1.HomeAssistantSceneSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "sharedscenename",
				Entities:         entities,
			},
		}
		err := k8sClient.Create(ctx, second)
		g.Expect(err).To(HaveOccurred(), "webhook should catch a name-fallback collision, not just an explicit spec.id match")
		g.Expect(err.Error()).To(ContainSubstring("sharedscenename"))
	})

	t.Run("same id, different HomeAssistant instance admits", func(t *testing.T) {
		other := &hav1.HomeAssistantScene{
			ObjectMeta: metav1.ObjectMeta{Name: "other-instance-scene", Namespace: "default"},
			Spec: hav1.HomeAssistantSceneSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "away"},
				ID:               "movie_night",
				Entities:         entities,
			},
		}
		g.Expect(k8sClient.Create(ctx, other)).To(Succeed())
	})

	t.Run("sibling marked for deletion is not a conflict", func(t *testing.T) {
		deleting := &hav1.HomeAssistantScene{
			ObjectMeta: metav1.ObjectMeta{
				Name: "deleting-scene", Namespace: "default",
				Finalizers: []string{"ha.homeassistant.io/test-hold"},
			},
			Spec: hav1.HomeAssistantSceneSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "recycled_scene_id",
				Entities:         entities,
			},
		}
		g.Expect(k8sClient.Create(ctx, deleting)).To(Succeed())
		g.Expect(k8sClient.Delete(ctx, deleting)).To(Succeed())

		replacement := &hav1.HomeAssistantScene{
			ObjectMeta: metav1.ObjectMeta{Name: "replacement-scene", Namespace: "default"},
			Spec: hav1.HomeAssistantSceneSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "recycled_scene_id",
				Entities:         entities,
			},
		}
		g.Expect(k8sClient.Create(ctx, replacement)).To(Succeed())
	})

	t.Run("update does not conflict with its own prior state", func(t *testing.T) {
		self := &hav1.HomeAssistantScene{
			ObjectMeta: metav1.ObjectMeta{Name: "self-update-scene", Namespace: "default"},
			Spec: hav1.HomeAssistantSceneSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "self_update_scene_id",
				Entities:         entities,
			},
		}
		g.Expect(k8sClient.Create(ctx, self)).To(Succeed())
		self.Spec.Icon = "mdi:movie"
		g.Expect(k8sClient.Update(ctx, self)).To(Succeed())
	})
}

// TestAdmissionWebhookScriptIdentifierCollision is the HomeAssistantScript
// equivalent of TestAdmissionWebhookAutomationIdentifierCollision.
func TestAdmissionWebhookScriptIdentifierCollision(t *testing.T) {
	g := NewWithT(t)
	k8sClient, _, cleanup := setupWebhookTestEnv(t)
	defer cleanup()
	ctx := context.Background()
	sequence := []hav1.ScriptAction{{RawExtension: runtime.RawExtension{Raw: []byte(`{"service":"backup.create"}`)}}}

	t.Run("explicit id collision with existing sibling is rejected and names it", func(t *testing.T) {
		first := &hav1.HomeAssistantScript{
			ObjectMeta: metav1.ObjectMeta{Name: "first-script", Namespace: "default"},
			Spec: hav1.HomeAssistantScriptSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "backup_now",
				Alias:            "Backup now",
				Sequence:         sequence,
			},
		}
		g.Expect(k8sClient.Create(ctx, first)).To(Succeed())

		second := first.DeepCopy()
		second.ObjectMeta = metav1.ObjectMeta{Name: "second-script", Namespace: "default"}
		err := k8sClient.Create(ctx, second)
		g.Expect(err).To(HaveOccurred(), "webhook should reject a colliding effective id")
		g.Expect(err.Error()).To(ContainSubstring("first-script"))
	})

	t.Run("name-fallback id collision is rejected", func(t *testing.T) {
		first := &hav1.HomeAssistantScript{
			ObjectMeta: metav1.ObjectMeta{Name: "sharedscriptname", Namespace: "default"},
			Spec: hav1.HomeAssistantScriptSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				Alias:            "First",
				Sequence:         sequence,
			},
		}
		g.Expect(k8sClient.Create(ctx, first)).To(Succeed())

		second := &hav1.HomeAssistantScript{
			ObjectMeta: metav1.ObjectMeta{Name: "second-script-explicit", Namespace: "default"},
			Spec: hav1.HomeAssistantScriptSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "sharedscriptname",
				Alias:            "Second",
				Sequence:         sequence,
			},
		}
		err := k8sClient.Create(ctx, second)
		g.Expect(err).To(HaveOccurred(), "webhook should catch a name-fallback collision, not just an explicit spec.id match")
		g.Expect(err.Error()).To(ContainSubstring("sharedscriptname"))
	})

	t.Run("same id, different HomeAssistant instance admits", func(t *testing.T) {
		other := &hav1.HomeAssistantScript{
			ObjectMeta: metav1.ObjectMeta{Name: "other-instance-script", Namespace: "default"},
			Spec: hav1.HomeAssistantScriptSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "away"},
				ID:               "backup_now",
				Alias:            "Also backup now, different instance",
				Sequence:         sequence,
			},
		}
		g.Expect(k8sClient.Create(ctx, other)).To(Succeed())
	})

	t.Run("sibling marked for deletion is not a conflict", func(t *testing.T) {
		deleting := &hav1.HomeAssistantScript{
			ObjectMeta: metav1.ObjectMeta{
				Name: "deleting-script", Namespace: "default",
				Finalizers: []string{"ha.homeassistant.io/test-hold"},
			},
			Spec: hav1.HomeAssistantScriptSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "recycled_script_id",
				Alias:            "Being deleted",
				Sequence:         sequence,
			},
		}
		g.Expect(k8sClient.Create(ctx, deleting)).To(Succeed())
		g.Expect(k8sClient.Delete(ctx, deleting)).To(Succeed())

		replacement := &hav1.HomeAssistantScript{
			ObjectMeta: metav1.ObjectMeta{Name: "replacement-script", Namespace: "default"},
			Spec: hav1.HomeAssistantScriptSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "recycled_script_id",
				Alias:            "Replacement",
				Sequence:         sequence,
			},
		}
		g.Expect(k8sClient.Create(ctx, replacement)).To(Succeed())
	})

	t.Run("update does not conflict with its own prior state", func(t *testing.T) {
		self := &hav1.HomeAssistantScript{
			ObjectMeta: metav1.ObjectMeta{Name: "self-update-script", Namespace: "default"},
			Spec: hav1.HomeAssistantScriptSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				ID:               "self_update_script_id",
				Alias:            "Self",
				Sequence:         sequence,
			},
		}
		g.Expect(k8sClient.Create(ctx, self)).To(Succeed())
		self.Spec.Alias = "Self, renamed"
		g.Expect(k8sClient.Update(ctx, self)).To(Succeed())
	})
}

// capturingWarningHandler records every admission warning surfaced by the
// API server, the same mechanism kubectl uses to print them to the user.
type capturingWarningHandler struct {
	messages []string
}

func (h *capturingWarningHandler) HandleWarningHeaderWithContext(_ context.Context, _ int, _, message string) {
	h.messages = append(h.messages, message)
}

// TestAdmissionWebhookConfigurationRecorderWarning exercises the real HTTP
// admission path for HomeAssistantConfigurationCustomValidator end to end,
// confirming the warning set in homeassistantconfiguration_webhook_test.go's
// pure-function tests actually reaches the client over the wire — the unit
// tests alone would not catch a registration/wiring bug.
func TestAdmissionWebhookConfigurationRecorderWarning(t *testing.T) {
	g := NewWithT(t)
	baseClient, cfg, cleanup := setupWebhookTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	// A HomeAssistantConfiguration always requires its own HomeAssistantRef
	// to point somewhere; existence is deliberately not checked at admission
	// time (see this feature's documented criteria), so no HomeAssistant
	// object needs to actually exist for this test.
	baseConfig := hav1.HomeAssistantConfigurationSpec{
		HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
		Configuration:    "homeassistant:\n",
	}

	t.Run("only database set produces no warning", func(t *testing.T) {
		handler := &capturingWarningHandler{}
		warnCfg := rest.CopyConfig(cfg)
		warnCfg.WarningHandlerWithContext = handler
		warnClient, err := client.New(warnCfg, client.Options{Scheme: baseClient.Scheme()})
		g.Expect(err).NotTo(HaveOccurred())

		obj := &hav1.HomeAssistantConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg-no-warning", Namespace: "default"},
			Spec:       baseConfig,
		}
		obj.Spec.Recorder = &hav1.RecorderConfig{Database: "sqlite:////config/home-assistant_v2.db"}
		g.Expect(warnClient.Create(ctx, obj)).To(Succeed())
		g.Expect(handler.messages).To(BeEmpty())
	})

	t.Run("both database and databaseSecretRef set produces a warning naming databaseSecretRef", func(t *testing.T) {
		handler := &capturingWarningHandler{}
		warnCfg := rest.CopyConfig(cfg)
		warnCfg.WarningHandlerWithContext = handler
		warnClient, err := client.New(warnCfg, client.Options{Scheme: baseClient.Scheme()})
		g.Expect(err).NotTo(HaveOccurred())

		obj := &hav1.HomeAssistantConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg-with-warning", Namespace: "default"},
			Spec:       baseConfig,
		}
		obj.Spec.Recorder = &hav1.RecorderConfig{
			Database:          "sqlite:////config/home-assistant_v2.db",
			DatabaseSecretRef: &hav1.SecretKeySelector{Name: "db-secret"},
		}
		g.Expect(warnClient.Create(ctx, obj)).To(Succeed())
		g.Expect(handler.messages).To(HaveLen(1))
		g.Expect(handler.messages[0]).To(ContainSubstring("databaseSecretRef"))
	})
}
