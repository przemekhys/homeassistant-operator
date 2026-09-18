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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Community repository pod materialization", func() {
	It("changes the rollout hash only when integration entries change", func() {
		base := `{"repositories":[` +
			`{"category":"integration","repository":"acme/integration","ref":"v1",` +
			`"resolvedTarget":"example","sourcePath":"custom_components/example"},` +
			`{"category":"theme","repository":"acme/theme","ref":"v1",` +
			`"resolvedTarget":"theme","sourcePath":"themes/theme.yaml"}]}`
		themeUpdated := `{"repositories":[` +
			`{"category":"integration","repository":"acme/integration","ref":"v1",` +
			`"resolvedTarget":"example","sourcePath":"custom_components/example"},` +
			`{"category":"theme","repository":"acme/theme","ref":"v2",` +
			`"resolvedTarget":"theme","sourcePath":"themes/theme.yaml"}]}`
		integrationUpdated := `{"repositories":[` +
			`{"category":"integration","repository":"acme/integration","ref":"v2",` +
			`"resolvedTarget":"example","sourcePath":"custom_components/example"},` +
			`{"category":"theme","repository":"acme/theme","ref":"v2",` +
			`"resolvedTarget":"theme","sourcePath":"themes/theme.yaml"}]}`

		baseHash := calculateIntegrationRepositoryHash(base)
		Expect(calculateIntegrationRepositoryHash(themeUpdated)).To(Equal(baseHash))
		Expect(calculateIntegrationRepositoryHash(integrationUpdated)).NotTo(Equal(baseHash))
	})

	Context("integration init script", func() {
		var fixtureServer *httptest.Server

		BeforeEach(func() {
			if _, err := exec.LookPath("python3"); err != nil {
				Skip("python3 not available")
			}
		})

		AfterEach(func() {
			if fixtureServer != nil {
				fixtureServer.Close()
			}
		})

		runScript := func(configDir, repositoryConfig, script string) ([]byte, error) {
			repositoryFile := filepath.Join(configDir, "repositories.json")
			Expect(os.WriteFile(repositoryFile, []byte(repositoryConfig), 0o600)).To(Succeed())
			terminationLog := filepath.Join(configDir, "termination.log")
			script = strings.Replace(script, "TERMINATION_LOG = '/dev/termination-log'",
				"TERMINATION_LOG = "+fmt.Sprintf("%q", terminationLog), 1)
			cmd := exec.Command("python3", "-c", script)
			cmd.Env = append(os.Environ(),
				"COMMUNITY_REPOSITORIES_FILE="+repositoryFile,
				"COMMUNITY_REPOSITORIES_HASH="+calculateIntegrationRepositoryHash(repositoryConfig),
				"HA_CONFIG_DIR="+configDir,
			)
			if fixtureServer != nil {
				cmd.Env = append(cmd.Env, "CODELOAD_BASE_URL="+fixtureServer.URL)
			}
			return cmd.CombinedOutput()
		}

		It("removes only previously owned integrations absent from the current configuration", func() {
			configDir := GinkgoT().TempDir()
			componentsDir := filepath.Join(configDir, "custom_components")
			Expect(os.MkdirAll(filepath.Join(componentsDir, "owned"), 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(componentsDir, "foreign"), 0o755)).To(Succeed())
			state := `{"ownedTargets":["owned"]}`
			Expect(os.WriteFile(filepath.Join(configDir, ".community_repositories_integration_state.json"),
				[]byte(state), 0o600)).To(Succeed())

			output, err := runScript(configDir, `{"repositories":[]}`, communityRepositoryInitScript)
			Expect(err).NotTo(HaveOccurred(), string(output))
			Expect(filepath.Join(componentsDir, "owned")).NotTo(BeADirectory())
			Expect(filepath.Join(componentsDir, "foreign")).To(BeADirectory())
		})

		It("restores the previous destination when replacement cannot complete", func() {
			fixture := buildFixtureTarball("integration-v1", map[string]string{
				"custom_components/example/manifest.json": `{"domain":"example"}`,
				"custom_components/example/version":       "new",
			})
			fixtureServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(fixture)
			}))

			configDir := GinkgoT().TempDir()
			dest := filepath.Join(configDir, "custom_components", "example")
			Expect(os.MkdirAll(dest, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dest, "version"), []byte("old"), 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(configDir, ".community_repositories_integration_state.json"),
				[]byte(`{"ownedTargets":["example"]}`), 0o600)).To(Succeed())
			config := `{"repositories":[{"category":"integration","repository":"acme/integration",` +
				`"ref":"v1","resolvedTarget":"example","sourcePath":"custom_components/example"}]}`
			failingScript := strings.Replace(communityRepositoryInitScript,
				"os.replace(staged, dest)", "raise RuntimeError('simulated replacement failure')", 1)

			_, err := runScript(configDir, config, failingScript)
			Expect(err).To(HaveOccurred())
			content, readErr := os.ReadFile(filepath.Join(dest, "version"))
			Expect(readErr).NotTo(HaveOccurred())
			Expect(string(content)).To(Equal("old"))
			Expect(filepath.Join(configDir, "custom_components", ".community-repository-backup-example")).
				NotTo(BeAnExistingFile())
		})

		It("recovers a backup left by an interrupted prior run before downloading", func() {
			fixtureServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}))
			configDir := GinkgoT().TempDir()
			componentsDir := filepath.Join(configDir, "custom_components")
			backup := filepath.Join(componentsDir, ".community-repository-backup-example")
			Expect(os.MkdirAll(backup, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(backup, "version"), []byte("old"), 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(configDir, ".community_repositories_integration_state.json"),
				[]byte(`{"ownedTargets":["example"]}`), 0o600)).To(Succeed())
			config := `{"repositories":[{"category":"integration","repository":"acme/integration",` +
				`"ref":"v1","resolvedTarget":"example","sourcePath":"custom_components/example"}]}`

			_, err := runScript(configDir, config, communityRepositoryInitScript)
			Expect(err).To(HaveOccurred())
			content, readErr := os.ReadFile(filepath.Join(componentsDir, "example", "version"))
			Expect(readErr).NotTo(HaveOccurred())
			Expect(string(content)).To(Equal("old"))
			Expect(backup).NotTo(BeAnExistingFile())
		})

		It("keeps the old owned target when a renamed replacement cannot be downloaded", func() {
			fixtureServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}))
			configDir := GinkgoT().TempDir()
			oldDest := filepath.Join(configDir, "custom_components", "old_example")
			Expect(os.MkdirAll(oldDest, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(oldDest, "version"), []byte("old"), 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(configDir, ".community_repositories_integration_state.json"),
				[]byte(`{"ownedTargets":["old_example"]}`), 0o600)).To(Succeed())
			config := `{"repositories":[{"category":"integration","repository":"acme/integration",` +
				`"ref":"v2","resolvedTarget":"new_example","sourcePath":"custom_components/new_example"}]}`

			_, err := runScript(configDir, config, communityRepositoryInitScript)
			Expect(err).To(HaveOccurred())
			Expect(oldDest).To(BeADirectory())
		})
	})
})
