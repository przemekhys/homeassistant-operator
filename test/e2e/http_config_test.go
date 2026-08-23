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
	"encoding/json"
	"fmt"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/przemekhys/homeassistant-operator/test/utils"
)

// http: configuration E2E. Verifies that a HomeAssistant WITHOUT native TLS
// still gets its non-TLS http: fields (cors_allowed_origins here) applied
// through HA's WS http/config/* API — the same mechanism native TLS rotation
// uses — rather than through configuration.yaml, which HA silently ignores
// for the http: section once it has migrated to its own internal storage.
// Kept in the "tls" label group (reuses the same make test-e2e-tls job) even
// though this scenario never enables native TLS, since it exercises the same
// reconcileHTTPConfigViaWS code path.
var _ = Describe("HTTP Config via WS E2E", Label("tls"), func() {
	var (
		namespace  string
		haName     string
		configName string
	)

	BeforeEach(func() {
		suffix := utils.RandomString(8)
		namespace = "ha-e2e-http-config-" + suffix
		haName = "ha-http-config"
		configName = "ha-http-config-cfg"

		By("Creating test namespace: " + namespace)
		Expect(utils.CreateNamespace(namespace)).To(Succeed())
	})

	AfterEach(func() {
		Expect(utils.ForceDeleteNamespace(namespace)).To(Succeed())
	})

	It("applies http: fields via WS on an instance without native TLS, and re-applies them on change", func() {
		applyConfig := func(origin string) {
			configYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantConfiguration
metadata:
  name: %s
  namespace: %s
spec:
  homeAssistantRef:
    name: %s
  configuration: |
    default_config:
    http:
      cors_allowed_origins:
        - %s
`, configName, namespace, haName, origin)
			Expect(utils.ApplyYAML(configYAML, namespace)).To(Succeed())
		}

		By("Creating HomeAssistantConfiguration with a custom http: block (no native TLS)")
		applyConfig("https://example.com")

		By("Creating bootstrap credentials Secret")
		credsYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: ha-http-config-creds
  namespace: %s
type: Opaque
stringData:
  username: admin
  password: e2e-http-config-pwd-123456
`, namespace)
		Expect(utils.ApplyYAML(credsYAML, namespace)).To(Succeed())

		By("Creating a HomeAssistant without spec.alpha.tls.native, with bootstrap enabled " +
			"(reconcileHTTPConfigViaWS needs the bootstrap API token to call http/config at all)")
		haYAML := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
kind: HomeAssistant
metadata:
  name: %s
  namespace: %s
spec:
  storage:
    size: 1Gi
  service:
    type: ClusterIP
    port: 8123
  bootstrap:
    enabled: true
    credentials:
      secretRef:
        name: ha-http-config-creds
    createApiToken: true
    apiTokenSecretName: %s-homeassistant-api-token
    ownerName: "E2E HTTP Config"
    language: "en"
  %s`, haName, namespace, haName, utils.GetDefaultHAResourceRequests())
		Expect(utils.ApplyYAML(haYAML, namespace)).To(Succeed())

		By("Waiting for bootstrap to complete (reconcileHTTPConfigViaWS needs the API token)")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "ha", haName, "-n", namespace,
				"-o", "jsonpath={.status.bootstrap.completed}")
			g.Expect(output).To(Equal("true"))
		}, utils.BootstrapTimeout, utils.DefaultEventuallyPollingInterval).Should(Succeed())

		By("Waiting for the HA pod to become ready")
		Eventually(func(g Gomega) {
			phase := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace, "-o", "jsonpath={.status.phase}")
			g.Expect(phase).To(Equal("Running"))
			ready := utils.Kubectl("get", "pod", haName+"-0", "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			g.Expect(ready).To(Equal("True"))
		}, utils.HAPodReadyTimeout, utils.DefaultEventuallyPollingInterval).Should(Succeed())

		By("Confirming the initial cors_allowed_origins reaches HA's own http config storage")
		Eventually(func(g Gomega) {
			origins, err := haHTTPStorageCORSOrigins(namespace, haName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(origins).To(ContainElement("https://example.com"))
		}, utils.HTTPConfigApplyTimeout, utils.DefaultEventuallyPollingInterval).Should(Succeed())

		By("Changing cors_allowed_origins in spec.configuration (YAML HA would otherwise ignore post-migration)")
		applyConfig("https://changed.example.com")

		By("Confirming the CHANGED value reaches HA via http/config/configure, not the ignored YAML, " +
			"and the old origin is actually gone (http/config/configure replaces the whole config, never merges)")
		Eventually(func(g Gomega) {
			origins, err := haHTTPStorageCORSOrigins(namespace, haName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(origins).To(ContainElement("https://changed.example.com"))
			g.Expect(origins).NotTo(ContainElement("https://example.com"))
		}, utils.HTTPConfigApplyTimeout, utils.DefaultEventuallyPollingInterval).Should(Succeed())

		By("Confirming TLSReady was never set on an instance that never requested native TLS")
		cond := utils.GetResourceStatus("homeassistants", haName, namespace,
			"{.status.conditions[?(@.type=='TLSReady')].status}")
		Expect(cond).To(BeEmpty())
	})
})

// haHTTPStorageCORSOrigins reads HA's own http/config-managed storage file
// (.storage/http, HA_STORAGE_KEY "http") straight out of the pod and returns
// stable.cors_allowed_origins — the field HA itself confirms is active,
// independent of anything the operator believes it sent.
func haHTTPStorageCORSOrigins(namespace, haName string) ([]string, error) {
	cmd := exec.Command("kubectl", "exec", haName+"-0", "-n", namespace, "-c", "home-assistant", "--",
		"cat", "/config/.storage/http")
	out, err := utils.Run(cmd)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Data struct {
			Stable struct {
				CORSAllowedOrigins []string `json:"cors_allowed_origins"`
			} `json:"stable"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("parsing .storage/http: %w (raw: %s)", err, out)
	}
	return parsed.Data.Stable.CORSAllowedOrigins, nil
}
