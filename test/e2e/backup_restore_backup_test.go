package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/test/e2e/fixtures"
	"github.com/przemekhys/homeassistant-operator/test/utils"
)

// All data and credentials belong to a newly created disposable instance.
// Failures deliberately report no native response, archive bytes or key values.
var _ = Describe("Protected Core backups", Label(backupRestoreLabel, "group-a"), func() {
	It(
		"configures the native schedule, downloads a protected backup and never redispatches after manager restart",
		func() {
			namespace := "ha-backup-qualification-" + utils.RandomString(8)
			const targetName = "backup-core"
			candidate := utils.HomeAssistantBackupRestoreCandidate
			digest := candidate.PlatformDigests[runtime.GOARCH]
			Expect(haclient.ValidBackupImageDigest(digest)).To(BeTrue(), "no pinned candidate for this platform")

			By("creating an isolated namespace and synthetic bootstrap credentials")
			Expect(utils.CreateNamespace(namespace)).To(Succeed())
			DeferCleanup(func() { Expect(utils.DeleteNamespace(namespace)).To(Succeed()) })
			DeferCleanup(func() {
				if CurrentSpecReport().Failed() {
					reportBackupTestFailure(namespace, targetName)
				}
			})
			Expect(utils.Kubectl("label", "namespace", namespace,
				utils.BackupQualificationNamespaceLabel+"=true")).NotTo(BeEmpty())
			Expect(utils.ApplyYAML(fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: bootstrap
  namespace: %s
type: Opaque
stringData:
  username: admin
  password: synthetic-disposable-backup-password
`, namespace), namespace)).To(Succeed())

			By("bootstrapping only the pinned candidate with scheduling initially disabled")
			fixture, err := fixtures.BackupAgentConfigMap(namespace)
			expectBackupStep(err, "local agent fixture is unavailable")
			fixtureJSON, err := json.Marshal(fixture)
			expectBackupStep(err, "local agent fixture could not be encoded")
			Expect(utils.ApplyYAML(string(fixtureJSON), namespace)).To(Succeed())
			Expect(utils.ApplyYAML(fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
kind: HomeAssistant
metadata:
  name: %s
  namespace: %s
spec:
  version: "%s@%s"
%s
  storage:
    size: 2Gi
  bootstrap:
    enabled: true
    credentials:
      secretRef:
        name: bootstrap
    createApiToken: true
    apiTokenSecretName: backup-core-homeassistant-api-token
    operatorAccount:
      mode: dedicated
      tokenSecretName: backup-core-homeassistant-api-token
  backup:
    enabled: false
    agentIDs: [backup.local]
---
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantConfiguration
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  configuration: |
    default_config:
    backup_test:
`, targetName, namespace, candidate.Version, digest, fixtures.BackupAgentVolumes,
				targetName, namespace, targetName), namespace)).To(Succeed())
			Eventually(func() string {
				return utils.Kubectl("get", "ha", targetName, "-n", namespace,
					"-o", "jsonpath={.status.bootstrap.completed}")
			}, utils.BootstrapTimeout, 5*time.Second).Should(Equal("true"))
			haClient, _ := criticalPathHAClient(namespace, targetName)
			token := criticalPathOperatorToken(namespace, targetName+"-homeassistant-api-token")

			By("setting a synthetic native encryption key and requiring protected Core storage")
			// This write belongs only to fixture preparation, never operator policy.
			_, err = haClient.SendWebSocketCommand(context.Background(), token, "backup/config/update", map[string]any{
				"create_backup": map[string]any{
					"password": utils.BackupRestoreTestKey, "agent_ids": []string{"backup.local"}, "include_database": true,
				},
				"agents": map[string]any{"backup.local": map[string]bool{"protected": true}},
			})
			expectBackupStep(err, "native encryption fixture setup failed (response suppressed)")
			Eventually(func() bool {
				config, readErr := haClient.GetBackupConfig(context.Background(), token)
				return readErr == nil && config.CreateBackup.Password != nil &&
					*config.CreateBackup.Password == utils.BackupRestoreTestKey && config.Agents["backup.local"].Protected
			}, utils.ResourceTimeout, 2*time.Second).Should(BeTrue(), "native protection could not be verified")

			By("verifying retained key evidence and managed schedule without exposing key contents")
			Eventually(func() string {
				return utils.Kubectl("get", "ha", targetName, "-n", namespace,
					"-o", "jsonpath={.status.backup.currentKeySecretRef.name}")
			}, 2*time.Minute, 5*time.Second).ShouldNot(BeEmpty())
			patch := `{"spec":{"backup":{"enabled":true,"recurrence":"daily","time":"03:00:00"}}}`
			Expect(
				utils.Kubectl("patch", "ha", targetName, "-n", namespace, "--type=merge", "-p", patch),
			).NotTo(BeEmpty())
			Eventually(func() bool {
				config, readErr := haClient.GetBackupConfig(context.Background(), token)
				return readErr == nil && config.Schedule.Recurrence == "daily" &&
					config.Schedule.Time != nil && *config.Schedule.Time == "03:00:00"
			}, utils.ResourceTimeout, 5*time.Second).Should(BeTrue(), "managed native schedule was not observed")

			targetUID := utils.Kubectl("get", "ha", targetName, "-n", namespace, "-o", "jsonpath={.metadata.uid}")
			Expect(targetUID).NotTo(BeEmpty())
			const operationName = "protected-once"
			Expect(utils.ApplyYAML(fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantBackup
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
    uid: "%s"
  agentIDs: [backup.local]
  includeDatabase: true
`, operationName, namespace, targetName, targetUID), namespace)).To(Succeed())

			By("running the scoped qualification process against this target only")
			projectDir, err := utils.GetProjectDir()
			Expect(err).NotTo(HaveOccurred())
			build := exec.Command("make", "build-backup-restore-qualification")
			build.Dir = projectDir
			Expect(build.Run()).To(Succeed())
			// Reuse the verified port-forward URL from the test-only client callback.
			url := backupQualificationURL(namespace, targetName)
			runQualification(projectDir, namespace, targetName, targetUID, digest, operationName, url)
			operation := readBackupOperation(namespace, operationName)
			Expect(operation.Status.Phase).To(Equal(hav1alpha1.BackupPhaseSucceeded))
			Expect(operation.Status.BackupID).NotTo(BeEmpty())
			Expect(operation.Status.BackupJobID).NotTo(BeEmpty())
			archiveID := operation.Status.BackupID
			evidence := readBackupEvidence(namespace, operation.Status.EvidenceRef)
			backupIDs := nativeBackupIDs(haClient, token)
			Expect(backupIDs).To(ContainElement(archiveID))

			By("downloading the original archive without passwords in URLs or archive content in diagnostics")
			before := downloadBackupFingerprint(url, token, archiveID)
			Expect(before.size).To(BeNumerically(">", 0))

			By("restarting the operator and observing the same durable operation with a new qualification process")
			Expect(utils.Kubectl("rollout", "restart", "deployment/homeassistant-operator-controller-manager",
				"-n", "homeassistant-operator-system")).NotTo(BeEmpty())
			Expect(utils.Kubectl("rollout", "status", "deployment/homeassistant-operator-controller-manager",
				"-n", "homeassistant-operator-system", "--timeout=120s")).NotTo(BeEmpty())
			runQualification(projectDir, namespace, targetName, targetUID, digest, operationName, url)
			after := readBackupOperation(namespace, operationName)
			Expect(after.UID).To(Equal(operation.UID))
			Expect(after.Status.BackupID).To(Equal(archiveID))
			Expect(after.Status.BackupJobID).To(Equal(operation.Status.BackupJobID))
			Expect(readBackupEvidence(namespace, after.Status.EvidenceRef)).To(Equal(evidence))
			Expect(
				nativeBackupIDs(haClient, token),
			).To(Equal(backupIDs), "restart created an unexpected native archive")
			Expect(downloadBackupFingerprint(url, token, archiveID)).To(Equal(before))
			exerciseFixtureBackups(projectDir, namespace, targetName, targetUID, digest, url, token)
			exerciseBackupKeyHistory(namespace, targetName, targetUID, candidate.Version+"@"+digest, haClient, token)
			AddReportEntry("backup-candidate", map[string]string{
				"version": candidate.Version, "sourceCommit": candidate.SourceCommit, "imageDigest": digest,
				"archiveDigest": before.digest, "result": "Core backup/download/restart passed; no production promotion",
			})
		},
	)
})

func exerciseBackupKeyHistory(namespace, target, uid, image string, client *haclient.Client, token string) {
	By("disabling scheduling before rotating synthetic keys through the native API")
	Expect(utils.Kubectl("patch", "ha", target, "-n", namespace, "--type=merge",
		"-p", `{"spec":{"backup":{"enabled":false}}}`)).NotTo(BeEmpty())
	Eventually(func() bool {
		config, err := client.GetBackupConfig(context.Background(), token)
		return err == nil && config.Schedule.Recurrence == "never"
	}, utils.ResourceTimeout, time.Second).Should(BeTrue())
	first := waitForBackupKey(namespace, target, uid, utils.BackupRestoreTestKey)
	const rotated = "synthetic-disposable-rotated-backup-key"
	setNativeBackupKey(client, token, rotated)
	second := waitForBackupKey(namespace, target, uid, rotated)
	Expect(second.Name).NotTo(Equal(first.Name))
	Expect(second.UID).NotTo(Equal(first.UID))
	assertBackupKeyUnchanged(namespace, first)

	By("returning to an observed key without creating another history Secret")
	before := backupKeyHistoryNames(namespace, uid)
	setNativeBackupKey(client, token, utils.BackupRestoreTestKey)
	reused := waitForBackupKey(namespace, target, uid, utils.BackupRestoreTestKey)
	Expect(reused.Name).To(Equal(first.Name))
	Expect(backupKeyHistoryNames(namespace, uid)).To(Equal(before))
	assertBackupKeyUnchanged(namespace, first)
	assertBackupKeyUnchanged(namespace, second)

	By("checking the documented external-export procedure without printing key material")
	verifyBackupKeyExport(namespace, first)
	verifyBackupKeyExport(namespace, second)

	By("deleting and recreating only the disposable target while retaining its UID-scoped key history")
	Expect(utils.Kubectl("delete", "ha", target, "-n", namespace, "--wait=true", "--timeout=120s")).NotTo(BeEmpty())
	assertBackupKeyUnchanged(namespace, first)
	assertBackupKeyUnchanged(namespace, second)
	Expect(utils.ApplyYAML(fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
kind: HomeAssistant
metadata:
  name: %s
  namespace: %s
spec:
  version: "%s"
  storage:
    size: 2Gi
  bootstrap:
    enabled: false
  backup:
    managementPolicy: Unmanaged
`, target, namespace, image), namespace)).To(Succeed())
	newUID := utils.Kubectl("get", "ha", target, "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	Expect(newUID).NotTo(BeEmpty())
	Expect(newUID).NotTo(Equal(uid))
	Expect(backupKeyHistoryNames(namespace, newUID)).To(BeEmpty())
	Expect(backupKeyHistoryNames(namespace, uid)).To(Equal(before))
	assertBackupKeyUnchanged(namespace, first)
	assertBackupKeyUnchanged(namespace, second)
	AddReportEntry("key-history-export",
		"rotation/back-rotation, immutable history, target UID replacement and private file export passed; "+
			"no restore or vault qualification")
}

func setNativeBackupKey(client *haclient.Client, token, key string) {
	_, err := client.SendWebSocketCommand(context.Background(), token, "backup/config/update", map[string]any{
		"create_backup": map[string]any{"password": key},
	})
	expectBackupStep(err, "native synthetic key rotation failed (response suppressed)")
}

func readBackupKeySecret(namespace, name string) corev1.Secret {
	data, err := exec.Command("kubectl", "get", "secret", name, "-n", namespace, "-o", "json").Output()
	expectBackupStep(err, "retained key Secret is unavailable (response suppressed)")
	var secret corev1.Secret
	expectBackupStep(json.Unmarshal(data, &secret), "retained key Secret is malformed")
	return secret
}

func waitForBackupKey(namespace, target, uid, key string) corev1.Secret {
	var secret corev1.Secret
	Eventually(func() bool {
		name := utils.Kubectl("get", "ha", target, "-n", namespace,
			"-o", "jsonpath={.status.backup.currentKeySecretRef.name}")
		if name == "" {
			return false
		}
		secret = readBackupKeySecret(namespace, name)
		// Never pass secret bytes or the Secret object to an assertion renderer.
		return string(secret.Data["password"]) == key
	}, 2*time.Minute, 2*time.Second).Should(BeTrue(), "observed key was not retained")
	Expect(secret.Immutable != nil && *secret.Immutable).To(BeTrue())
	Expect(secret.OwnerReferences).To(BeEmpty())
	Expect(secret.Labels["ha.homeassistant.io/instance-uid"]).To(Equal(uid))
	Expect(secret.Labels["ha.homeassistant.io/purpose"]).To(Equal("backup-key-history"))
	return secret
}

func assertBackupKeyUnchanged(namespace string, original corev1.Secret) {
	current := readBackupKeySecret(namespace, original.Name)
	Expect(current.UID).To(Equal(original.UID))
	Expect(current.ResourceVersion).To(Equal(original.ResourceVersion))
	unchanged := string(current.Data["password"]) == string(original.Data["password"])
	Expect(unchanged).To(BeTrue(), "retained key changed")
}

func backupKeyHistoryNames(namespace, uid string) []string {
	data, err := exec.Command("kubectl", "get", "secrets", "-n", namespace,
		"-l", "ha.homeassistant.io/instance-uid="+uid+",ha.homeassistant.io/purpose=backup-key-history",
		"-o", "jsonpath={.items[*].metadata.name}").Output()
	expectBackupStep(err, "key history inventory is unavailable")
	names := strings.Fields(string(data))
	sort.Strings(names)
	return names
}

func verifyBackupKeyExport(namespace string, secret corev1.Secret) {
	// This private local directory models offline export outside the cluster;
	// it is not a vault integration or proof of native archive decryptability.
	directory := GinkgoT().TempDir()
	export := exec.Command("bash", "-c", `set +x
umask 077
EXPORT_FILE=$(mktemp "$KEY_EXPORT_DIR/ha-backup-key.XXXXXXXX.json") || exit 1
if ! kubectl --context="$CONTEXT" get secret "$KEY_SECRET" -n "$NAMESPACE" -o json > "$EXPORT_FILE"; then
  rm -f -- "$EXPORT_FILE"
  exit 1
fi
printf '%s' "$EXPORT_FILE"`)
	export.Env = append(os.Environ(), "KEY_EXPORT_DIR="+directory, "CONTEXT="+utils.K3dContextName(),
		"KEY_SECRET="+secret.Name, "NAMESPACE="+namespace)
	path, err := export.Output()
	expectBackupStep(err, "private key export failed (output suppressed)")
	info, err := os.Stat(string(path))
	expectBackupStep(err, "private export file is unavailable")
	Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	data, err := os.ReadFile(string(path))
	expectBackupStep(err, "private export could not be read")
	var exported corev1.Secret
	expectBackupStep(json.Unmarshal(data, &exported), "private export is malformed")
	Expect(exported.UID).To(Equal(secret.UID))
	Expect(len(exported.Data["password"]) > 0 &&
		string(exported.Data["password"]) == string(secret.Data["password"])).To(BeTrue(), "exported key does not match")
	// The error branch must not leave an empty file that looks like a valid export.
	missing := exec.Command("bash", "-c", export.Args[2])
	missing.Env = append(export.Env, "KEY_SECRET=nonexistent-backup-key")
	_, err = missing.Output()
	Expect(err).To(HaveOccurred(), "missing Secret must fail export")
	files, err := os.ReadDir(directory)
	expectBackupStep(err, "private export inventory is unavailable")
	Expect(files).To(HaveLen(1), "failed export left a misleading file")
}

func runQualification(projectDir, namespace, target, uid, digest, operation, url string) {
	runQualificationExpect(projectDir, namespace, target, uid, digest, operation, url, hav1alpha1.BackupPhaseSucceeded)
}

func runQualificationExpect(projectDir, namespace, target, uid, digest, operation, url string,
	expected hav1alpha1.BackupOperationPhase,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(projectDir, "bin", "backup-restore-qualification"),
		"--image-digest="+digest, "--namespace="+namespace, "--target="+target, "--target-uid="+uid,
		"--operation="+operation, "--ha-url="+url, "--kube-context="+utils.K3dContextName())
	// The program uses fixed safe diagnostics; no credential or response capture.
	err := cmd.Run()
	if expected == hav1alpha1.BackupPhaseSucceeded {
		expectBackupStep(err, "scoped native backup did not pass; inspect safe CR status/evidence")
	} else {
		Expect(err).To(HaveOccurred(), "qualification must reject partial success")
	}
	Expect(readBackupOperation(namespace, operation).Status.Phase).To(Equal(expected))
}

func exerciseFixtureBackups(projectDir, namespace, target, uid, digest, url, token string) {
	const agentA, agentB = "backup_test.agent-a", "backup_test.agent-b"
	client := haclient.NewClient(url)
	By("discovering native agents backed by separate local directories")
	Eventually(func() bool {
		agents, err := client.ListBackupAgents(context.Background(), token)
		if err != nil {
			return false
		}
		found := map[string]bool{}
		for _, agent := range agents {
			found[agent.ID] = true
		}
		return found[agentA] && found[agentB]
	}, utils.ResourceTimeout, time.Second).Should(BeTrue())
	_, err := client.SendWebSocketCommand(context.Background(), token, "backup/config/update", map[string]any{
		"agents": map[string]any{agentA: map[string]bool{"protected": true}, agentB: map[string]bool{"protected": true}},
	})
	expectBackupStep(err, "fixture protection setup failed")
	for _, scenario := range []struct {
		name   string
		agents []string
		fail   bool
		phase  hav1alpha1.BackupOperationPhase
	}{
		{"fixture-only", []string{agentA}, false, hav1alpha1.BackupPhaseSucceeded},
		{"fixture-multi", []string{agentA, agentB}, false, hav1alpha1.BackupPhaseSucceeded},
		{"fixture-partial", []string{agentA, agentB}, true, hav1alpha1.BackupPhasePartiallySucceeded},
	} {
		By("executing protected native backup: " + scenario.name)
		lastSuccess := utils.Kubectl("get", "ha", target, "-n", namespace,
			"-o", "jsonpath={.status.backup.lastSuccess}")
		Expect(lastSuccess).NotTo(BeEmpty(), "previous fully successful backup was not recorded")
		if scenario.fail {
			_, err := client.SendWebSocketCommand(context.Background(), token, "call_service", map[string]any{
				"domain": "backup_test", "service": "set_failure", "service_data": map[string]bool{"enabled": true},
			})
			expectBackupStep(err, "fixture failure injection failed")
		}
		operation := map[string]any{
			"apiVersion": "ha.homeassistant.io/v1alpha1", "kind": "HomeAssistantBackup",
			"metadata": map[string]string{"name": scenario.name, "namespace": namespace},
			"spec": map[string]any{
				"homeAssistantRef": map[string]string{"name": target, "uid": uid},
				"agentIDs":         scenario.agents, "includeDatabase": true,
			},
		}
		raw, err := json.Marshal(operation)
		expectBackupStep(err, "fixture operation encoding failed")
		Expect(utils.ApplyYAML(string(raw), namespace)).To(Succeed())
		runQualificationExpect(projectDir, namespace, target, uid, digest, scenario.name, url, scenario.phase)
		completed := readBackupOperation(namespace, scenario.name)
		details, err := client.GetBackupDetails(context.Background(), token, completed.Status.BackupID)
		expectBackupStep(err, "fixture archive details unavailable")
		var successful, failed []string
		for _, destination := range details.Agents {
			if destination.Error == "" {
				Expect(destination.Protected).To(BeTrue())
				successful = append(successful, destination.AgentID)
			} else {
				failed = append(failed, destination.AgentID)
			}
		}
		first := downloadBackupFingerprintFromAgent(url, token, completed.Status.BackupID, agentA)
		Expect(first.size).To(BeNumerically(">", 0))
		if scenario.fail {
			Expect(successful).To(ConsistOf(agentA))
			Expect(failed).To(ConsistOf(agentB))
			Expect(utils.Kubectl("get", "ha", target, "-n", namespace,
				"-o", "jsonpath={.status.backup.lastSuccess}")).To(Equal(lastSuccess))
		} else {
			Expect(successful).To(ConsistOf(scenario.agents))
			Expect(failed).To(BeEmpty())
			if len(scenario.agents) == 2 {
				Expect(downloadBackupFingerprintFromAgent(url, token, completed.Status.BackupID, agentB)).To(Equal(first))
			}
		}
		Expect(successful).NotTo(ContainElement("backup.local"), "unexpected Core fallback")
	}
	AddReportEntry("local-agent-fixtures",
		"non-Core-only, multi-agent and write failure passed; no provider qualification")
}

func readBackupOperation(namespace, name string) hav1alpha1.HomeAssistantBackup {
	data, err := exec.Command("kubectl", "get", "homeassistantbackup", name, "-n", namespace, "-o", "json").Output()
	expectBackupStep(err, "backup status is unavailable")
	var operation hav1alpha1.HomeAssistantBackup
	expectBackupStep(json.Unmarshal(data, &operation), "backup status is malformed")
	return operation
}

func readBackupEvidence(namespace, name string) map[string]string {
	data, err := exec.Command("kubectl", "get", "configmap", name, "-n", namespace, "-o", "json").Output()
	expectBackupStep(err, "backup evidence is unavailable")
	var cm struct {
		Data map[string]string `json:"data"`
	}
	expectBackupStep(json.Unmarshal(data, &cm), "backup evidence is malformed")
	return cm.Data
}

type backupFingerprint struct {
	size   int64
	digest string
}

func downloadBackupFingerprint(baseURL, token, archiveID string) backupFingerprint {
	return downloadBackupFingerprintFromAgent(baseURL, token, archiveID, "backup.local")
}

func downloadBackupFingerprintFromAgent(baseURL, token, archiveID, agentID string) backupFingerprint {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/api/backup/download/"+archiveID+"?agent_id="+agentID, nil)
	expectBackupStep(err, "native download request could not be prepared")
	request.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	expectBackupStep(err, "native download failed (response suppressed)")
	defer func() { _ = response.Body.Close() }()
	Expect(response.StatusCode).To(Equal(http.StatusOK))
	hash := sha256.New()
	size, err := io.Copy(hash, response.Body)
	expectBackupStep(err, "native archive download was incomplete")
	return backupFingerprint{size: size, digest: hex.EncodeToString(hash.Sum(nil))}
}

func backupQualificationURL(namespace, target string) string {
	// Port-forward setup is kept separate so the raw native archive uses the
	// exact same explicitly scoped endpoint as the qualification program.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	port := listener.Addr().(*net.TCPAddr).Port
	Expect(listener.Close()).To(Succeed())
	cmd := exec.Command("kubectl", "port-forward", "-n", namespace, "service/"+target, fmt.Sprintf("%d:8123", port))
	Expect(cmd.Start()).To(Succeed())
	DeferCleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	Eventually(func() bool {
		return haclient.NewClient(url).CheckHealth(context.Background()) == nil
	}, utils.ResourceTimeout, time.Second).Should(BeTrue())
	return url
}

func nativeBackupIDs(client *haclient.Client, token string) []string {
	raw, err := client.SendWebSocketCommand(context.Background(), token, "backup/info", nil)
	expectBackupStep(err, "native backup inventory is unavailable (response suppressed)")
	var inventory struct {
		Backups []struct {
			BackupID string `json:"backup_id"`
		} `json:"backups"`
	}
	expectBackupStep(json.Unmarshal(raw, &inventory), "native backup inventory is malformed")
	ids := make([]string, 0, len(inventory.Backups))
	for _, backup := range inventory.Backups {
		ids = append(ids, backup.BackupID)
	}
	sort.Strings(ids)
	return ids
}

// Native errors can embed a response containing credentials. Assert success
// without letting the assertion framework render the underlying error value.
func expectBackupStep(err error, message string) {
	if err != nil {
		Fail(message, 1)
	}
}

// Keep failure evidence useful without dumping Secret data, raw HA responses,
// whole CRs or uncontrolled error strings into CI output.
func reportBackupTestFailure(namespace, target string) {
	for _, kind := range []string{"homeassistant", "homeassistantbackup"} {
		data, err := exec.Command("kubectl", "get", kind, "-n", namespace, "-o", "json").Output()
		if err != nil {
			continue
		}
		var list struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Status struct {
					Phase      string                                  `json:"phase"`
					Conditions []struct{ Type, Status, Reason string } `json:"conditions"`
				} `json:"status"`
			} `json:"items"`
		}
		if json.Unmarshal(data, &list) != nil {
			continue
		}
		for _, item := range list.Items {
			if kind == "homeassistant" && item.Metadata.Name != target {
				continue
			}
			_, _ = fmt.Fprintf(GinkgoWriter, "Safe backup diagnostic: kind=%s name=%s phase=%s\n",
				kind, item.Metadata.Name, item.Status.Phase)
			for _, condition := range item.Status.Conditions {
				_, _ = fmt.Fprintf(GinkgoWriter, "  condition=%s status=%s reason=%s\n",
					condition.Type, condition.Status, condition.Reason)
			}
		}
	}
}
