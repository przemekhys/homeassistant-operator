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

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/przemekhys/homeassistant-operator/test/utils"
)

// haVersion returns the HA image tag to use in E2E tests.
// Set HA_VERSION env var in CI to pin a specific version; defaults to "stable".
func haVersion() string {
	if v := os.Getenv("HA_VERSION"); v != "" {
		return v
	}
	return "stable"
}

// Polling intervals shared across critical path tests.
const (
	reconcileInterval  = 2 * time.Second
	haPodReadyInterval = 10 * time.Second
	bootstrapInterval  = 10 * time.Second
)

var _ = Describe("Critical Path E2E", Ordered, ContinueOnFailure, func() {
	// Shared state across all tests — set in BeforeAll, used by every It block.
	var (
		namespace   string
		haName      string
		configName  string
		suiteFailed bool
	)

	// -------------------------------------------------------------------------
	// BeforeAll — single bootstrap for the entire suite
	// -------------------------------------------------------------------------
	BeforeAll(func() {
		namespace = "ha-e2e-critical-" + utils.RandomString(8)
		haName = "ha-critical"
		configName = "ha-critical-config"

		By("Creating test namespace: " + namespace)
		Expect(utils.CreateNamespace(namespace)).To(Succeed())

		By("Creating bootstrap credentials Secret")
		credsYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: ha-bootstrap-creds
  namespace: %s
type: Opaque
stringData:
  username: admin
  password: e2e-critical-path-pwd-123456
`, namespace)
		Expect(utils.ApplyYAML(credsYAML, namespace)).To(Succeed())

		By("Creating HomeAssistant CR with bootstrap and backup enabled")
		haYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistant
metadata:
  name: %s
  namespace: %s
spec:
  version: "%s"
  storage:
    size: "1Gi"
  bootstrap:
    enabled: true
    credentials:
      secretRef:
        name: ha-bootstrap-creds
    createApiToken: true
    apiTokenSecretName: %s-homeassistant-api-token
    ownerName: "E2E Critical Path"
    language: "en"
  backup:
    enabled: true
    recurrence: daily
    time: "03:00:00"
    retentionCopies: 3
    includeDatabase: true
  %s
`, haName, namespace, haVersion(), haName, utils.GetEnhancedHAResourceRequests())
		Expect(utils.ApplyYAML(haYAML, namespace)).To(Succeed())

		By("Creating HomeAssistantConfiguration CR")
		configYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantConfiguration
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  reloadStrategy: auto
  configuration: |
    automation: !include automations.yaml
    scene: !include scenes.yaml
    script: !include scripts.yaml
`, configName, namespace, haName)
		Expect(utils.ApplyYAML(configYAML, namespace)).To(Succeed())

		By("Waiting for bootstrap to complete")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "ha", haName, "-n", namespace,
				"-o", "jsonpath={.status.bootstrap.completed}")
			g.Expect(output).To(Equal("true"))
		}, utils.BootstrapTimeout, bootstrapInterval).Should(Succeed())

		By("Waiting for pod to be fully Ready")
		Eventually(func(g Gomega) {
			phase := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
				"-o", "jsonpath={.status.phase}")
			g.Expect(phase).To(Equal("Running"))

			ready := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			g.Expect(ready).To(Equal("True"))
		}, utils.HAPodReadyTimeout, haPodReadyInterval).Should(Succeed())

		By("Verifying API token Secret was created")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "secret", haName+"-homeassistant-api-token", "-n", namespace)
			g.Expect(output).NotTo(BeEmpty())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			suiteFailed = true
		}
	})

	AfterAll(func() {
		if suiteFailed {
			collectCriticalPathDebugInfo(namespace, haName, configName)
		}
		By("Deleting test namespace: " + namespace)
		if err := utils.DeleteNamespace(namespace); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "Warning: failed to delete namespace %s: %v\n", namespace, err)
		}
	})

	// -------------------------------------------------------------------------
	// 1. HomeAssistant — pod running and status ready
	// -------------------------------------------------------------------------
	It("HomeAssistant — pod running and status ready", func() {
		By("Verifying pod is Running and Ready")
		Eventually(func(g Gomega) {
			phase := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
				"-o", "jsonpath={.status.phase}")
			g.Expect(phase).To(Equal("Running"))

			ready := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			g.Expect(ready).To(Equal("True"))
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Verifying StatefulSet has 1/1 ready replicas")
		Eventually(func(g Gomega) {
			readyReplicas := utils.Kubectl("get", "statefulset", haName, "-n", namespace,
				"-o", "jsonpath={.status.readyReplicas}")
			g.Expect(readyReplicas).To(Equal("1"))
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Verifying Service exists with port 8123")
		Eventually(func(g Gomega) {
			port := utils.Kubectl("get", "service", haName, "-n", namespace,
				"-o", "jsonpath={.spec.ports[0].port}")
			g.Expect(port).To(Equal("8123"))
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())

		By("Verifying PVC exists")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "pvc", haName+"-data", "-n", namespace,
				"-o", "jsonpath={.status.phase}")
			g.Expect(output).To(Equal("Bound"))
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())

		By("Verifying HomeAssistant status shows bootstrap completed")
		Eventually(func(g Gomega) {
			completed := utils.Kubectl("get", "ha", haName, "-n", namespace,
				"-o", "jsonpath={.status.bootstrap.completed}")
			g.Expect(completed).To(Equal("true"))
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// 2. HomeAssistantConfiguration — hot-reload on config change
	// -------------------------------------------------------------------------
	It("HomeAssistantConfiguration — hot-reload on config change", func() {
		By("Capturing pod UID before config change")
		podUID := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
			"-o", "jsonpath={.metadata.uid}")
		Expect(podUID).NotTo(BeEmpty())

		By("Waiting for pod UID to be stable (no pending restarts)")
		Consistently(func(g Gomega) {
			uid := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
				"-o", "jsonpath={.metadata.uid}")
			g.Expect(uid).To(Equal(podUID))
		}, 10*time.Second, 2*time.Second).Should(Succeed())

		By("Capturing initial configHash")
		var initialHash string
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "haconfig", configName, "-n", namespace,
				"-o", "jsonpath={.status.configHash}")
			g.Expect(hash).NotTo(BeEmpty())
			initialHash = hash
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Updating HAConfig with reloadable change (logger section)")
		updateYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantConfiguration
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  reloadStrategy: auto
  configuration: |
    automation: !include automations.yaml
    scene: !include scenes.yaml
    script: !include scripts.yaml
    logger:
      default: info
`, configName, namespace, haName)
		Expect(utils.ApplyYAML(updateYAML, namespace)).To(Succeed())

		By("Verifying configHash changed")
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "haconfig", configName, "-n", namespace,
				"-o", "jsonpath={.status.configHash}")
			g.Expect(hash).NotTo(Equal(initialHash))
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Verifying lastReloadMethod is hot-reload")
		Eventually(func(g Gomega) {
			method := utils.Kubectl("get", "haconfig", configName, "-n", namespace,
				"-o", "jsonpath={.status.lastReloadMethod}")
			g.Expect(method).To(Equal("hot-reload"))
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Verifying pod was NOT restarted (UID unchanged)")
		uid := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
			"-o", "jsonpath={.metadata.uid}")
		Expect(uid).To(Equal(podUID))
	})

	// -------------------------------------------------------------------------
	// 3. HomeAssistantSecrets — generated secret with correct hash
	// -------------------------------------------------------------------------
	It("HomeAssistantSecrets — generated secret with correct hash", func() {
		sourceName := "cp-secrets-source"
		secretsName := "cp-hasecrets"

		By("Creating source K8s Secret")
		sourceYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: Opaque
stringData:
  mqtt_password: "test123"
`, sourceName, namespace)
		Expect(utils.ApplyYAML(sourceYAML, namespace)).To(Succeed())

		By("Creating HomeAssistantSecrets CR")
		haSecretsYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantSecrets
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  secretRefs:
    - name: %s
`, secretsName, namespace, haName, sourceName)
		Expect(utils.ApplyYAML(haSecretsYAML, namespace)).To(Succeed())

		By("Verifying generated Secret exists")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "secret", haName+"-generated-secrets", "-n", namespace)
			g.Expect(output).NotTo(BeEmpty())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())

		By("Verifying HomeAssistantSecrets status Ready=True")
		Eventually(func(g Gomega) {
			status := utils.Kubectl("get", "hasecrets", secretsName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			g.Expect(status).To(Equal("True"))
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Capturing initial secretsHash")
		var initialHash string
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "hasecrets", secretsName, "-n", namespace,
				"-o", "jsonpath={.status.secretsHash}")
			g.Expect(hash).NotTo(BeEmpty())
			initialHash = hash
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Updating source Secret value")
		updatedSourceYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: Opaque
stringData:
  mqtt_password: "updated456"
`, sourceName, namespace)
		Expect(utils.ApplyYAML(updatedSourceYAML, namespace)).To(Succeed())

		By("Verifying secretsHash changed after source Secret update")
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "hasecrets", secretsName, "-n", namespace,
				"-o", "jsonpath={.status.secretsHash}")
			g.Expect(hash).NotTo(Equal(initialHash))
		}, utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())

		By("Deleting HomeAssistantSecrets CR")
		cmd := exec.Command("kubectl", "delete", "hasecrets", secretsName, "-n", namespace, "--wait=true", "--timeout=30s")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	// -------------------------------------------------------------------------
	// 4. HomeAssistantAutomation — PUT, reload, and delete via REST API
	// -------------------------------------------------------------------------
	It("HomeAssistantAutomation — PUT, reload, and delete via REST API", func() {
		autoName := "cp-automation"

		By("Creating HomeAssistantAutomation CR")
		autoYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantAutomation
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  id: critical_path_auto
  alias: "Critical Path Automation"
  autoReload: true
  mode: single
  triggers:
    - platform: time
      at: "07:00:00"
  actions:
    - service: light.turn_on
      target:
        entity_id: light.living_room
`, autoName, namespace, haName)
		Expect(utils.ApplyYAML(autoYAML, namespace)).To(Succeed())

		By("Verifying Ready=True with reason AutomationGenerated")
		Eventually(func(g Gomega) {
			status := utils.Kubectl("get", "haauto", autoName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			g.Expect(status).To(Equal("True"))

			reason := utils.Kubectl("get", "haauto", autoName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].reason}")
			g.Expect(reason).To(Equal("AutomationGenerated"))
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Verifying automationHash is set (SHA256)")
		var initialHash string
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "haauto", autoName, "-n", namespace,
				"-o", "jsonpath={.status.automationHash}")
			g.Expect(hash).To(MatchRegexp("^[a-f0-9]{64}$"))
			initialHash = hash
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Verifying lastReloadTime is set")
		Eventually(func(g Gomega) {
			reloadTime := utils.Kubectl("get", "haauto", autoName, "-n", namespace,
				"-o", "jsonpath={.status.lastReloadTime}")
			g.Expect(reloadTime).NotTo(BeEmpty())
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Updating automation alias to trigger rehash and reload")
		updatedAutoYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantAutomation
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  id: critical_path_auto
  alias: "Updated Critical Path Automation"
  autoReload: true
  mode: single
  triggers:
    - platform: time
      at: "07:00:00"
  actions:
    - service: light.turn_on
      target:
        entity_id: light.living_room
`, autoName, namespace, haName)
		Expect(utils.ApplyYAML(updatedAutoYAML, namespace)).To(Succeed())

		By("Verifying automationHash changed after update")
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "haauto", autoName, "-n", namespace,
				"-o", "jsonpath={.status.automationHash}")
			g.Expect(hash).NotTo(Equal(initialHash))
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Deleting HomeAssistantAutomation CR")
		cmd := exec.Command("kubectl", "delete", "haauto", autoName, "-n", namespace, "--wait=false")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying CR is fully deleted (finalizer removed)")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "haauto", autoName, "-n", namespace, "--ignore-not-found")
			g.Expect(output).To(BeEmpty())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// 5. HomeAssistantScene — PUT, reload, and delete via REST API
	// -------------------------------------------------------------------------
	It("HomeAssistantScene — PUT, reload, and delete via REST API", func() {
		sceneName := "cp-scene"

		By("Creating HomeAssistantScene CR")
		sceneYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantScene
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  id: critical_path_scene
  name: "Critical Path Scene"
  autoReload: true
  entities:
    - entity_id: light.living_room
      state: "on"
      attributes:
        brightness: 200
        color_temp: 300
`, sceneName, namespace, haName)
		Expect(utils.ApplyYAML(sceneYAML, namespace)).To(Succeed())

		By("Verifying Ready=True with reason SceneGenerated")
		Eventually(func(g Gomega) {
			status := utils.Kubectl("get", "hascene", sceneName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			g.Expect(status).To(Equal("True"))

			reason := utils.Kubectl("get", "hascene", sceneName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].reason}")
			g.Expect(reason).To(Equal("SceneGenerated"))
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Verifying sceneHash is set (SHA256)")
		var initialHash string
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "hascene", sceneName, "-n", namespace,
				"-o", "jsonpath={.status.sceneHash}")
			g.Expect(hash).To(MatchRegexp("^[a-f0-9]{64}$"))
			initialHash = hash
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Verifying lastReloadTime is set")
		Eventually(func(g Gomega) {
			reloadTime := utils.Kubectl("get", "hascene", sceneName, "-n", namespace,
				"-o", "jsonpath={.status.lastReloadTime}")
			g.Expect(reloadTime).NotTo(BeEmpty())
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Updating scene to add another entity")
		updatedSceneYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantScene
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  id: critical_path_scene
  name: "Critical Path Scene"
  autoReload: true
  entities:
    - entity_id: light.living_room
      state: "on"
      attributes:
        brightness: 200
        color_temp: 300
    - entity_id: light.bedroom
      state: "off"
`, sceneName, namespace, haName)
		Expect(utils.ApplyYAML(updatedSceneYAML, namespace)).To(Succeed())

		By("Verifying sceneHash changed after update")
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "hascene", sceneName, "-n", namespace,
				"-o", "jsonpath={.status.sceneHash}")
			g.Expect(hash).NotTo(Equal(initialHash))
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Deleting HomeAssistantScene CR")
		cmd := exec.Command("kubectl", "delete", "hascene", sceneName, "-n", namespace, "--wait=false")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying CR is fully deleted (finalizer removed)")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "hascene", sceneName, "-n", namespace, "--ignore-not-found")
			g.Expect(output).To(BeEmpty())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// 6. HomeAssistantScript — PUT, reload, and delete via REST API
	// -------------------------------------------------------------------------
	It("HomeAssistantScript — PUT, reload, and delete via REST API", func() {
		scriptName := "cp-script"

		By("Creating HomeAssistantScript CR")
		scriptYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantScript
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  id: critical_path_script
  alias: "Critical Path Script"
  description: "E2E critical path test script"
  autoReload: true
  mode: single
  sequence:
    - service: light.turn_on
      target:
        entity_id: light.living_room
`, scriptName, namespace, haName)
		Expect(utils.ApplyYAML(scriptYAML, namespace)).To(Succeed())

		By("Verifying Ready=True with reason ScriptGenerated")
		Eventually(func(g Gomega) {
			status := utils.Kubectl("get", "hascript", scriptName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			g.Expect(status).To(Equal("True"))

			reason := utils.Kubectl("get", "hascript", scriptName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].reason}")
			g.Expect(reason).To(Equal("ScriptGenerated"))
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Verifying scriptHash is set (SHA256)")
		var initialHash string
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "hascript", scriptName, "-n", namespace,
				"-o", "jsonpath={.status.scriptHash}")
			g.Expect(hash).To(MatchRegexp("^[a-f0-9]{64}$"))
			initialHash = hash
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Verifying lastReloadTime is set")
		Eventually(func(g Gomega) {
			reloadTime := utils.Kubectl("get", "hascript", scriptName, "-n", namespace,
				"-o", "jsonpath={.status.lastReloadTime}")
			g.Expect(reloadTime).NotTo(BeEmpty())
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Updating script sequence to trigger rehash and reload")
		updatedScriptYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantScript
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  id: critical_path_script
  alias: "Critical Path Script"
  description: "E2E critical path test script"
  autoReload: true
  mode: single
  sequence:
    - service: light.turn_on
      target:
        entity_id: light.living_room
    - delay:
        seconds: 5
    - service: light.turn_off
      target:
        entity_id: light.living_room
`, scriptName, namespace, haName)
		Expect(utils.ApplyYAML(updatedScriptYAML, namespace)).To(Succeed())

		By("Verifying scriptHash changed after update")
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "hascript", scriptName, "-n", namespace,
				"-o", "jsonpath={.status.scriptHash}")
			g.Expect(hash).NotTo(Equal(initialHash))
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Deleting HomeAssistantScript CR")
		cmd := exec.Command("kubectl", "delete", "hascript", scriptName, "-n", namespace, "--wait=false")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying CR is fully deleted (finalizer removed)")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "hascript", scriptName, "-n", namespace, "--ignore-not-found")
			g.Expect(output).To(BeEmpty())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// 7. HomeAssistantIntegration — config flow and entryID
	// -------------------------------------------------------------------------
	It("HomeAssistantIntegration — config flow and entryID", func() {
		intName := "cp-integration"

		By("Creating HomeAssistantIntegration CR for moon (zero-config, single-step)")
		intYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantIntegration
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  domain: moon
  configuration:
    name:
      value: "Moon"
`, intName, namespace, haName)
		Expect(utils.ApplyYAML(intYAML, namespace)).To(Succeed())

		By("Verifying HA integration Ready=True")
		Eventually(func(g Gomega) {
			status := utils.Kubectl("get", "haint", intName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			g.Expect(status).To(Equal("True"))
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Verifying entryID is stored in status")
		Eventually(func(g Gomega) {
			entryID := utils.Kubectl("get", "haint", intName, "-n", namespace,
				"-o", "jsonpath={.status.entryID}")
			g.Expect(entryID).NotTo(BeEmpty())
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Verifying configHash is set (SHA256)")
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "haint", intName, "-n", namespace,
				"-o", "jsonpath={.status.configHash}")
			g.Expect(hash).To(MatchRegexp("^[a-f0-9]{64}$"))
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Deleting HomeAssistantIntegration CR")
		cmd := exec.Command("kubectl", "delete", "haint", intName, "-n", namespace, "--wait=false")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying CR is fully deleted (finalizer removed config entry)")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "haint", intName, "-n", namespace, "--ignore-not-found")
			g.Expect(output).To(BeEmpty())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// 8. HomeAssistantFloor — create via API and delete
	// -------------------------------------------------------------------------
	It("HomeAssistantFloor — create via API and delete", func() {
		floorName := "cp-floor"

		By("Creating HomeAssistantFloor CR")
		floorYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantFloor
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  name: "Ground Floor"
  level: 0
  icon: "mdi:home-floor-0"
`, floorName, namespace, haName)
		Expect(utils.ApplyYAML(floorYAML, namespace)).To(Succeed())

		By("Verifying finalizer is added")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "hafloor", floorName, "-n", namespace,
				"-o", "jsonpath={.metadata.finalizers}")
			g.Expect(output).To(ContainSubstring("ha.homeassistant.io/floor-finalizer"))
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())

		By("Verifying Ready=True")
		Eventually(func(g Gomega) {
			status := utils.Kubectl("get", "hafloor", floorName, "-n", namespace,
				"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
			g.Expect(status).To(Equal("True"))
		}, utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())

		By("Deleting HomeAssistantFloor CR")
		cmd := exec.Command("kubectl", "delete", "hafloor", floorName, "-n", namespace, "--wait=false")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying CR is fully deleted")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "hafloor", floorName, "-n", namespace, "--ignore-not-found")
			g.Expect(output).To(BeEmpty())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// 9. HomeAssistantLabel — create via API and delete
	// -------------------------------------------------------------------------
	It("HomeAssistantLabel — create via API and delete", func() {
		labelName := "cp-label"

		By("Creating HomeAssistantLabel CR")
		labelYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantLabel
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  name: "Critical Path Label"
  icon: "mdi:tag"
  color: "blue"
`, labelName, namespace, haName)
		Expect(utils.ApplyYAML(labelYAML, namespace)).To(Succeed())

		By("Verifying finalizer is added")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "halabel", labelName, "-n", namespace,
				"-o", "jsonpath={.metadata.finalizers}")
			g.Expect(output).To(ContainSubstring("ha.homeassistant.io/label-finalizer"))
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())

		By("Verifying Ready=True")
		Eventually(func(g Gomega) {
			status := utils.Kubectl("get", "halabel", labelName, "-n", namespace,
				"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
			g.Expect(status).To(Equal("True"))
		}, utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())

		By("Deleting HomeAssistantLabel CR")
		cmd := exec.Command("kubectl", "delete", "halabel", labelName, "-n", namespace, "--wait=false")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying CR is fully deleted")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "halabel", labelName, "-n", namespace, "--ignore-not-found")
			g.Expect(output).To(BeEmpty())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// 10. HomeAssistantArea — create via API and delete
	// -------------------------------------------------------------------------
	It("HomeAssistantArea — create via API and delete", func() {
		areaName := "cp-area"

		By("Creating HomeAssistantArea CR")
		areaYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantArea
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  name: "Living Room"
  icon: "mdi:sofa"
`, areaName, namespace, haName)
		Expect(utils.ApplyYAML(areaYAML, namespace)).To(Succeed())

		By("Verifying finalizer is added")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "haarea", areaName, "-n", namespace,
				"-o", "jsonpath={.metadata.finalizers}")
			g.Expect(output).To(ContainSubstring("ha.homeassistant.io/area-finalizer"))
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())

		By("Verifying Ready=True")
		Eventually(func(g Gomega) {
			status := utils.Kubectl("get", "haarea", areaName, "-n", namespace,
				"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
			g.Expect(status).To(Equal("True"))
		}, utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())

		By("Deleting HomeAssistantArea CR")
		cmd := exec.Command("kubectl", "delete", "haarea", areaName, "-n", namespace, "--wait=false")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying CR is fully deleted")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "haarea", areaName, "-n", namespace, "--ignore-not-found")
			g.Expect(output).To(BeEmpty())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
	})

})

// collectCriticalPathDebugInfo gathers diagnostic information on failure.
func collectCriticalPathDebugInfo(namespace, haName, configName string) {
	write := func(format string, args ...any) {
		_, _ = fmt.Fprintf(GinkgoWriter, format, args...)
	}
	write("\n=== Critical Path Debug Info (namespace: %s) ===\n", namespace)

	write("\n--- HomeAssistant CR ---\n")
	write("%s\n", utils.Kubectl("get", "ha", haName, "-n", namespace, "-o", "yaml"))

	write("\n--- HomeAssistantConfiguration ---\n")
	write("%s\n", utils.Kubectl("describe", "haconfig", configName, "-n", namespace))

	write("\n--- Pods ---\n")
	write("%s\n", utils.Kubectl("get", "pods", "-n", namespace, "-o", "wide"))

	write("\n--- Pod logs (%s-0) ---\n", haName)
	cmd := exec.Command("kubectl", "logs", haName+"-0", "-n", namespace, "--tail=100")
	out, err := utils.Run(cmd)
	if err == nil {
		write("%s\n", out)
	}

	write("\n--- Controller logs ---\n")
	cmd = exec.Command("kubectl", "logs", "-n", "homeassistant-operator-system",
		"-l", "control-plane=controller-manager", "--tail=200")
	out, err = utils.Run(cmd)
	if err == nil {
		write("%s\n", out)
	}

	write("\n--- All CRs ---\n")
	kinds := []string{
		"ha", "haconfig", "hasecrets", "haauto", "hascene",
		"hascript", "haint", "hafloor", "halabel", "haarea",
	}
	for _, kind := range kinds {
		write("  %s: %s\n", kind, utils.Kubectl("get", kind, "-n", namespace, "--ignore-not-found"))
	}

	write("\n--- Recent events ---\n")
	write("%s\n", utils.Kubectl("get", "events", "-n", namespace, "--sort-by=.lastTimestamp"))
}
