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

func runMigrationRegistryMatrixScenario(namespace, haName string) {
	api, cleanup := criticalPathHAClient(namespace, haName)
	defer cleanup()
	token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	uid := strings.TrimSpace(utils.Kubectl("get", "ha", haName, "-n", namespace, "-o", "jsonpath={.metadata.uid}"))
	Expect(uid).NotTo(BeEmpty())
	suffix := utils.RandomString(8)
	create := func(kind string, data map[string]interface{}) string {
		data["name"] = "migration-" + kind + "-" + suffix
		raw, err := api.SendWebSocketCommand(ctx, token, "config/"+kind+"_registry/create", data)
		Expect(err).NotTo(HaveOccurred())
		var object map[string]json.RawMessage
		Expect(json.Unmarshal(raw, &object)).To(Succeed())
		var id string
		Expect(json.Unmarshal(object[kind+"_id"], &id)).To(Succeed())
		Expect(id).NotTo(BeEmpty())
		return id
	}
	floorID := create("floor", map[string]interface{}{})
	labelID := create("label", map[string]interface{}{})
	areaID := create("area", map[string]interface{}{"floor_id": floorID, "labels": []string{labelID}})
	defer func() {
		for _, object := range []struct{ kind, id string }{{"area", areaID}, {"label", labelID}, {"floor", floorID}} {
			_, _ = api.SendWebSocketCommand(
				context.Background(),
				token,
				"config/"+object.kind+"_registry/delete",
				map[string]interface{}{object.kind + "_id": object.id},
			)
		}
	}()
	manifests := map[string]string{}
	for _, object := range []struct{ kind, id, extra string }{
		{"floor", floorID, ""},
		{"label", labelID, ""},
		{"area", areaID, fmt.Sprintf("  floorName: migration-floor-%s\n  labels:\n    - migration-label-%s\n", suffix, suffix)},
	} {
		crName := "migration-" + object.kind + "-" + suffix
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
  name: %q
%s`, strings.ToUpper(object.kind[:1])+object.kind[1:], crName, namespace, object.id, uid, haName, crName, object.extra)
		manifests[object.kind] = manifest
		Expect(utils.ApplyYAML(manifest, namespace)).To(Succeed())
		_, err := utils.Run(
			exec.Command(
				"kubectl",
				"annotate",
				"homeassistant"+object.kind,
				crName,
				"-n",
				namespace,
				"ha.homeassistant.io/migration-paused-",
			),
		)
		Expect(err).NotTo(HaveOccurred())
	}
	waitCondition := func(kind, reason string) {
		crName := "migration-" + kind + "-" + suffix
		Eventually(func(g Gomega) {
			value := utils.Kubectl("get", "homeassistant"+kind, crName, "-n", namespace, "-o", "json")
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
					g.Expect(condition.Reason).To(Equal(reason))
					if reason == "Adopted" {
						g.Expect(condition.Status).To(Equal("True"))
					} else {
						g.Expect(condition.Status).To(Equal("False"))
					}
					return
				}
			}
			g.Expect(false).To(BeTrue(), "MigrationAdoption condition missing")
		}, utils.StatusUpdateTimeout+time.Minute, reconcileInterval).Should(Succeed())
	}
	for _, kind := range []string{"floor", "label", "area"} {
		waitCondition(kind, "Adopted")
	}
	_, err := utils.Run(
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
	for _, kind := range []string{"floor", "label", "area"} {
		crName := "migration-" + kind + "-" + suffix
		_, err := utils.Run(
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
		waitCondition(kind, "Adopted")
	}
	for _, object := range []struct{ kind, id string }{{"area", areaID}, {"label", labelID}, {"floor", floorID}} {
		crName := "migration-" + object.kind + "-" + suffix
		changedName := crName + "-changed"
		_, err := api.SendWebSocketCommand(
			ctx,
			token,
			"config/"+object.kind+"_registry/update",
			map[string]interface{}{object.kind + "_id": object.id, "name": changedName},
		)
		Expect(err).NotTo(HaveOccurred())
		waitCondition(object.kind, "RegistryConflict")
		patch, err := json.Marshal(map[string]any{"spec": map[string]string{"name": changedName}})
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(
			exec.Command(
				"kubectl",
				"patch",
				"homeassistant"+object.kind,
				crName,
				"-n",
				namespace,
				"--type=merge",
				"-p",
				string(patch),
			),
		)
		Expect(err).NotTo(HaveOccurred())
		waitCondition(object.kind, "Adopted")
		_, err = utils.Run(
			exec.Command("kubectl", "delete", "homeassistant"+object.kind, crName, "-n", namespace, "--wait=true"),
		)
		Expect(err).NotTo(HaveOccurred())
		raw, err := api.SendWebSocketCommand(ctx, token, "config/"+object.kind+"_registry/list", nil)
		Expect(err).NotTo(HaveOccurred())
		var entries []map[string]json.RawMessage
		Expect(json.Unmarshal(raw, &entries)).To(Succeed())
		matches := 0
		for _, entry := range entries {
			var id string
			if json.Unmarshal(entry[object.kind+"_id"], &id) == nil && id == object.id {
				matches++
			}
		}
		Expect(matches).To(Equal(1))
		manifest := strings.Replace(manifests[object.kind], "  name: \""+crName+"\"", "  name: \""+changedName+"\"", 1)
		Expect(utils.ApplyYAML(manifest, namespace)).To(Succeed())
		_, err = utils.Run(
			exec.Command(
				"kubectl",
				"annotate",
				"homeassistant"+object.kind,
				crName,
				"-n",
				namespace,
				"ha.homeassistant.io/migration-paused-",
			),
		)
		Expect(err).NotTo(HaveOccurred())
		waitCondition(object.kind, "Adopted")
		_, err = api.SendWebSocketCommand(
			ctx,
			token,
			"config/"+object.kind+"_registry/delete",
			map[string]interface{}{object.kind + "_id": object.id},
		)
		Expect(err).NotTo(HaveOccurred())
		waitCondition(object.kind, "RegistryIdentityNotFound")
		_, err = utils.Run(
			exec.Command("kubectl", "delete", "homeassistant"+object.kind, crName, "-n", namespace, "--wait=true"),
		)
		Expect(err).NotTo(HaveOccurred())
	}
}
