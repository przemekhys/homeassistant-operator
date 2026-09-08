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
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/przemekhys/homeassistant-operator/test/utils"
)

// Exposure TLS E2E. Verifies the operator provisions the edge certificate and
// manages the exposure resources
// (Ingress, and — when Gateway API CRDs are present — HTTPRoute).
var _ = Describe("Exposure TLS E2E", Label("tls", "exposure"), func() {
	const clusterIssuer = "ha-e2e-selfsigned"

	var (
		namespace  string
		haName     string
		configName string
	)

	BeforeEach(func() {
		if !utils.IsCertManagerCRDsInstalled() {
			By("Installing cert-manager (not present)")
			Expect(utils.InstallCertManager()).To(Succeed())
		}
		issuerYAML := fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: %s
spec:
  selfSigned: {}
`, clusterIssuer)
		Expect(utils.ApplyYAML(issuerYAML, "")).To(Succeed())

		suffix := utils.RandomString(8)
		namespace = "ha-e2e-tls-exposure-" + suffix
		haName = "ha-expose"
		configName = "ha-expose-config"
		Expect(utils.CreateNamespace(namespace)).To(Succeed())

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
`, configName, namespace, haName)
		Expect(utils.ApplyYAML(configYAML, namespace)).To(Succeed())
	})

	AfterEach(func() {
		Expect(utils.ForceDeleteNamespace(namespace)).To(Succeed())
	})

	It("provisions an Ingress with a cert-manager certificate", func() {
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
  ingress:
    enabled: true
    host: %s.example.com
    tls:
      enabled: true
      issuerRef:
        name: %s
        kind: ClusterIssuer
  %s`, haName, namespace, haName, clusterIssuer, utils.GetDefaultHAResourceRequests())
		Expect(utils.ApplyYAML(haYAML, namespace)).To(Succeed())

		By("Waiting for the ingress Certificate to be Ready")
		certName := haName + "-ingress-tls"
		Eventually(func() string {
			return utils.GetResourceStatus("certificate", certName, namespace,
				"{.status.conditions[?(@.type=='Ready')].status}")
		}, utils.CertIssueTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("True"))

		By("Verifying the Ingress exists and references the TLS Secret")
		Eventually(func() string {
			return utils.GetResourceStatus("ingress", haName, namespace, "{.spec.tls[0].secretName}")
		}, utils.ResourceTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal(certName))

		By("Waiting for the HomeAssistant to report ExposureReady")
		Eventually(func() string {
			return utils.GetResourceStatus("homeassistants", haName, namespace,
				"{.status.conditions[?(@.type=='ExposureReady')].status}")
		}, utils.CertIssueTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("True"))
	})

	It("manages a Gateway API HTTPRoute with a certificate", func() {
		if !gatewayAPIInstalled() {
			Skip("Gateway API CRDs not installed")
		}
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
  gateway:
    enabled: true
    host: %s.example.com
    issuerRef:
      name: %s
      kind: ClusterIssuer
    manageGateway: true
    gatewayClassName: traefik
    filters:
      - type: RequestRedirect
        requestRedirect:
          scheme: https
          statusCode: 301
      - type: ResponseHeaderModifier
        responseHeaderModifier:
          set:
            - name: X-Frame-Options
              value: SAMEORIGIN
  %s`, haName, namespace, haName, clusterIssuer, utils.GetDefaultHAResourceRequests())
		Expect(utils.ApplyYAML(haYAML, namespace)).To(Succeed())

		By("Verifying the managed Gateway selects the configured class")
		Eventually(func() string {
			return utils.GetResourceStatus("gateway", haName+"-gateway", namespace, "{.spec.gatewayClassName}")
		}, utils.ResourceTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("traefik"))

		By("Verifying the HTTPRoute is created with the correct hostname")
		Eventually(func() string {
			return utils.GetResourceStatus("httproute", haName, namespace, "{.spec.hostnames[0]}")
		}, utils.ResourceTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal(haName + ".example.com"))

		By("Verifying the HTTPRoute carries the declared filters, in order, with their exact fields")
		Eventually(func() string {
			return utils.GetResourceStatus("httproute", haName, namespace, "{.spec.rules[0].filters[*].type}")
		}, utils.ResourceTimeout, utils.DefaultEventuallyPollingInterval).Should(
			Equal("RequestRedirect ResponseHeaderModifier"))
		Expect(utils.GetResourceStatus("httproute", haName, namespace,
			"{.spec.rules[0].filters[0].requestRedirect.scheme}")).To(Equal("https"))
		Expect(utils.GetResourceStatus("httproute", haName, namespace,
			"{.spec.rules[0].filters[0].requestRedirect.statusCode}")).To(Equal("301"))
		Expect(utils.GetResourceStatus("httproute", haName, namespace,
			"{.spec.rules[0].filters[1].responseHeaderModifier.set[0].name}")).To(Equal("X-Frame-Options"))
		Expect(utils.GetResourceStatus("httproute", haName, namespace,
			"{.spec.rules[0].filters[1].responseHeaderModifier.set[0].value}")).To(Equal("SAMEORIGIN"))

		By("Waiting for the gateway Certificate to be Ready")
		Eventually(func() string {
			return utils.GetResourceStatus("certificate", haName+"-gateway-tls", namespace,
				"{.status.conditions[?(@.type=='Ready')].status}")
		}, utils.CertIssueTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("True"))

		By("Recording the HA pod start time before changing filters")
		var podStartBefore string
		Eventually(func() string {
			podStartBefore = utils.GetResourceStatus("pod", haName+"-0", namespace, "{.status.startTime}")
			return podStartBefore
		}, utils.ResourceTimeout, utils.DefaultEventuallyPollingInterval).ShouldNot(BeEmpty())

		By("Removing a filter and confirming the route updates without a pod restart")
		redirectOnlyPatch := `{"spec":{"gateway":{"filters":[` +
			`{"type":"RequestRedirect","requestRedirect":{"scheme":"https","statusCode":301}}` +
			`]}}}`
		Expect(utils.PatchResource("homeassistants", haName, namespace, "merge", redirectOnlyPatch)).To(Succeed())

		By("Verifying the route now carries exactly one RequestRedirect filter with its expected configuration")
		Eventually(func() string {
			return utils.GetResourceStatus("httproute", haName, namespace, "{.spec.rules[0].filters[*].type}")
		}, utils.ResourceTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("RequestRedirect"))
		Expect(utils.GetResourceStatus("httproute", haName, namespace,
			"{.spec.rules[0].filters[0].requestRedirect.scheme}")).To(Equal("https"))
		Expect(utils.GetResourceStatus("httproute", haName, namespace,
			"{.spec.rules[0].filters[0].requestRedirect.statusCode}")).To(Equal("301"))

		Expect(utils.GetResourceStatus("pod", haName+"-0", namespace, "{.status.startTime}")).
			To(Equal(podStartBefore), "changing spec.gateway.filters must not restart the HA pod")

		By("Changing the selected GatewayClass in place")
		Expect(utils.PatchResource("homeassistants", haName, namespace, "merge",
			`{"spec":{"gateway":{"gatewayClassName":"cilium"}}}`)).To(Succeed())
		Eventually(func() string {
			return utils.GetResourceStatus("gateway", haName+"-gateway", namespace, "{.spec.gatewayClassName}")
		}, utils.ResourceTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("cilium"))

		By("Removing the selection and restoring the default class")
		Expect(utils.PatchResource("homeassistants", haName, namespace, "merge",
			`{"spec":{"gateway":{"gatewayClassName":null}}}`)).To(Succeed())
		Eventually(func() string {
			return utils.GetResourceStatus("gateway", haName+"-gateway", namespace, "{.spec.gatewayClassName}")
		}, utils.ResourceTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("traefik"))
		Expect(utils.GetResourceStatus("pod", haName+"-0", namespace, "{.status.startTime}")).
			To(Equal(podStartBefore), "changing spec.gateway.gatewayClassName must not restart the HA pod")
	})
})

// gatewayAPIInstalled reports whether the Gateway API HTTPRoute CRD is present.
func gatewayAPIInstalled() bool {
	cmd := exec.Command("kubectl", "get", "crd", "httproutes.gateway.networking.k8s.io")
	_, err := utils.Run(cmd)
	return err == nil
}
