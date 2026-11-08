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
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
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

// Query the native recorder without printing history or other database contents.
const recorderHistoryProbe = `import sqlite3, sys
try:
    db = sqlite3.connect('file:/config/home-assistant_v2.db?mode=ro', uri=True)
    row = db.execute(
        "SELECT 1 FROM states JOIN states_meta USING (metadata_id) "
        "WHERE states_meta.entity_id = ? AND states.state = '0' LIMIT 1",
        (sys.argv[1],),
    ).fetchone()
    sys.exit(0 if bool(row) == (sys.argv[2] == 'present') else 1)
except sqlite3.Error:
    sys.exit(2)
`

var _ = Describe("Critical Path E2E", Ordered, ContinueOnFailure, Label("critical-path", "group-a"), func() {
	// Shared state across all tests — set in BeforeAll, used by every It block.
	var (
		namespace           string
		haName              string
		configName          string
		localTodoEntryID    string
		localTodoEntity     string
		localTodoItem       string
		retainedKeySecret   string
		retainedFirstKey    string
		retainedSecondKey   string
		retainedFirstValue  string
		retainedSecondValue string
		suiteFailed         bool
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
		haYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
    operatorAccount:
      mode: dedicated
      tokenSecretName: %s-homeassistant-api-token
    ownerName: "E2E Critical Path"
    language: "en"
    location:
      country: PL
  backup:
    enabled: true
    recurrence: daily
    time: "03:00:00"
    retentionCopies: 3
    includeDatabase: true
  %s
`, haName, namespace, haVersion(), haName, haName, utils.GetEnhancedHAResourceRequests())
		Expect(utils.ApplyYAML(haYAML, namespace)).To(Succeed())

		By("Creating HomeAssistantConfiguration CR")
		configYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantConfiguration
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  reloadStrategy: auto
  configuration: |
    recorder:
    history:
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
		defer func() {
			By("Deleting test namespace: " + namespace)
			if err := utils.DeleteNamespace(namespace); err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Warning: failed to delete namespace %s: %v\n", namespace, err)
			}
		}()
		if suiteFailed {
			collectCriticalPathDebugInfo(namespace, haName, configName)
		}
		if retainedKeySecret != "" {
			By("verifying retained backup keys after deleting the shared HomeAssistant")
			_, err := utils.Run(exec.Command("kubectl", "delete", "ha", haName, "-n", namespace, "--wait=true"))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				secretJSON := utils.Kubectl("get", "secret", retainedKeySecret, "-n", namespace, "-o", "json")
				var secret struct {
					Data map[string]string `json:"data"`
				}
				g.Expect(json.Unmarshal([]byte(secretJSON), &secret)).To(Succeed())
				g.Expect(secret.Data).To(HaveKey(retainedFirstKey))
				g.Expect(secret.Data).To(HaveKey(retainedSecondKey))
				first, firstErr := base64.StdEncoding.DecodeString(secret.Data[retainedFirstKey])
				g.Expect(firstErr).NotTo(HaveOccurred())
				firstMatches := string(first) == retainedFirstValue
				g.Expect(firstMatches).To(BeTrue())
				second, secondErr := base64.StdEncoding.DecodeString(secret.Data[retainedSecondKey])
				g.Expect(secondErr).NotTo(HaveOccurred())
				secondMatches := string(second) == retainedSecondValue
				g.Expect(secondMatches).To(BeTrue())
				g.Expect(secret.Data).NotTo(HaveKey(""))
			}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
		}
	})

	It("bootstrap country is persisted in Home Assistant /api/config", func() {
		apiClient, cleanup := criticalPathHAClient(namespace, haName)
		defer cleanup()
		token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
		Eventually(func(g Gomega) {
			config, err := apiClient.GetConfig(context.Background(), token)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(config.Country).To(Equal("PL"))
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

	})

	It("creates Local To-do recovery control data outside operator CRs", Label("backup"), func() {
		endpoint, cleanup := criticalPathHAEndpoint(namespace, haName)
		defer cleanup()
		apiClient := haclient.NewClient(endpoint)
		Eventually(func() error {
			return apiClient.CheckHealth(context.Background())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
		token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
		title := "Recovery " + utils.RandomString(10)

		By("creating a native Local To-do config entry without a CR")
		flow, err := apiClient.StartConfigFlow(context.Background(), token, "local_todo")
		Expect(err).NotTo(HaveOccurred())
		Expect(flow.FlowID).NotTo(BeEmpty())
		completed, err := apiClient.SubmitConfigFlow(context.Background(), token, flow.FlowID,
			map[string]interface{}{"todo_list_name": title})
		Expect(err).NotTo(HaveOccurred())
		Expect(completed.Type).To(Equal("create_entry"))

		By("recording the config entry and stable To-do entity identity")
		Eventually(func(g Gomega) {
			entries, listErr := apiClient.ListConfigEntries(context.Background(), token)
			g.Expect(listErr).NotTo(HaveOccurred())
			for _, entry := range entries {
				if entry.Domain == "local_todo" && entry.Title == title {
					localTodoEntryID = entry.EntryID
					break
				}
			}
			g.Expect(localTodoEntryID).NotTo(BeEmpty())
			states, stateErr := utils.ListHAStates(context.Background(), nil, endpoint, token)
			g.Expect(stateErr).NotTo(HaveOccurred())
			var found bool
			localTodoEntity, found = utils.LocalTodoEntity(states, title)
			g.Expect(found).To(BeTrue())
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("adding a Local To-do control item")
		localTodoItem = "recovery-control-" + utils.RandomString(12)
		Expect(utils.AddLocalTodoItem(context.Background(), nil, endpoint, token,
			localTodoEntity, localTodoItem)).To(Succeed())
		By("waiting for the local recorder to persist the To-do state")
		Eventually(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return exec.CommandContext(ctx, "kubectl", "exec", haName+"-0", "-n", namespace, "--",
				"python3", "-c", recorderHistoryProbe, localTodoEntity, "present").Run()
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())
	})

	It("creates one native backup from an explicit request", Label("backup"), func() {
		By("disabling the native schedule while preserving one-shot backups")
		_, err := utils.Run(exec.Command(
			"kubectl", "patch", "ha", haName, "-n", namespace, "--type=merge",
			"-p", `{"spec":{"backup":{"enabled":true,"recurrence":"never",`+
				`"retentionCopies":3,"includeDatabase":true,"agentIDs":["backup.local"]}}}`,
		))
		Expect(err).NotTo(HaveOccurred())

		token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
		apiClient, cleanup := criticalPathHAClient(namespace, haName)
		defer cleanup()
		requestID := "e2e-backup-" + utils.RandomString(12)
		By("requesting one backup through the durable annotation")
		_, err = utils.Run(exec.Command(
			"kubectl", "annotate", "ha", haName, "-n", namespace,
			"ha.homeassistant.io/backup-request="+requestID, "--overwrite",
		))
		Expect(err).NotTo(HaveOccurred())

		var backupID string
		Eventually(func(g Gomega) {
			receipt := utils.Kubectl("get", "ha", haName, "-n", namespace,
				"-o", "jsonpath={.metadata.annotations.ha\\.homeassistant\\.io/backup-receipt}")
			g.Expect(receipt).To(ContainSubstring(requestID))
			var parsed struct {
				Phase    string `json:"phase"`
				BackupID string `json:"backupID"`
			}
			g.Expect(json.Unmarshal([]byte(receipt), &parsed)).To(Succeed())
			g.Expect(parsed.Phase).To(Or(Equal("Accepted"), Equal("Succeeded")))
			g.Expect(parsed.BackupID).NotTo(BeEmpty())
			backupID = parsed.BackupID
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("observing the native archive identified by the request receipt")
		Eventually(func(g Gomega) {
			info, getErr := apiClient.GetBackupInfo(context.Background(), token)
			g.Expect(getErr).NotTo(HaveOccurred())
			for _, archive := range info.Backups {
				if archive.ID == backupID {
					return
				}
			}
			g.Expect(info.Backups).To(ContainElement(HaveField("ID", backupID)))
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("streaming the archive from the native local agent without exporting it")
		response, downloadErr := apiClient.DownloadBackup(context.Background(), token, backupID, "backup.local")
		Expect(downloadErr).NotTo(HaveOccurred())
		defer func() { _ = response.Body.Close() }()
		bytesRead, copyErr := io.Copy(io.Discard, response.Body)
		Expect(copyErr).NotTo(HaveOccurred())
		Expect(bytesRead).To(BeNumerically(">", 0))
	})

	It("observes a native scheduled backup independently of a request", Label("backup"), func() {
		token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
		apiClient, cleanup := criticalPathHAClient(namespace, haName)
		defer cleanup()
		config, err := apiClient.GetConfig(context.Background(), token)
		Expect(err).NotTo(HaveOccurred())
		priorReceipt := utils.Kubectl("get", "ha", haName, "-n", namespace,
			"-o", "jsonpath={.metadata.annotations.ha\\.homeassistant\\.io/backup-receipt}")
		var priorRequest struct {
			RequestID string `json:"requestID"`
		}
		Expect(json.Unmarshal([]byte(priorReceipt), &priorRequest)).To(Succeed())
		location := time.UTC
		if config.TimeZone != "" {
			location, err = time.LoadLocation(config.TimeZone)
			Expect(err).NotTo(HaveOccurred())
		}
		// Keep enough lead time for the controller to apply the configuration,
		// but avoid waiting for a daily cycle.
		target := time.Now().In(location).Add(90 * time.Second).Truncate(time.Second)
		By("scheduling the nearest native backup in Home Assistant's timezone")
		_, err = utils.Run(exec.Command(
			"kubectl", "patch", "ha", haName, "-n", namespace, "--type=merge",
			"-p", fmt.Sprintf(`{"spec":{"backup":{"enabled":true,"recurrence":"daily","time":"%s",`+
				`"retentionCopies":3,"includeDatabase":true,"agentIDs":["backup.local"]}}}`, target.Format("15:04:05")),
		))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_, _ = utils.Run(exec.Command(
				"kubectl", "patch", "ha", haName, "-n", namespace, "--type=merge",
				"-p", `{"spec":{"backup":{"enabled":true,"recurrence":"never",`+
					`"retentionCopies":3,"includeDatabase":true,"agentIDs":["backup.local"]}}}`,
			))
		})

		By("waiting for a new automatic archive rather than creating a request")
		var scheduledID string
		var scheduledDate time.Time
		Eventually(func(g Gomega) {
			info, getErr := apiClient.GetBackupInfo(context.Background(), token)
			g.Expect(getErr).NotTo(HaveOccurred())
			for _, archive := range info.Backups {
				if archive.Automatic && archive.Date >= target.Add(-time.Minute).Format(time.RFC3339) {
					scheduledID = archive.ID
					var parseErr error
					scheduledDate, parseErr = time.Parse(time.RFC3339, archive.Date)
					g.Expect(parseErr).NotTo(HaveOccurred())
					return
				}
			}
			g.Expect(scheduledID).NotTo(BeEmpty())
		}, 4*time.Minute, reconcileInterval).Should(Succeed())

		By("checking that backup status keeps the schedule separate from a request")
		Eventually(func(g Gomega) {
			lastSuccessful := utils.Kubectl("get", "ha", haName, "-n", namespace,
				"-o", "jsonpath={.status.backup.lastSuccessfulBackupTime}")
			g.Expect(lastSuccessful).NotTo(BeEmpty())
			observedDate, parseErr := time.Parse(time.RFC3339, lastSuccessful)
			g.Expect(parseErr).NotTo(HaveOccurred())
			g.Expect(utils.BackupStatusCoversArchiveDate(observedDate, scheduledDate)).To(BeTrue())
			receipt := utils.Kubectl("get", "ha", haName, "-n", namespace,
				"-o", "jsonpath={.metadata.annotations.ha\\.homeassistant\\.io/backup-receipt}")
			var currentRequest struct {
				RequestID string `json:"requestID"`
			}
			g.Expect(json.Unmarshal([]byte(receipt), &currentRequest)).To(Succeed())
			g.Expect(currentRequest.RequestID).To(Equal(priorRequest.RequestID), "schedule must not create a one-shot request")
		}, 3*time.Minute, reconcileInterval).Should(Succeed())
		Expect(scheduledID).NotTo(BeEmpty())
	})

	It("recovers a protected native archive onto a fresh isolated Container", Label("backup"), func() {
		Expect(localTodoEntryID).NotTo(BeEmpty())
		Expect(localTodoEntity).NotTo(BeEmpty())
		Expect(localTodoItem).NotTo(BeEmpty())
		_, err := utils.Run(exec.Command(
			"kubectl", "patch", "ha", haName, "-n", namespace, "--type=merge",
			"-p", `{"spec":{"backup":{"enabled":false}}}`,
		))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string {
			return utils.Kubectl("get", "ha", haName, "-n", namespace,
				"-o", `jsonpath={.status.conditions[?(@.type=="BackupConfigured")].reason}`)
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Equal("Unmanaged"))
		Eventually(func() string {
			return utils.Kubectl("get", "ha", haName, "-n", namespace,
				"-o", `jsonpath={.status.conditions[?(@.type=="BackupObservationReady")].reason}`)
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Equal("Observed"))
		endpoint, cleanup := criticalPathHAEndpoint(namespace, haName)
		defer cleanup()
		source := haclient.NewClient(endpoint)
		Eventually(func() error { return source.CheckHealth(context.Background()) },
			utils.ResourceTimeout, reconcileInterval).Should(Succeed())
		token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
		key := "e2e-recovery-" + utils.RandomString(24)
		Expect(utils.SetNativeBackupPassword(context.Background(), endpoint, token, key)).To(Succeed())
		Eventually(func() bool {
			config, getErr := source.GetBackupConfig(context.Background(), token)
			return getErr == nil && config.CreateBackup.Password != nil && *config.CreateBackup.Password == key
		}, utils.ResourceTimeout, reconcileInterval).Should(BeTrue())
		_, err = utils.Run(exec.Command("kubectl", "annotate", "ha", haName, "-n", namespace,
			"ha.homeassistant.io/key-observation=recovery", "--overwrite"))
		Expect(err).NotTo(HaveOccurred())

		var secretName, secretKey string
		Eventually(func(g Gomega) {
			resource := utils.Kubectl("get", "ha", haName, "-n", namespace, "-o", "json")
			var ha struct {
				Status struct {
					Backup struct {
						KeySecretRef *struct {
							Name string `json:"name"`
							Key  string `json:"key"`
						} `json:"keySecretRef"`
					} `json:"backup"`
					Conditions []struct {
						Type   string `json:"type"`
						Reason string `json:"reason"`
					} `json:"conditions"`
				} `json:"status"`
			}
			g.Expect(json.Unmarshal([]byte(resource), &ha)).To(Succeed())
			var keyProtectionReason string
			for _, condition := range ha.Status.Conditions {
				if condition.Type == "BackupKeyProtected" {
					keyProtectionReason = condition.Reason
				}
			}
			g.Expect(keyProtectionReason).To(Equal("Protected"))
			g.Expect(ha.Status.Backup.KeySecretRef).NotTo(BeNil())
			secretName, secretKey = ha.Status.Backup.KeySecretRef.Name, ha.Status.Backup.KeySecretRef.Key
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
		secretJSON, getErr := exec.Command("kubectl", "get", "secret", secretName, "-n", namespace, "-o", "json").Output()
		Expect(getErr).NotTo(HaveOccurred())
		var secret struct {
			Data map[string]string `json:"data"`
		}
		Expect(json.Unmarshal(secretJSON, &secret)).To(Succeed())
		recoveryKey, decodeErr := base64.StdEncoding.DecodeString(secret.Data[secretKey])
		Expect(decodeErr).NotTo(HaveOccurred())
		keyMatches := string(recoveryKey) == key
		Expect(keyMatches).To(BeTrue(), "retained recovery key does not match")

		for _, includeDatabase := range []bool{true, false} {
			By(fmt.Sprintf("creating and recovering a protected archive with includeDatabase=%t", includeDatabase))
			Expect(source.ConfigureBackup(context.Background(), token, &haclient.BackupConfigRequest{
				CreateBackup: &haclient.BackupCreateConfig{AgentIDs: []string{"backup.local"}, IncludeDatabase: &includeDatabase},
			})).To(Succeed())
			previous, previousErr := source.GetBackupInfo(context.Background(), token)
			Expect(previousErr).NotTo(HaveOccurred())
			seen := make(map[string]bool)
			for _, archive := range previous.Backups {
				seen[archive.ID] = true
			}
			_, err = source.GenerateBackupWithAutomaticSettings(context.Background(), token)
			Expect(err).NotTo(HaveOccurred())
			var backupID string
			Eventually(func(g Gomega) {
				info, infoErr := source.GetBackupInfo(context.Background(), token)
				g.Expect(infoErr).NotTo(HaveOccurred())
				for _, archive := range info.Backups {
					if !seen[archive.ID] && archive.Agents["backup.local"].Protected {
						backupID = archive.ID
						return
					}
				}
				g.Expect(backupID).NotTo(BeEmpty())
			}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

			workspace, workspaceErr := utils.NewRecoveryWorkspace("")
			Expect(workspaceErr).NotTo(HaveOccurred())
			DeferCleanup(func() {
				args, cleanupErr := utils.RecoveryCleanupArgs(workspace, os.Getuid(), os.Getgid())
				Expect(cleanupErr).NotTo(HaveOccurred())
				_, cleanupErr = utils.Run(exec.Command("docker", args...))
				Expect(cleanupErr).NotTo(HaveOccurred())
				Expect(workspace.Cleanup()).To(Succeed())
			})
			downloadURL, urlErr := utils.NativeBackupDownloadURL(endpoint, backupID, "backup.local")
			Expect(urlErr).NotTo(HaveOccurred())
			_, downloadErr := utils.DownloadProtectedBackup(
				context.Background(), nil, downloadURL, "Bearer "+token, workspace.Archive, true)
			Expect(downloadErr).NotTo(HaveOccurred())

			network := "ha-recovery-" + utils.RandomString(10)
			// Docker cannot publish a loopback port from an --internal-only network.
			// This private bridge has no published address except RecoveryContainerArgs'
			// loopback binding; the target never shares the source's network or mounts.
			_, err = utils.Run(exec.Command("docker", "network", "create", network))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _, _ = utils.Run(exec.Command("docker", "network", "rm", network)) })
			container := "ha-recovery-" + utils.RandomString(10)
			DeferCleanup(func() { _, _ = utils.Run(exec.Command("docker", "rm", "--force", container)) })
			args, argsErr := utils.RecoveryContainerArgs(container, network, workspace.ConfigDir,
				"ghcr.io/home-assistant/home-assistant:"+haVersion())
			Expect(argsErr).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("docker", args...))
			Expect(err).NotTo(HaveOccurred())
			recoveryEndpoint := "http://127.0.0.1:18124"
			checkOnboarding := func() error { return utils.OnboardingBackupInfo(context.Background(), nil, recoveryEndpoint) }
			Eventually(checkOnboarding, utils.HAPodReadyTimeout, reconcileInterval).Should(Succeed())
			Consistently(checkOnboarding, 10*time.Second, reconcileInterval).Should(Succeed())
			Expect(utils.UploadBackupForOnboarding(context.Background(), nil, recoveryEndpoint, workspace.Archive)).To(Succeed())
			Expect(utils.OnboardingBackupInfo(context.Background(), nil, recoveryEndpoint)).To(Succeed())
			wrongKeyErr := utils.RestoreOnboardingBackup(context.Background(), nil, recoveryEndpoint,
				backupID, "backup.local", "incorrect-"+utils.RandomString(12))
			Expect(wrongKeyErr).To(HaveOccurred())
			Expect(wrongKeyErr.Error()).NotTo(ContainSubstring("incorrect-"))
			Expect(utils.RestoreOnboardingBackup(context.Background(), nil, recoveryEndpoint,
				backupID, "backup.local", string(recoveryKey))).To(Succeed())

			By("checking recovered Local To-do identity and control item without reusing source credentials")
			Eventually(func(g Gomega) {
				entryFound, entryErr := utils.RecoveredStorageContains(workspace.ConfigDir, "core.config_entries", localTodoEntryID)
				g.Expect(entryErr).NotTo(HaveOccurred())
				g.Expect(entryFound).To(BeTrue())
				entityFound, entityErr := utils.RecoveredStorageContains(
					workspace.ConfigDir, "core.entity_registry", localTodoEntity)
				g.Expect(entityErr).NotTo(HaveOccurred())
				g.Expect(entityFound).To(BeTrue())
				itemFound, itemErr := utils.RecoveredStorageContains(workspace.ConfigDir, "local_todo.*.ics", localTodoItem)
				g.Expect(itemErr).NotTo(HaveOccurred())
				g.Expect(itemFound).To(BeTrue())
			}, utils.HAPodReadyTimeout, reconcileInterval).Should(Succeed())
			By("checking recorder history on the recovered target")
			want := "absent"
			if includeDatabase {
				want = "present"
			}
			Eventually(func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return exec.CommandContext(ctx, "docker", "exec", container, "python3", "-c",
					recorderHistoryProbe, localTodoEntity, want).Run()
			}, utils.HAPodReadyTimeout, reconcileInterval).Should(Succeed())
			_, err = utils.Run(exec.Command("docker", "rm", "--force", container))
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(source.CheckHealth(context.Background())).To(Succeed(), "recovery must not modify the source")
	})

	It("retains distinct observed backup encryption keys", Label("backup"), func() {
		_, err := utils.Run(exec.Command(
			"kubectl", "patch", "ha", haName, "-n", namespace, "--type=merge",
			"-p", `{"spec":{"backup":{"enabled":false}}}`,
		))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string {
			return utils.Kubectl("get", "ha", haName, "-n", namespace,
				"-o", `jsonpath={.status.conditions[?(@.type=="BackupConfigured")].reason}`)
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Equal("Unmanaged"))
		endpoint, cleanup := criticalPathHAEndpoint(namespace, haName)
		defer cleanup()
		fixtureClient := haclient.NewClient(endpoint)
		Eventually(func() error {
			return fixtureClient.CheckHealth(context.Background())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
		token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
		first, second := "e2e-key-"+utils.RandomString(24), "e2e-key-"+utils.RandomString(24)
		observe := func(value, marker string) string {
			Expect(utils.SetNativeBackupPassword(context.Background(), endpoint, token, value)).To(Succeed())
			_, err := utils.Run(exec.Command("kubectl", "annotate", "ha", haName, "-n", namespace,
				"ha.homeassistant.io/key-observation="+marker, "--overwrite"))
			Expect(err).NotTo(HaveOccurred())
			var ref struct {
				Key string `json:"key"`
			}
			Eventually(func() bool {
				payload := utils.Kubectl("get", "ha", haName, "-n", namespace, "-o", "json")
				var resource struct {
					Status struct {
						Backup struct {
							KeySecretRef *struct {
								Name string `json:"name"`
								Key  string `json:"key"`
							} `json:"keySecretRef"`
						} `json:"backup"`
					} `json:"status"`
				}
				if json.Unmarshal([]byte(payload), &resource) != nil || resource.Status.Backup.KeySecretRef == nil {
					return false
				}
				secretJSON, err := exec.Command("kubectl", "get", "secret", resource.Status.Backup.KeySecretRef.Name,
					"-n", namespace, "-o", "json").Output()
				if err != nil {
					return false
				}
				var secret struct {
					Data map[string]string `json:"data"`
				}
				if json.Unmarshal(secretJSON, &secret) != nil {
					return false
				}
				observed, err := base64.StdEncoding.DecodeString(secret.Data[resource.Status.Backup.KeySecretRef.Key])
				if err != nil || string(observed) != value {
					return false
				}
				ref.Key = resource.Status.Backup.KeySecretRef.Key
				return true
			}, utils.StatusUpdateTimeout, reconcileInterval).Should(BeTrue())
			return ref.Key
		}
		firstKey := observe(first, "first")
		secondKey := observe(second, "second")
		firstAgainKey := observe(first, "first-again")
		Expect(firstKey).To(Equal(firstAgainKey))
		Expect(secondKey).NotTo(Equal(firstKey))
		var retainedSecret struct {
			Status struct {
				Backup struct {
					KeySecretRef struct {
						Name string `json:"name"`
					} `json:"keySecretRef"`
				} `json:"backup"`
			} `json:"status"`
		}
		payload := utils.Kubectl("get", "ha", haName, "-n", namespace, "-o", "json")
		Expect(json.Unmarshal([]byte(payload), &retainedSecret)).To(Succeed())
		Expect(retainedSecret.Status.Backup.KeySecretRef.Name).NotTo(BeEmpty())
		retainedKeySecret = retainedSecret.Status.Backup.KeySecretRef.Name
		retainedFirstKey, retainedSecondKey = firstKey, secondKey
		retainedFirstValue, retainedSecondValue = first, second
	})

	It("dedicated bootstrap isolates the runtime principal and retires bootstrap access", func() {
		By("Verifying the namespace-local managed Secret metadata")
		owner := utils.Kubectl(
			"get",
			"secret",
			haName+"-homeassistant-api-token",
			"-n",
			namespace,
			"-o",
			"jsonpath={.metadata.ownerReferences[?(@.controller==true)].uid}",
		)
		uid := utils.Kubectl("get", "ha", haName, "-n", namespace, "-o", "jsonpath={.metadata.uid}")
		Expect(owner).To(Equal(uid))
		secretList, listErr := exec.Command("kubectl", "get", "secrets", "--all-namespaces", "-o", "json").Output()
		Expect(listErr).NotTo(HaveOccurred())
		var allSecrets struct {
			Items []struct {
				Metadata struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"metadata"`
				Data map[string][]byte `json:"data"`
			} `json:"items"`
		}
		Expect(json.Unmarshal(secretList, &allSecrets)).To(Succeed())
		principal := utils.Kubectl(
			"get",
			"ha",
			haName,
			"-n",
			namespace,
			"-o",
			"jsonpath={.status.bootstrap.operatorAccount.userID}",
		)
		Expect(principal).NotTo(BeEmpty())
		apiClient, cleanup := criticalPathHAClient(namespace, haName)
		defer cleanup()
		token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
		for _, secret := range allSecrets.Items {
			if secret.Metadata.Name == haName+"-homeassistant-api-token" && secret.Metadata.Namespace == namespace {
				continue
			}
			for _, value := range secret.Data {
				Expect(strings.Contains(string(value), token)).To(BeFalse(), "operator token copied to another Secret")
			}
		}
		user, err := apiClient.CurrentUser(context.Background(), token)
		Expect(err).NotTo(HaveOccurred(), "dedicated principal query failed")
		Expect(user.ID).To(Equal(principal))
		Expect(user.IsOwner).To(BeFalse())
		Expect(user.IsAdmin).To(BeTrue())
		users, err := apiClient.ListUsers(context.Background(), token)
		Expect(err).NotTo(HaveOccurred())
		ownerFound := false
		for _, candidate := range users {
			if candidate.ID == user.ID {
				Expect(candidate.GroupIDs).To(ContainElement("system-admin"))
			}
			if candidate.IsOwner && candidate.ID != user.ID {
				ownerFound = true
			}
		}
		Expect(ownerFound).To(BeTrue(), "onboarding owner and operator must be distinct")
		By("Checking the credential is absent from status, events, and operator logs")
		for _, args := range [][]string{
			{"get", "ha", haName, "-n", namespace, "-o", "json"},
			{"get", "events", "-n", namespace, "-o", "json"},
			{"logs", "-n", "homeassistant-operator-system", "-l", "control-plane=controller-manager", "--tail=-1"},
		} {
			out, readErr := exec.Command("kubectl", args...).Output()
			Expect(readErr).NotTo(HaveOccurred())
			Expect(strings.Contains(string(out), token)).To(BeFalse(), "credential leaked into diagnostics")
		}
		By("Removing bootstrap credentials before the remaining management scenarios")
		_, err = utils.Run(exec.Command("kubectl", "delete", "secret", "ha-bootstrap-creds", "-n", namespace))
		Expect(err).NotTo(HaveOccurred())
		Expect(apiClient.CheckHealthWithToken(context.Background(), token)).To(Succeed())
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
	// 2. HomeAssistantDashboard — inline dashboard create and update
	// -------------------------------------------------------------------------
	It("HomeAssistantDashboard — creates and updates an inline dashboard", func() {
		dashboardYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantDashboard
metadata:
  name: cp-dashboard
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  title: Critical Path Dashboard
  urlPath: critical-path
  inline: |
    title: Critical Path Dashboard
    views:
      - title: Overview
        cards: []
`, namespace, haName)
		By("Creating an inline HomeAssistantDashboard")
		Expect(utils.ApplyYAML(dashboardYAML, namespace)).To(Succeed())

		var dashboardID, initialHash string
		By("Verifying the dashboard is ready with its Home Assistant identity")
		Eventually(func(g Gomega) {
			ready := utils.Kubectl("get", "hadashboard", "cp-dashboard", "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			status := utils.Kubectl("get", "hadashboard", "cp-dashboard", "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].reason}: {.status.lastError}")
			g.Expect(ready).To(Equal("True"), status)
			dashboardID = utils.Kubectl("get", "hadashboard", "cp-dashboard", "-n", namespace,
				"-o", "jsonpath={.status.dashboardID}")
			g.Expect(dashboardID).NotTo(BeEmpty())
			path := utils.Kubectl("get", "hadashboard", "cp-dashboard", "-n", namespace,
				"-o", "jsonpath={.status.urlPath}")
			g.Expect(path).To(Equal("critical-path"))
			initialHash = utils.Kubectl("get", "hadashboard", "cp-dashboard", "-n", namespace,
				"-o", "jsonpath={.status.sourceHash}")
			g.Expect(initialHash).NotTo(BeEmpty())
		}, time.Minute, reconcileInterval).Should(Succeed())

		By("Updating the inline dashboard")
		updatedYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantDashboard
metadata:
  name: cp-dashboard
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  title: Updated Critical Path Dashboard
  urlPath: critical-path
  inline: |
    title: Updated Critical Path Dashboard
    views:
      - title: Updated Overview
        cards: []
`, namespace, haName)
		Expect(utils.ApplyYAML(updatedYAML, namespace)).To(Succeed())

		By("Verifying the same dashboard was updated")
		Eventually(func(g Gomega) {
			hash := utils.Kubectl("get", "hadashboard", "cp-dashboard", "-n", namespace,
				"-o", "jsonpath={.status.sourceHash}")
			g.Expect(hash).NotTo(Equal(initialHash))
			id := utils.Kubectl("get", "hadashboard", "cp-dashboard", "-n", namespace,
				"-o", "jsonpath={.status.dashboardID}")
			g.Expect(id).To(Equal(dashboardID))
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Deleting the dashboard while Home Assistant is available")
		_, err := utils.Run(
			exec.Command("kubectl", "delete", "hadashboard", "cp-dashboard", "-n", namespace, "--wait=false"),
		)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "hadashboard", "cp-dashboard", "-n", namespace, "--ignore-not-found")
			g.Expect(output).To(BeEmpty())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// 3. HomeAssistantConfiguration — hot-reload on config change
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
		updateYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantConfiguration
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  reloadStrategy: auto
  configuration: |
    recorder:
    history:
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
	// 2b. HomeAssistantConfiguration — http: delivered via the HA API (2026.8+)
	// -------------------------------------------------------------------------
	It("HomeAssistantConfiguration — http: is delivered via the API and omitted from configuration.yaml", func() {
		// Timeouts here allow for the full round-trip on a real Home Assistant:
		// probe -> configure -> HA restarts its own process if needed -> the
		// operator's ~30s requeue -> promote -> Applied. Home Assistant may
		// restart during this window (that is its call, not the operator's), so
		// this spec does not assert pod stability. The merge / no-write-when-equal
		// / foreign-change / rejection logic is covered by the unit tests.
		By("Applying a config with an http: section")
		httpYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
    http:
      cors_allowed_origins:
        - https://cast.home-assistant.io
`, configName, namespace, haName)
		Expect(utils.ApplyYAML(httpYAML, namespace)).To(Succeed())

		By("Verifying status.httpConfigSource becomes Api")
		Eventually(func(g Gomega) {
			src := utils.Kubectl("get", "haconfig", configName, "-n", namespace,
				"-o", "jsonpath={.status.httpConfigSource}")
			g.Expect(src).To(Equal("Api"))
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Verifying HTTPConfigReady eventually reports Applied")
		Eventually(func(g Gomega) {
			reason := utils.Kubectl("get", "haconfig", configName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='HTTPConfigReady')].reason}")
			g.Expect(reason).To(Equal("Applied"))
		}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())

		By("Verifying the generated configuration.yaml contains no http: section")
		Eventually(func(g Gomega) {
			cm := utils.Kubectl("get", "configmap", haName+"-configuration", "-n", namespace,
				"-o", "jsonpath={.data.configuration\\.yaml}")
			g.Expect(cm).NotTo(BeEmpty())
			g.Expect(cm).NotTo(ContainSubstring("http:"))
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())

		By("Verifying the config hash does not churn (the operator is not rewriting the ConfigMap)")
		hash := utils.Kubectl("get", "haconfig", configName, "-n", namespace,
			"-o", "jsonpath={.status.configHash}")
		Consistently(func(g Gomega) {
			h := utils.Kubectl("get", "haconfig", configName, "-n", namespace,
				"-o", "jsonpath={.status.configHash}")
			g.Expect(h).To(Equal(hash))
		}, 12*time.Second, 4*time.Second).Should(Succeed())
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
		haSecretsYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
		cmd := exec.Command(
			"kubectl",
			"delete",
			"hasecrets",
			secretsName,
			"-n",
			namespace,
			"--wait=true",
			"--timeout=30s",
		)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	// -------------------------------------------------------------------------
	// 4. HomeAssistantAutomation — PUT, reload, and delete via REST API
	// -------------------------------------------------------------------------
	It("HomeAssistantAutomation — PUT, reload, and delete via REST API", func() {
		autoName := "cp-automation"

		By("Creating HomeAssistantAutomation CR")
		autoYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
		updatedAutoYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
		sceneYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
		updatedSceneYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
		scriptYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
		updatedScriptYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
		intYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
		floorYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
		labelYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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
		areaYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
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

	It("operator credential loss requires explicit recovery and does not replay an observed request", func() {
		secretName := haName + "-homeassistant-api-token"
		oldToken := criticalPathOperatorToken(namespace, secretName)
		_, err := utils.Run(exec.Command("kubectl", "delete", "secret", secretName, "-n", namespace))
		Expect(err).NotTo(HaveOccurred())
		// A spec update guarantees an immediate reconciliation without relying on
		// the periodic health interval or Secret watch delivery.
		_, err = utils.Run(
			exec.Command("kubectl", "annotate", "ha", haName, "-n", namespace, "credential-check=loss", "--overwrite"),
		)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string {
			return utils.Kubectl(
				"get",
				"ha",
				haName,
				"-n",
				namespace,
				"-o",
				`jsonpath={.status.conditions[?(@.type=="Degraded")].reason}`,
			)
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Equal("CredentialMissing"))
		Expect(utils.Kubectl("get", "secret", secretName, "-n", namespace, "--ignore-not-found")).To(BeEmpty())
		By("Restoring owner credentials and requesting one replacement")
		credentials := fmt.Sprintf(
			"apiVersion: v1\nkind: Secret\nmetadata:\n  name: ha-bootstrap-creds\n  namespace: %s\n"+
				"type: Opaque\nstringData:\n  username: admin\n  password: e2e-critical-path-pwd-123456\n",
			namespace,
		)
		Expect(utils.ApplyYAML(credentials, namespace)).To(Succeed())
		_, err = utils.Run(
			exec.Command(
				"kubectl",
				"patch",
				"ha",
				haName,
				"-n",
				namespace,
				"--type=merge",
				"-p",
				`{"spec":{"bootstrap":{"operatorAccount":{"recoveryRequestID":"critical-path-1"}}}}`,
			),
		)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string {
			return utils.Kubectl(
				"get",
				"ha",
				haName,
				"-n",
				namespace,
				"-o",
				"jsonpath={.status.bootstrap.operatorAccount.recoveryResult}",
			)
		}, utils.BootstrapTimeout, reconcileInterval).Should(Equal("Succeeded"))
		newToken := criticalPathOperatorToken(namespace, secretName)
		// Comparing a boolean keeps both sensitive operands out of failure output.
		replaced := newToken != oldToken
		Expect(replaced).To(BeTrue(), "recovery did not replace the token")
		apiClient, cleanup := criticalPathHAClient(namespace, haName)
		defer cleanup()
		Expect(apiClient.CheckHealthWithToken(context.Background(), newToken)).To(Succeed())
		_, err = utils.Run(
			exec.Command(
				"kubectl",
				"annotate",
				"ha",
				haName,
				"-n",
				namespace,
				"credential-check=replay",
				"--overwrite",
			),
		)
		Expect(err).NotTo(HaveOccurred())
		Consistently(
			func() bool { return criticalPathOperatorToken(namespace, secretName) == newToken },
			10*time.Second,
			reconcileInterval,
		).Should(BeTrue(), "observed recovery request replayed")
	})

	It("legacy-token migration needs no bootstrap credentials and retains its original token", func() {
		migrationName := "ha-migration"
		manifest := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
kind: HomeAssistant
metadata:
  name: %s
  namespace: %s
spec:
  version: "%s"
  storage:
    size: 1Gi
  bootstrap:
    enabled: true
    credentials:
      secretRef:
        name: ha-bootstrap-creds
    createApiToken: true
---
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantConfiguration
metadata:
  name: ha-migration-config
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  configuration: |
    default_config:
`, migrationName, namespace, haVersion(), namespace, migrationName)
		Expect(utils.ApplyYAML(manifest, namespace)).To(Succeed())
		Eventually(func() string {
			return utils.Kubectl(
				"get",
				"ha",
				migrationName,
				"-n",
				namespace,
				"-o",
				"jsonpath={.status.bootstrap.completed}",
			)
		}, utils.BootstrapTimeout, bootstrapInterval).Should(Equal("true"))
		legacyName := migrationName + "-homeassistant-api-token"
		legacyToken := criticalPathOperatorToken(namespace, legacyName)
		By("Removing bootstrap credentials before migration; only the legacy API token remains")
		_, err := utils.Run(exec.Command("kubectl", "delete", "secret", "ha-bootstrap-creds", "-n", namespace))
		Expect(err).NotTo(HaveOccurred())
		migrationPatch := fmt.Sprintf(
			`{"spec":{"bootstrap":{"operatorAccount":{"mode":"dedicated","tokenSecretName":%q}}}}`, legacyName)
		_, err = utils.Run(
			exec.Command(
				"kubectl",
				"patch",
				"ha",
				migrationName,
				"-n",
				namespace,
				"--type=merge",
				"-p",
				migrationPatch,
			),
		)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string {
			return utils.Kubectl(
				"get",
				"ha",
				migrationName,
				"-n",
				namespace,
				"-o",
				`jsonpath={.status.conditions[?(@.type=="OperatorCredentialReady")].reason}`,
			)
		}, utils.StatusUpdateTimeout, reconcileInterval).Should(Equal("CredentialMalformed"))
		retained := criticalPathOperatorToken(namespace, legacyName) == legacyToken
		Expect(retained).To(BeTrue())
		_, err = utils.Run(
			exec.Command(
				"kubectl",
				"patch",
				"ha",
				migrationName,
				"-n",
				namespace,
				"--type=merge",
				"-p",
				`{"spec":{"bootstrap":{"operatorAccount":{"tokenSecretName":"ha-migration-dedicated"}}}}`,
			),
		)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string {
			return utils.Kubectl(
				"get",
				"ha",
				migrationName,
				"-n",
				namespace,
				"-o",
				"jsonpath={.status.bootstrap.operatorAccount.migrationPhase}",
			)
		}, utils.BootstrapTimeout, reconcileInterval).Should(Equal("Completed"))
		retained = criticalPathOperatorToken(namespace, legacyName) == legacyToken
		Expect(retained).To(BeTrue(), "migration modified the legacy token")
		Expect(
			utils.Kubectl(
				"get",
				"ha",
				migrationName,
				"-n",
				namespace,
				"-o",
				"jsonpath={.status.bootstrap.operatorAccount.legacySecretName}",
			),
		).To(Equal(legacyName))
	})
})

