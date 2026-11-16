package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/gomega"
	"github.com/przemekhys/homeassistant-operator/test/utils"
)

func runMigrationDefinitionMatrixScenario(namespace, haName string) {
	api, cleanup := criticalPathHAClient(namespace, haName)
	defer cleanup()
	token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	uid := strings.TrimSpace(utils.Kubectl("get", "ha", haName, "-n", namespace, "-o", "jsonpath={.metadata.uid}"))
	Expect(uid).NotTo(BeEmpty())
	suffix := strings.ToLower(utils.RandomString(8))
	for _, kind := range []string{"scene", "script"} {
		id := "migration_" + kind + "_" + suffix
		name := "Migration " + kind + " " + suffix
		var spec string
		if kind == "scene" {
			Expect(
				api.PutScene(
					ctx,
					token,
					id,
					map[string]interface{}{
						"id":   id,
						"name": name,
						"entities": map[string]interface{}{
							"light.migration_test": map[string]interface{}{"state": "on"},
						},
					},
				),
			).To(Succeed())
			spec = "  name: " + fmt.Sprintf(
				"%q",
				name,
			) + "\n  entities:\n    - entity_id: light.migration_test\n      state: 'on'\n"
			defer func() { _ = api.DeleteScene(context.Background(), token, id) }()
		} else {
			Expect(api.PutScript(ctx, token, id, map[string]interface{}{"alias": name, "mode": "single", "sequence": []interface{}{map[string]interface{}{"event": "migration_test"}}})).To(Succeed())
			spec = "  alias: " + fmt.Sprintf("%q", name) + "\n  mode: single\n  sequence:\n    - event: migration_test\n"
			defer func() { _ = api.DeleteScript(context.Background(), token, id) }()
		}
		crName := "migration-" + kind + "-" + suffix
		manifest := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
kind: HomeAssistant%s
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
  id: %q
%s`, strings.ToUpper(kind[:1])+kind[1:], crName, namespace, id, uid, haName, id, spec)
		Expect(utils.ApplyYAML(manifest, namespace)).To(Succeed())
		_, err := utils.Run(
			exec.Command(
				"kubectl",
				"annotate",
				"homeassistant"+kind,
				crName,
				"-n",
				namespace,
				"ha.homeassistant.io/migration-paused-",
			),
		)
		Expect(err).NotTo(HaveOccurred())
		waitCondition := func(reason string) {
			Eventually(func(g Gomega) {
				value := utils.Kubectl("get", "homeassistant"+kind, crName, "-n", namespace, "-o", "json")
				g.Expect(value).NotTo(ContainSubstring(token))
				var object struct {
					Status struct {
						Conditions []struct {
							Type   string `json:"type"`
							Status string `json:"status"`
							Reason string `json:"reason"`
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
				"homeassistant"+kind,
				crName,
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
		changedName := name + " changed"
		if kind == "scene" {
			Expect(
				api.PutScene(
					ctx,
					token,
					id,
					map[string]interface{}{
						"id":   id,
						"name": changedName,
						"entities": map[string]interface{}{
							"light.migration_test": map[string]interface{}{"state": "on"},
						},
					},
				),
			).To(Succeed())
		} else {
			Expect(api.PutScript(ctx, token, id, map[string]interface{}{"alias": changedName, "mode": "single", "sequence": []interface{}{map[string]interface{}{"event": "migration_test"}}})).To(Succeed())
		}
		waitCondition("CanonicalFieldsDiffer")
		field := "name"
		if kind == "script" {
			field = "alias"
		}
		patch, err := json.Marshal(map[string]any{"spec": map[string]string{field: changedName}})
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(
			exec.Command(
				"kubectl",
				"patch",
				"homeassistant"+kind,
				crName,
				"-n",
				namespace,
				"--type=merge",
				"-p",
				string(patch),
			),
		)
		Expect(err).NotTo(HaveOccurred())
		waitCondition("Adopted")
		_, err = utils.Run(
			exec.Command("kubectl", "delete", "homeassistant"+kind, crName, "-n", namespace, "--wait=true"),
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = api.ReadMigrationDefinition(ctx, token, kind, id)
		Expect(err).NotTo(HaveOccurred(), "HA definition must survive CR deletion")
		Expect(
			utils.ApplyYAML(
				strings.Replace(manifest, fmt.Sprintf("%q", name), fmt.Sprintf("%q", changedName), 1),
				namespace,
			),
		).To(Succeed())
		_, err = utils.Run(
			exec.Command(
				"kubectl",
				"annotate",
				"homeassistant"+kind,
				crName,
				"-n",
				namespace,
				"ha.homeassistant.io/migration-paused-",
			),
		)
		Expect(err).NotTo(HaveOccurred())
		waitCondition("Adopted")
		if kind == "scene" {
			Expect(api.DeleteScene(ctx, token, id)).To(Succeed())
		} else {
			Expect(api.DeleteScript(ctx, token, id)).To(Succeed())
		}
		waitCondition("DefinitionUnavailable")
		_, err = api.ReadMigrationDefinition(ctx, token, kind, id)
		Expect(err).To(HaveOccurred(), "missing HA definition must not be recreated")
		_, err = utils.Run(
			exec.Command("kubectl", "delete", "homeassistant"+kind, crName, "-n", namespace, "--wait=true"),
		)
		Expect(err).NotTo(HaveOccurred())
	}
}
