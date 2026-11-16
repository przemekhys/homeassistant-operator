package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/gomega"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/test/utils"
)

func runMigrationDashboardScenario(namespace, haName string) {
	api, cleanup := criticalPathHAClient(namespace, haName)
	defer cleanup()
	token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	suffix := strings.ToLower(utils.RandomString(8))
	path := "migration-" + suffix
	dashboard, err := api.CreateDashboard(
		ctx,
		token,
		haclient.Dashboard{
			URLPath:       path,
			Title:         "Migration Dashboard",
			Icon:          "mdi:view-dashboard",
			ShowInSidebar: true,
		},
	)
	Expect(err).NotTo(HaveOccurred())
	Expect(dashboard.ID).NotTo(BeEmpty())
	defer func() { _ = api.DeleteDashboard(context.Background(), token, dashboard.ID) }()
	Expect(api.SaveDashboardConfig(ctx, token, path, json.RawMessage(`{"views":[]}`))).To(Succeed())
	uid := strings.TrimSpace(utils.Kubectl("get", "ha", haName, "-n", namespace, "-o", "jsonpath={.metadata.uid}"))
	manifest := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantDashboard
metadata:
  name: %s
  namespace: %s
  annotations:
    ha.homeassistant.io/migration-imported: "true"
    ha.homeassistant.io/migration-paused: "true"
    ha.homeassistant.io/migration-source-id: %q
    ha.homeassistant.io/migration-target-uid: %q
spec:
  homeAssistantRef:
    name: %s
  title: Migration Dashboard
  urlPath: %s
  icon: mdi:view-dashboard
  showInSidebar: true
  requireAdmin: false
  inline: 'views: []'
`, path, namespace, dashboard.ID, uid, haName, path)
	Expect(utils.ApplyYAML(manifest, namespace)).To(Succeed())
	_, err = utils.Run(
		exec.Command(
			"kubectl",
			"annotate",
			"homeassistantdashboard",
			path,
			"-n",
			namespace,
			"ha.homeassistant.io/migration-paused-",
		),
	)
	Expect(err).NotTo(HaveOccurred())
	waitCondition := func(reason string) {
		Eventually(func(g Gomega) {
			value := utils.Kubectl("get", "homeassistantdashboard", path, "-n", namespace, "-o", "json")
			g.Expect(value).NotTo(ContainSubstring(token))
			var object struct {
				Status struct {
					Conditions []struct {
						Type   string `json:"type"`
						Reason string `json:"reason"`
						Status string `json:"status"`
					} `json:"conditions"`
				} `json:"status"`
			}
			g.Expect(json.Unmarshal([]byte(value), &object)).To(Succeed())
			for _, condition := range object.Status.Conditions {
				if condition.Type == "MigrationAdoption" {
					if reason == "Adopted" {
						g.Expect(condition.Status).To(Equal("True"))
					} else {
						g.Expect(condition.Status).To(Equal("False"))
					}
					g.Expect(condition.Reason).To(Equal(reason))
					return
				}
			}
			g.Expect(false).To(BeTrue(), "MigrationAdoption condition missing")
		}, utils.StatusUpdateTimeout+time.Minute, reconcileInterval).Should(Succeed())
	}
	waitCondition("Adopted")
	_, err = utils.Run(
		exec.Command(
			"kubectl",
			"rollout",
			"restart",
			"deployment/homeassistant-operator-controller-manager",
			"-n",
			"homeassistant-operator-system",
		),
	)
	Expect(err).NotTo(HaveOccurred())
	_, err = utils.Run(
		exec.Command(
			"kubectl",
			"rollout",
			"status",
			"deployment/homeassistant-operator-controller-manager",
			"-n",
			"homeassistant-operator-system",
			"--timeout=180s",
		),
	)
	Expect(err).NotTo(HaveOccurred())
	_, err = utils.Run(
		exec.Command(
			"kubectl",
			"patch",
			"homeassistantdashboard",
			path,
			"-n",
			namespace,
			"--subresource=status",
			"--type=merge",
			"-p",
			`{"status":{"conditions":[]}}`,
		),
	)
	Expect(err).NotTo(HaveOccurred())
	waitCondition("Adopted")
	changed := haclient.Dashboard{
		URLPath:       path,
		Title:         "Migration Dashboard Changed",
		Icon:          "mdi:view-dashboard",
		ShowInSidebar: true,
	}
	_, err = api.UpdateDashboard(ctx, token, dashboard.ID, changed)
	Expect(err).NotTo(HaveOccurred())
	waitCondition("DashboardMetadataConflict")
	_, err = utils.Run(
		exec.Command(
			"kubectl",
			"patch",
			"homeassistantdashboard",
			path,
			"-n",
			namespace,
			"--type=merge",
			"-p",
			`{"spec":{"title":"Migration Dashboard Changed"}}`,
		),
	)
	Expect(err).NotTo(HaveOccurred())
	waitCondition("Adopted")
	_, err = utils.Run(
		exec.Command("kubectl", "delete", "homeassistantdashboard", path, "-n", namespace, "--wait=true"),
	)
	Expect(err).NotTo(HaveOccurred())
	dashboards, err := api.ListDashboards(ctx, token)
	Expect(err).NotTo(HaveOccurred())
	found := false
	for _, existing := range dashboards {
		if existing.ID == dashboard.ID {
			found = true
		}
	}
	Expect(found).To(BeTrue(), "HA dashboard must survive CR deletion")
	Expect(
		utils.ApplyYAML(
			strings.Replace(manifest, "title: Migration Dashboard", "title: Migration Dashboard Changed", 1),
			namespace,
		),
	).To(Succeed())
	_, err = utils.Run(
		exec.Command(
			"kubectl",
			"annotate",
			"homeassistantdashboard",
			path,
			"-n",
			namespace,
			"ha.homeassistant.io/migration-paused-",
		),
	)
	Expect(err).NotTo(HaveOccurred())
	waitCondition("Adopted")
	Expect(api.DeleteDashboard(ctx, token, dashboard.ID)).To(Succeed())
	waitCondition("DashboardIdentityUnverified")
	dashboards, err = api.ListDashboards(ctx, token)
	Expect(err).NotTo(HaveOccurred())
	for _, existing := range dashboards {
		Expect(existing.ID).NotTo(Equal(dashboard.ID))
		Expect(existing.URLPath).NotTo(Equal(path))
	}
	_, err = utils.Run(
		exec.Command("kubectl", "delete", "homeassistantdashboard", path, "-n", namespace, "--wait=true"),
	)
	Expect(err).NotTo(HaveOccurred())
}