// Read sensitive output directly so command errors cannot print a credential.
func criticalPathOperatorToken(namespace, name string) string {
	data, err := exec.Command("kubectl", "get", "secret", name, "-n", namespace, "-o", "json").Output()
	Expect(err).NotTo(HaveOccurred(), "cannot read managed token Secret")
	var secret struct {
		Data map[string]string `json:"data"`
	}
	Expect(json.Unmarshal(data, &secret)).To(Succeed())
	token, err := base64.StdEncoding.DecodeString(secret.Data["token"])
	Expect(err == nil && len(token) > 0).To(BeTrue(), "invalid managed token payload")
	return string(token)
}

func criticalPathHAClient(namespace, name string) (*haclient.Client, func()) {
	endpoint, cleanup := criticalPathHAEndpoint(namespace, name)
	c := haclient.NewClient(endpoint)
	Eventually(
		func() error { return c.CheckHealth(context.Background()) },
		utils.ResourceTimeout,
		reconcileInterval,
	).Should(Succeed())
	return c, cleanup
}

func criticalPathHAEndpoint(namespace, name string) (string, func()) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	port := listener.Addr().(*net.TCPAddr).Port
	Expect(listener.Close()).To(Succeed())
	cmd := exec.Command("kubectl", "port-forward", "-n", namespace, "service/"+name, fmt.Sprintf("%d:8123", port))
	Expect(cmd.Start()).To(Succeed())
	cleanup := func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
	DeferCleanup(cleanup)
	return fmt.Sprintf("http://127.0.0.1:%d", port), func() {} // cleanup is registered above
}

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
