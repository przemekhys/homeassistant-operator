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

// runMigrationAdoptionScenario uses only the disposable HA created by the
// critical-path suite. The object is created directly in HA to exercise a
// manually prepared import before any kind is qualified for CLI import.
func runMigrationAdoptionScenario(namespace, haName string) {
	api, cleanup := criticalPathHAClient(namespace, haName)
	defer cleanup()
	token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	name := "migration-e2e-" + utils.RandomString(8)
	raw, err := api.SendWebSocketCommand(
		ctx,
		token,
		"config/floor_registry/create",
		map[string]interface{}{"name": name},
	)
	Expect(err).NotTo(HaveOccurred())
	var created struct {
		ID string `json:"floor_id"`
	}
	Expect(json.Unmarshal(raw, &created)).To(Succeed())
	Expect(created.ID).NotTo(BeEmpty())
	defer func() {
		_, _ = api.SendWebSocketCommand(
			context.Background(),
			token,
			"config/floor_registry/delete",
			map[string]interface{}{"floor_id": created.ID},
		)
	}()
	uid := strings.TrimSpace(utils.Kubectl("get", "ha", haName, "-n", namespace, "-o", "jsonpath={.metadata.uid}"))
	Expect(uid).NotTo(BeEmpty())
	crName := "migration-floor-" + utils.RandomString(8)
	manifest := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantFloor
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
  name: %q
`, crName, namespace, created.ID, uid, haName, name+"-different")
	Expect(utils.ApplyYAML(manifest, namespace)).To(Succeed())
	defer func() {
		_, _ = utils.Run(
			exec.Command("kubectl", "delete", "homeassistantfloor", crName, "-n", namespace, "--ignore-not-found=true"),
		)
	}()
	condition := func(g Gomega, expected string) {
		value := utils.Kubectl("get", "homeassistantfloor", crName, "-n", namespace, "-o", "json")
		g.Expect(value).NotTo(ContainSubstring(token), "CR and status must not expose the HA token")
		var object struct {
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Reason string `json:"reason"`
				} `json:"conditions"`
			} `json:"status"`
		}
		g.Expect(json.Unmarshal([]byte(value), &object)).To(Succeed())
		for _, item := range object.Status.Conditions {
			if item.Type == "MigrationAdoption" {
				g.Expect(item.Reason).To(Equal(expected))
				return
			}
		}
		g.Expect(false).To(BeTrue(), "MigrationAdoption condition missing")
	}
	Eventually(
		func(g Gomega) { condition(g, "MigrationPaused") },
		utils.StatusUpdateTimeout,
		reconcileInterval,
	).Should(Succeed())
	_, err = utils.Run(
		exec.Command(
			"kubectl",
			"annotate",
			"homeassistantfloor",
			crName,
			"-n",
			namespace,
			"ha.homeassistant.io/migration-paused-",
		),
	)
	Expect(err).NotTo(HaveOccurred())
	Eventually(
		func(g Gomega) { condition(g, "RegistryConflict") },
		utils.StatusUpdateTimeout,
		reconcileInterval,
	).Should(Succeed())
	patch, err := json.Marshal(map[string]any{"spec": map[string]string{"name": name}})
	Expect(err).NotTo(HaveOccurred())
	_, err = utils.Run(
		exec.Command(
			"kubectl",
			"patch",
			"homeassistantfloor",
			crName,
			"-n",
			namespace,
			"--type=merge",
			"-p",
			string(patch),
		),
	)
	Expect(err).NotTo(HaveOccurred())
	Eventually(
		func(g Gomega) { condition(g, "Adopted") },
		utils.StatusUpdateTimeout,
		reconcileInterval,
	).Should(Succeed())

	changedName := name + "-changed"
	_, err = api.SendWebSocketCommand(
		ctx,
		token,
		"config/floor_registry/update",
		map[string]interface{}{"floor_id": created.ID, "name": changedName},
	)
	Expect(err).NotTo(HaveOccurred())
	Eventually(
		func(g Gomega) { condition(g, "RegistryConflict") },
		utils.StatusUpdateTimeout+time.Minute,
		reconcileInterval,
	).Should(Succeed())
	patch, err = json.Marshal(map[string]any{"spec": map[string]string{"name": changedName}})
	Expect(err).NotTo(HaveOccurred())
	_, err = utils.Run(
		exec.Command(
			"kubectl",
			"patch",
			"homeassistantfloor",
			crName,
			"-n",
			namespace,
			"--type=merge",
			"-p",
			string(patch),
		),
	)
	Expect(err).NotTo(HaveOccurred())
	Eventually(
		func(g Gomega) { condition(g, "Adopted") },
		utils.StatusUpdateTimeout,
		reconcileInterval,
	).Should(Succeed())

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
			"homeassistantfloor",
			crName,
			"-n",
			namespace,
			"--subresource=status",
			"--type=merge",
			"-p",
			`{"status":{"conditions":[],"floorID":""}}`,
		),
	)
	Expect(err).NotTo(HaveOccurred())
	Eventually(
		func(g Gomega) { condition(g, "Adopted") },
		utils.StatusUpdateTimeout,
		reconcileInterval,
	).Should(Succeed())
	_, err = utils.Run(exec.Command("kubectl", "delete", "homeassistantfloor", crName, "-n", namespace, "--wait=true"))
	Expect(err).NotTo(HaveOccurred())
	raw, err = api.SendWebSocketCommand(ctx, token, "config/floor_registry/list", nil)
	Expect(err).NotTo(HaveOccurred())
	var floors []struct {
		ID   string `json:"floor_id"`
		Name string `json:"name"`
	}
	Expect(json.Unmarshal(raw, &floors)).To(Succeed())
	matches := 0
	for _, floor := range floors {
		if floor.ID == created.ID && floor.Name == changedName {
			matches++
		}
	}
	Expect(matches).To(Equal(1), "deleting the imported CR must preserve the original HA floor")

	// Adopt the same HA object again, then remove it in HA. The controller must
	// block instead of recreating it or matching an object by name.
	Expect(utils.ApplyYAML(strings.Replace(manifest, name+"-different", changedName, 1), namespace)).To(Succeed())
	_, err = utils.Run(
		exec.Command(
			"kubectl",
			"annotate",
			"homeassistantfloor",
			crName,
			"-n",
			namespace,
			"ha.homeassistant.io/migration-paused-",
		),
	)
	Expect(err).NotTo(HaveOccurred())
	Eventually(
		func(g Gomega) { condition(g, "Adopted") },
		utils.StatusUpdateTimeout,
		reconcileInterval,
	).Should(Succeed())
	_, err = api.SendWebSocketCommand(
		ctx,
		token,
		"config/floor_registry/delete",
		map[string]interface{}{"floor_id": created.ID},
	)
	Expect(err).NotTo(HaveOccurred())
	Eventually(
		func(g Gomega) { condition(g, "RegistryIdentityNotFound") },
		utils.StatusUpdateTimeout+time.Minute,
		reconcileInterval,
	).Should(Succeed())
	raw, err = api.SendWebSocketCommand(ctx, token, "config/floor_registry/list", nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(json.Unmarshal(raw, &floors)).To(Succeed())
	for _, floor := range floors {
		Expect(floor.ID).NotTo(Equal(created.ID))
		Expect(floor.Name).NotTo(Equal(changedName))
	}
}
