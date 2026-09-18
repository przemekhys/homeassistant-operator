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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/przemekhys/homeassistant-operator/test/utils"
)

// Fixture repository names used by this suite.
const (
	crFixtureIntegration  = "acme/integration-fixture"
	crFixtureTheme        = "acme/theme-fixture"
	crFixturePlugin       = "acme/plugin-fixture"
	crFixturePythonScript = "acme/python-script-fixture"
	crFixtureTemplate     = "acme/template-fixture"
)

// crActivationSettleDelay must stay in sync with the operator's own
// activationSettleDelay (internal/controller/homeassistantcommunityrepository_controller.go):
// the minimum time reconcileInstalling waits, from entering Installing, before its
// first activation attempt for non-integration categories — long enough for the
// ConfigMap to propagate to the pod's mounted volume (~60-90s kubelet sync) and for
// the sidecar to run a poll cycle (~30s). Tests must budget for at least this much
// time before expecting Installed for theme/python_script/template/plugin.
const crActivationSettleDelay = 90 * time.Second

// buildCommunityRepoFixtureTarball builds a gzip-compressed tarball of files,
// wrapped in a single top-level directory, mirroring GitHub codeload's own layout —
// see internal/communityrepo.FetchTarball.
func buildCommunityRepoFixtureTarball(prefix string, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: prefix + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	for name, content := range files {
		full := prefix + "/" + name
		_ = tw.WriteHeader(&tar.Header{Name: full, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// buildCommunityRepoFixtureTreeConfigMap runs generate.sh to build the v1.0.0
// fixture tarballs for all 5 categories, adds a hand-built v2.0.0 theme variant
// (for the update scenario), arranges them into the "owner/repo/tar.gz/ref"
// directory shape a codeload-compatible static file server expects, and returns a
// ConfigMap manifest (as YAML) holding the whole tree as a single base64-encoded
// tarball — decoded and unpacked by the fixture server's own startup command.
func buildCommunityRepoFixtureTreeConfigMap(namespace string) (string, error) {
	projectDir, err := utils.GetProjectDir()
	if err != nil {
		return "", fmt.Errorf("failed to get project dir: %w", err)
	}
	generateScript := filepath.Join(projectDir, "test", "e2e", "fixtures", "community-repositories", "generate.sh")

	genOutDir, err := os.MkdirTemp("", "cr-e2e-fixtures-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(genOutDir) }()

	cmd := exec.Command("bash", generateScript, genOutDir)
	if _, err := utils.Run(cmd); err != nil {
		return "", fmt.Errorf("generate.sh failed: %w", err)
	}

	treeDir, err := os.MkdirTemp("", "cr-e2e-tree-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(treeDir) }()

	// All fixtures are generated at "v1.0.0"; the update scenario's v2.0.0 variant
	// is placed separately below.
	place := func(repo, tarballPath string) error {
		dest := filepath.Join(treeDir, repo, "tar.gz", "v1.0.0")
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(tarballPath) //nolint:gosec // reading our own just-generated fixture
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	}

	if err := place(crFixtureIntegration, filepath.Join(genOutDir, "integration.tar.gz")); err != nil {
		return "", err
	}
	if err := place(crFixtureTheme, filepath.Join(genOutDir, "theme.tar.gz")); err != nil {
		return "", err
	}
	if err := place(crFixturePlugin, filepath.Join(genOutDir, "plugin.tar.gz")); err != nil {
		return "", err
	}
	if err := place(crFixturePythonScript, filepath.Join(genOutDir, "python_script.tar.gz")); err != nil {
		return "", err
	}
	if err := place(crFixtureTemplate, filepath.Join(genOutDir, "template.tar.gz")); err != nil {
		return "", err
	}

	// Hand-built v2.0.0 theme variant, distinguishable from v1 by content, for the
	// update scenario.
	themeV2 := buildCommunityRepoFixtureTarball("fixture-theme-v2.0.0", map[string]string{
		"hacs.json":                 `{"name":"Example Theme","category":"theme"}`,
		"themes/example_theme.yaml": "example_theme:\n  primary-color: \"#00ff00\"\n  version: 2\n",
	})
	v2Dest := filepath.Join(treeDir, crFixtureTheme, "tar.gz", "v2.0.0")
	if err := os.MkdirAll(filepath.Dir(v2Dest), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(v2Dest, themeV2, 0o644); err != nil {
		return "", err
	}

	var treeTar bytes.Buffer
	gz := gzip.NewWriter(&treeTar)
	tw := tar.NewWriter(gz)
	err = filepath.Walk(treeDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		rel, relErr := filepath.Rel(treeDir, path)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // reading our own just-generated fixture tree
		if readErr != nil {
			return readErr
		}
		hdr := &tar.Header{Name: rel, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	})
	if err != nil {
		return "", err
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}

	encoded := base64.StdEncoding.EncodeToString(treeTar.Bytes())
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: community-repo-fixtures
  namespace: %s
data:
  fixtures.tar.gz.b64: %s
`, namespace, encoded), nil
}

// communityRepoFixtureImage pins python:3-alpine to a manifest-list digest (rather
// than the mutable tag) so the fixture server can't silently change out from under
// CI, matching this project's own "never track a moving reference" policy for
// community repositories. Re-pin deliberately (docker pull python:3-alpine, then
// docker inspect --format='{{index .RepoDigests 0}}' python:3-alpine) when a newer
// Python/Alpine patch is wanted.
const communityRepoFixtureImage = "python:3-alpine@sha256:" +
	"26730869004e2b9c4b9ad09cab8625e81d256d1ce97e72df5520e806b1709f92"

// communityRepoFixtureServerYAML is the Deployment+Service that decodes/unpacks the
// ConfigMap above and serves it as a codeload-compatible file tree. Requests for
// the integration fixture from Python are rejected until the E2E test creates a
// recovery marker; Go requests from operator-side validation remain available.
// This deterministically exercises init-container retry without using real GitHub.
func communityRepoFixtureServerYAML(namespace string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: community-repo-fixture-server
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: community-repo-fixture-server
  template:
    metadata:
      labels:
        app: community-repo-fixture-server
    spec:
      containers:
        - name: server
          image: %[2]s
          command: ["sh", "-c"]
          args:
            - |
              set -e
              mkdir -p /data
              base64 -d /cm/fixtures.tar.gz.b64 > /tmp/fixtures.tar.gz
              tar -C /data -xzf /tmp/fixtures.tar.gz
              cat > /tmp/server.py <<'PY'
              import os
              from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

              INTEGRATION_PATH = '/acme/integration-fixture/tar.gz/v1.0.0'
              FAILURE_MARKER = '/tmp/integration-download-failed'
              RECOVERY_MARKER = '/tmp/allow-integration-download'

              class Handler(SimpleHTTPRequestHandler):
                  def __init__(self, *args, **kwargs):
                      super().__init__(*args, directory='/data', **kwargs)

                  def do_GET(self):
                      user_agent = self.headers.get('User-Agent', '')
                      if (self.path == INTEGRATION_PATH and user_agent.startswith('Python-urllib')
                              and not os.path.exists(RECOVERY_MARKER)):
                          open(FAILURE_MARKER, 'a').close()
                          self.send_error(503, 'simulated transient integration download failure')
                          return
                      super().do_GET()

              ThreadingHTTPServer(('0.0.0.0', 8080), Handler).serve_forever()
              PY
              exec python3 -u /tmp/server.py
          volumeMounts:
            - name: fixtures-cm
              mountPath: /cm
          ports:
            - containerPort: 8080
      volumes:
        - name: fixtures-cm
          configMap:
            name: community-repo-fixtures
---
apiVersion: v1
kind: Service
metadata:
  name: community-repo-fixture-server
  namespace: %[1]s
spec:
  selector:
    app: community-repo-fixture-server
  ports:
    - port: 8080
      targetPort: 8080
`, namespace, communityRepoFixtureImage)
}

var _ = Describe("HomeAssistantCommunityRepository E2E", Ordered, ContinueOnFailure, func() {
	var (
		namespace       string
		haName          string
		origCodeloadEnv string
	)

	getPhase := func(name string) string {
		return utils.Kubectl("get", "hacr", name, "-n", namespace, "-o", "jsonpath={.status.phase}")
	}
	getInstalledVersion := func(name string) string {
		return utils.Kubectl("get", "hacr", name, "-n", namespace, "-o", "jsonpath={.status.installedVersion}")
	}
	getReadyMessage := func(name string) string {
		return utils.Kubectl("get", "hacr", name, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].message}")
	}

	BeforeAll(func() {
		namespace = "cr-e2e-" + utils.RandomString(8)
		haName = "cr-e2e-ha"

		By("Creating test namespace: " + namespace)
		Expect(utils.CreateNamespace(namespace)).To(Succeed())

		By("Deploying the community-repository fixture server (stand-in for codeload.github.com)")
		fixtureCM, err := buildCommunityRepoFixtureTreeConfigMap(namespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(utils.ApplyYAML(fixtureCM, namespace)).To(Succeed())
		Expect(utils.ApplyYAML(communityRepoFixtureServerYAML(namespace), namespace)).To(Succeed())
		Eventually(func(g Gomega) {
			available := utils.Kubectl("get", "deployment", "community-repo-fixture-server", "-n", namespace,
				"-o", "jsonpath={.status.availableReplicas}")
			g.Expect(available).To(Equal("1"))
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())

		By("Pointing the operator at the fixture server (test-only env var)")
		origCodeloadEnv = utils.Kubectl("get", "deployment", "homeassistant-operator-controller-manager",
			"-n", "homeassistant-operator-system",
			"-o", "jsonpath={.spec.template.spec.containers[?(@.name=='manager')].env[?"+
				"(@.name=='COMMUNITY_REPOSITORY_CODELOAD_BASE_URL')].value}")
		// A trailing dot marks the hostname fully-qualified, skipping resolver
		// search-domain expansion — harmless everywhere and avoids environments
		// (observed on at least one dev machine) where an inherited non-cluster
		// search list makes plain "ndots<5" lookups fail or time out.
		fixtureURL := fmt.Sprintf("http://community-repo-fixture-server.%s.svc.cluster.local.:8080", namespace)
		cmd := exec.Command("kubectl", "set", "env",
			"deployment/homeassistant-operator-controller-manager",
			"-n", "homeassistant-operator-system", "-c", "manager",
			"COMMUNITY_REPOSITORY_CODELOAD_BASE_URL="+fixtureURL)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "rollout", "status",
				"deployment/homeassistant-operator-controller-manager",
				"-n", "homeassistant-operator-system", "--timeout=60s")
			_, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
		}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())

		By("Creating bootstrap credentials Secret")
		credsYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: cr-e2e-bootstrap-creds
  namespace: %s
type: Opaque
stringData:
  username: admin
  password: e2e-community-repo-pwd-123456
`, namespace)
		Expect(utils.ApplyYAML(credsYAML, namespace)).To(Succeed())

		By("Creating HomeAssistant CR with bootstrap enabled")
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
        name: cr-e2e-bootstrap-creds
    createApiToken: true
    apiTokenSecretName: %s-api-token
    ownerName: "CR E2E"
    language: "en"
  %s
`, haName, namespace, haVersion(), haName, utils.GetEnhancedHAResourceRequests())
		Expect(utils.ApplyYAML(haYAML, namespace)).To(Succeed())

		By("Creating HomeAssistantConfiguration CR (python_script enabled for the python_script scenario)")
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
    default_config:
    python_script:
`, haName, namespace, haName)
		Expect(utils.ApplyYAML(configYAML, namespace)).To(Succeed())

		By("Waiting for bootstrap to complete")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "ha", haName, "-n", namespace,
				"-o", "jsonpath={.status.bootstrap.completed}")
			g.Expect(output).To(Equal("true"))
		}, utils.BootstrapTimeout, bootstrapInterval).Should(Succeed())

		By("Waiting for the HA pod to be fully Ready")
		Eventually(func(g Gomega) {
			phase := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace, "-o", "jsonpath={.status.phase}")
			g.Expect(phase).To(Equal("Running"))
			ready := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			g.Expect(ready).To(Equal("True"))
		}, utils.HAPodReadyTimeout, haPodReadyInterval).Should(Succeed())
	})

	AfterAll(func() {
		By("Restoring the operator's codeload base URL env var")
		var cmd *exec.Cmd
		if origCodeloadEnv == "" {
			cmd = exec.Command("kubectl", "set", "env",
				"deployment/homeassistant-operator-controller-manager",
				"-n", "homeassistant-operator-system", "-c", "manager",
				"COMMUNITY_REPOSITORY_CODELOAD_BASE_URL-")
		} else {
			cmd = exec.Command("kubectl", "set", "env",
				"deployment/homeassistant-operator-controller-manager",
				"-n", "homeassistant-operator-system", "-c", "manager",
				"COMMUNITY_REPOSITORY_CODELOAD_BASE_URL="+origCodeloadEnv)
		}
		_, envErr := utils.Run(cmd)

		By("Waiting for the operator to finish rolling out with the restored env var")
		rolloutCmd := exec.Command("kubectl", "rollout", "status",
			"deployment/homeassistant-operator-controller-manager",
			"-n", "homeassistant-operator-system", "--timeout=60s")
		_, rolloutErr := utils.Run(rolloutCmd)

		By("Deleting test namespace: " + namespace)
		_ = utils.DeleteNamespace(namespace)

		Expect(envErr).NotTo(HaveOccurred(), "failed to restore the operator's codeload base URL env var")
		Expect(rolloutErr).NotTo(HaveOccurred(),
			"operator did not finish rolling out after the env var was restored")
	})

	It("retries a failed integration download before reporting the repository and config entry Ready",
		Label("community-repository", "fast", "group-a"), func() {
			podStartBefore := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace, "-o", "jsonpath={.status.startTime}")

			hacrYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantCommunityRepository
metadata:
  name: e2e-integration
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  category: integration
  repository: %s
  ref: v1.0.0
`, namespace, haName, crFixtureIntegration)
			Expect(utils.ApplyYAML(hacrYAML, namespace)).To(Succeed())

			By("Creating a dependent HomeAssistantIntegration while the custom integration is not materialized")
			integrationYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantIntegration
metadata:
  name: e2e-custom-integration
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  domain: example_integration
`, namespace, haName)
			Expect(utils.ApplyYAML(integrationYAML, namespace)).To(Succeed())

			By("Waiting for the fixture server to reject an init-container download")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "exec", "deployment/community-repo-fixture-server",
					"-n", namespace, "--", "test", "-f", "/tmp/integration-download-failed")
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}, utils.RestartTimeout, reconcileInterval).Should(Succeed())

			By("Confirming the failed materialization is not reported as Installed")
			Eventually(func(g Gomega) {
				g.Expect(getPhase("e2e-integration")).To(Equal("Installing"))
				reason := utils.Kubectl("get", "hacr", "e2e-integration", "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].reason}")
				g.Expect(reason).To(Equal("MaterializationFailed"))
				ready := utils.Kubectl("get", "hacr", "e2e-integration", "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				g.Expect(ready).To(Equal("False"))
				g.Expect(getInstalledVersion("e2e-integration")).To(BeEmpty())
			}, utils.RestartTimeout, reconcileInterval).Should(Succeed())

			failedPodUID := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
				"-o", "jsonpath={.metadata.uid}")
			Eventually(func(g Gomega) {
				restarts := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
					"-o", "jsonpath={.status.initContainerStatuses[?(@.name=='community-repository-init')].restartCount}")
				g.Expect(restarts).To(MatchRegexp(`^[1-9][0-9]*$`))
			}, utils.RestartTimeout, reconcileInterval).Should(Succeed())

			By("Restoring fixture connectivity without restarting the Home Assistant pod")
			recoverCmd := exec.Command("kubectl", "exec", "deployment/community-repo-fixture-server",
				"-n", namespace, "--", "touch", "/tmp/allow-integration-download")
			_, err := utils.Run(recoverCmd)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(getPhase("e2e-integration")).To(Equal("Installed"))
			}, utils.HAPodReadyTimeout, reconcileInterval).Should(Succeed())
			Expect(getInstalledVersion("e2e-integration")).To(Equal("v1.0.0"))
			Expect(utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
				"-o", "jsonpath={.metadata.uid}")).To(Equal(failedPodUID),
				"kubelet must retry the init container in the same pod")

			By("Waiting for the pod restart the integration category requires")
			Eventually(func(g Gomega) {
				podStartAfter := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace, "-o", "jsonpath={.status.startTime}")
				g.Expect(podStartAfter).NotTo(Equal(podStartBefore))
			}, utils.RestartTimeout, haPodReadyInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				phase := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace, "-o", "jsonpath={.status.phase}")
				g.Expect(phase).To(Equal("Running"))
				ready := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				g.Expect(ready).To(Equal("True"))
			}, utils.HAPodReadyTimeout, haPodReadyInterval).Should(Succeed())

			By("Verifying the integration's files are present on the pod")
			cmd := exec.Command("kubectl", "exec", haName+"-0", "-n", namespace, "-c", "home-assistant", "--",
				"test", "-f", "/config/custom_components/example_integration/manifest.json")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the dependent HomeAssistantIntegration config flow to recover")
			Eventually(func(g Gomega) {
				ready := utils.Kubectl("get", "haint", "e2e-custom-integration", "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				g.Expect(ready).To(Equal("True"))
			}, utils.HotReloadTimeout, reconcileInterval).Should(Succeed())
		})

	It("installs a theme-category repository without restarting the HA pod",
		Label("community-repository", "fast", "group-a"), func() {
			podStartBefore := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace, "-o", "jsonpath={.status.startTime}")

			hacrYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantCommunityRepository
metadata:
  name: e2e-theme
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  category: theme
  repository: %s
  ref: v1.0.0
`, namespace, haName, crFixtureTheme)
			Expect(utils.ApplyYAML(hacrYAML, namespace)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(getPhase("e2e-theme")).To(Equal("Installed"))
			}, crActivationSettleDelay+utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())

			podStartAfter := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace, "-o", "jsonpath={.status.startTime}")
			Expect(podStartAfter).To(Equal(podStartBefore), "theme install must not restart the pod")

			By("Waiting for the sidecar to materialize the theme file (~poll interval + propagation)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "exec", haName+"-0", "-n", namespace, "-c", "home-assistant", "--",
					"test", "-f", "/config/themes/example_theme.yaml")
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}, crActivationSettleDelay+utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())
		})

	It("installs a python_script-category repository",
		Label("community-repository", "fast", "group-b"), func() {
			hacrYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantCommunityRepository
metadata:
  name: e2e-python-script
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  category: python_script
  repository: %s
  ref: v1.0.0
`, namespace, haName, crFixturePythonScript)
			Expect(utils.ApplyYAML(hacrYAML, namespace)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(getPhase("e2e-python-script")).To(Equal("Installed"))
			}, crActivationSettleDelay+utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())

			By("Waiting for the sidecar to materialize the python_script file (~poll interval + propagation)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "exec", haName+"-0", "-n", namespace, "-c", "home-assistant", "--",
					"test", "-f", "/config/python_scripts/example_script.py")
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}, crActivationSettleDelay+utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())
		})

	It("installs a template-category repository",
		Label("community-repository", "fast", "group-b"), func() {
			hacrYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantCommunityRepository
metadata:
  name: e2e-template
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  category: template
  repository: %s
  ref: v1.0.0
`, namespace, haName, crFixtureTemplate)
			Expect(utils.ApplyYAML(hacrYAML, namespace)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(getPhase("e2e-template")).To(Equal("Installed"))
			}, crActivationSettleDelay+utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())

			By("Waiting for the sidecar to materialize the template file (~poll interval + propagation)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "exec", haName+"-0", "-n", namespace, "-c", "home-assistant", "--",
					"test", "-f", "/config/custom_templates/example_template.jinja")
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}, crActivationSettleDelay+utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())
		})

	It("installs a plugin-category repository and registers its Lovelace resource",
		Label("community-repository", "slow", "group-b"), func() {
			hacrYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantCommunityRepository
metadata:
  name: e2e-plugin
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  category: plugin
  repository: %s
  ref: v1.0.0
`, namespace, haName, crFixturePlugin)
			Expect(utils.ApplyYAML(hacrYAML, namespace)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(getPhase("e2e-plugin")).To(Equal("Installed"))
			}, crActivationSettleDelay+utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())

			// The Ready message distinguishes a confirmed Lovelace resource registration
			// ("reload confirmed") from the YAML-mode fallback path, which also reaches
			// Installed but never actually registers the resource via the API.
			Expect(getReadyMessage("e2e-plugin")).To(ContainSubstring("reload confirmed"),
				"the Lovelace resource must have been actually registered via the API, not just the file placed")

			By("Waiting for the sidecar to materialize the plugin file (~poll interval + propagation)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "exec", haName+"-0", "-n", namespace, "-c", "home-assistant", "--",
					"test", "-f", "/config/www/community/example-card.js")
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}, crActivationSettleDelay+utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())
		})

	It("keeps installedVersion at the old ref until a ref update is confirmed, then updates it",
		Label("community-repository", "fast", "group-a"), func() {
			Expect(getPhase("e2e-theme")).To(Equal("Installed"))
			Expect(getInstalledVersion("e2e-theme")).To(Equal("v1.0.0"))

			Expect(utils.PatchResource("hacr", "e2e-theme", namespace, "merge", `{"spec":{"ref":"v2.0.0"}}`)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(getPhase("e2e-theme")).To(Equal("Installed"))
				g.Expect(getInstalledVersion("e2e-theme")).To(Equal("v2.0.0"))
			}, crActivationSettleDelay+utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())

			By("Waiting for the sidecar to materialize the updated theme file (~poll interval + propagation)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "exec", haName+"-0", "-n", namespace, "-c", "home-assistant", "--",
					"grep", "-q", "version: 2", "/config/themes/example_theme.yaml")
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}, crActivationSettleDelay+utils.ReconciliationTimeout, reconcileInterval).Should(Succeed(),
				"theme file content must reflect v2.0.0")
		})

	It("removes the ConfigMap entry and the materialized file on deletion",
		Label("community-repository", "fast", "group-b"), func() {
			Expect(getPhase("e2e-python-script")).To(Equal("Installed"))

			cmd := exec.Command("kubectl", "delete", "hacr", "e2e-python-script", "-n", namespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				output := utils.Kubectl("get", "hacr", "e2e-python-script", "-n", namespace)
				g.Expect(output).To(BeEmpty())
			}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				cmData := utils.Kubectl("get", "configmap", haName+"-community-repositories", "-n", namespace,
					"-o", "jsonpath={.data.repositories\\.json}")
				g.Expect(cmData).NotTo(BeEmpty(), "kubectl get configmap must succeed")
				g.Expect(cmData).NotTo(ContainSubstring("example_script"))
			}, utils.ResourceTimeout, reconcileInterval).Should(Succeed())

			By("Waiting for the sidecar to remove the file from the pod (~poll interval + propagation)")
			Eventually(func(g Gomega) {
				// "test -f" alone would exit non-zero both when the file is genuinely
				// absent and when kubectl exec itself fails to reach the container —
				// echoing an explicit marker lets the assertion tell those apart instead
				// of treating a transient connectivity failure as a false-positive pass.
				cmd := exec.Command("kubectl", "exec", haName+"-0", "-n", namespace, "-c", "home-assistant", "--",
					"sh", "-c", "test -f /config/python_scripts/example_script.py && echo PRESENT || echo ABSENT")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "kubectl exec must reach the home-assistant container")
				g.Expect(output).To(ContainSubstring("ABSENT"), "file must be removed once the sidecar picks up the deletion")
			}, crActivationSettleDelay+utils.ReconciliationTimeout, reconcileInterval).Should(Succeed())
		})
})
