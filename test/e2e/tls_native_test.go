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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/przemekhys/homeassistant-operator/test/utils"
)

// Native TLS E2E. Verifies that the operator provisions a cert-manager
// Certificate for native TLS and reflects
// issuance in the HomeAssistant status. Uses a self-signed ClusterIssuer so the
// certificate is issued quickly and deterministically (no ACME).
var _ = Describe("Native TLS E2E", Label("tls", "native"), func() {
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

		By("Ensuring a self-signed ClusterIssuer exists")
		issuerYAML := fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: %s
spec:
  selfSigned: {}
`, clusterIssuer)
		Expect(utils.ApplyYAML(issuerYAML, "")).To(Succeed())

		suffix := utils.RandomString(8)
		namespace = "ha-e2e-tls-native-" + suffix
		haName = "ha-native"
		configName = "ha-native-config"

		By("Creating test namespace: " + namespace)
		Expect(utils.CreateNamespace(namespace)).To(Succeed())

		By("Creating HomeAssistantConfiguration")
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

	It("issues a native TLS certificate and reports TLSReady", func() {
		By("Creating a HomeAssistant with native TLS enabled")
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
  alpha:
    tls:
      native:
        enabled: true
        issuerRef:
          name: %s
          kind: ClusterIssuer
        dnsNames:
          - %s.example.com
  %s`, haName, namespace, clusterIssuer, haName, utils.GetDefaultHAResourceRequests())
		Expect(utils.ApplyYAML(haYAML, namespace)).To(Succeed())

		By("Waiting for the operator to report cert-manager available")
		Eventually(func() string {
			return utils.GetResourceStatus("homeassistants", haName, namespace,
				"{.status.conditions[?(@.type=='CertManagerAvailable')].status}")
		}, utils.CertIssueTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("True"))

		By("Waiting for the cert-manager Certificate to be created and Ready")
		certName := haName + "-native-tls"
		Eventually(func() string {
			return utils.GetResourceStatus("certificate", certName, namespace,
				"{.status.conditions[?(@.type=='Ready')].status}")
		}, utils.CertIssueTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("True"))

		By("Waiting for the HomeAssistant to report TLSReady")
		Eventually(func() string {
			return utils.GetResourceStatus("homeassistants", haName, namespace,
				"{.status.conditions[?(@.type=='TLSReady')].status}")
		}, utils.CertIssueTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("True"))

		By("Verifying the TLS Secret carries certificate material")
		Eventually(func() string {
			return utils.GetResourceStatus("secret", certName, namespace, "{.data.tls\\.crt}")
		}, utils.CertIssueTimeout, utils.DefaultEventuallyPollingInterval).ShouldNot(BeEmpty())

		By("Capturing the pod restart count before rotation (WS rotation must not restart the pod)")
		restartCountBefore := haPodRestartCount(namespace, haName)

		By("Rotating the native TLS certificate (deleting the Secret so cert-manager reissues it)")
		firstCert := utils.GetResourceStatus("secret", certName, namespace, "{.data.tls\\.crt}")
		Expect(utils.Kubectl("delete", "secret", certName, "-n", namespace)).NotTo(BeNil())
		var rotatedCert string
		Eventually(func() string {
			rotatedCert = utils.GetResourceStatus("secret", certName, namespace, "{.data.tls\\.crt}")
			return rotatedCert
		}, utils.CertIssueTimeout, utils.DefaultEventuallyPollingInterval).ShouldNot(Or(BeEmpty(), Equal(firstCert)))

		By("Confirming TLSReady returns to True after the rotation is applied")
		Eventually(func() string {
			return utils.GetResourceStatus("homeassistants", haName, namespace,
				"{.status.conditions[?(@.type=='TLSReady')].status}")
		}, utils.CertIssueTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("True"))

		By("Confirming HA's own mounted certificate file reflects the rotated content " +
			"(TLSReady=True alone doesn't prove which certificate is actually active)")
		Eventually(func(g Gomega) {
			mounted, err := haMountedCertBase64(namespace, haName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(mounted).To(Equal(rotatedCert))
		}, utils.CertIssueTimeout, utils.DefaultEventuallyPollingInterval).Should(Succeed())

		By("Verifying no pod restart occurred (rotation went through WS, not a StatefulSet rollout)")
		restartCountAfter := haPodRestartCount(namespace, haName)
		Expect(restartCountAfter).To(Equal(restartCountBefore),
			"native TLS rotation via WS must not restart the HA pod")
	})

	// Labeled "slow" (on top of the Describe-level "tls"/"native" labels) so
	// it can run in its own CI job/Makefile target instead of the main tls
	// job: it is the only e2e spec in this repo that waits out HA's ~5-6
	// minute internal auto-revert timer, which repeatedly blew the shared
	// tls job's local/CI time budget once combined with the other tls specs.
	It("keeps HA available on the old certificate when a rotation is rejected (auto-revert)",
		Label("slow"), func() {
			By("Creating a HomeAssistant with native TLS enabled")
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
  alpha:
    tls:
      native:
        enabled: true
        issuerRef:
          name: %s
          kind: ClusterIssuer
        dnsNames:
          - %s.example.com
  %s`, haName, namespace, clusterIssuer, haName, utils.GetDefaultHAResourceRequests())
			Expect(utils.ApplyYAML(haYAML, namespace)).To(Succeed())

			certName := haName + "-native-tls"
			By("Waiting for the HomeAssistant to report TLSReady on the original certificate")
			Eventually(func() string {
				return utils.GetResourceStatus("homeassistants", haName, namespace,
					"{.status.conditions[?(@.type=='TLSReady')].status}")
			}, utils.CertIssueTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("True"))
			goodCert := utils.GetResourceStatus("secret", certName, namespace, "{.data.tls\\.crt}")

			By("Swapping in a syntactically valid but mismatched TLS key (fails the handshake, not process startup)")
			// A syntactically invalid key (random bytes) tends to make aiohttp's
			// SSL context creation raise at process startup, crash-looping the
			// whole container before HA's own WS-internal trial/revert bookkeeping
			// (which lives in the HA process's event loop) ever gets a chance to
			// run for the full auto-revert window. A well-formed key that simply
			// doesn't match the certificate is the more realistic "rejected
			// rotation" fault: the process starts, but the specific new config is
			// unusable — the case HA's own auto-revert handling is designed for.
			mismatchedKeyPEM := generateMismatchedTLSKeyPEM()
			patch := fmt.Sprintf(`{"data":{"tls.key":%q}}`, base64.StdEncoding.EncodeToString(mismatchedKeyPEM))
			Expect(utils.PatchResource("secret", certName, namespace, "merge", patch)).To(Succeed())

			By("Waiting for HA to auto-revert and the operator to report TLSConfigReverted")
			Eventually(func() string {
				return utils.GetResourceStatus("homeassistants", haName, namespace,
					"{.status.conditions[?(@.type=='TLSReady')].reason}")
			}, utils.NativeTLSAutoRevertTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("TLSConfigReverted"))

			By("Confirming HA is still reachable/TLSReady=True throughout (never permanently unavailable)")
			Expect(utils.GetResourceStatus("homeassistants", haName, namespace,
				"{.status.conditions[?(@.type=='TLSReady')].status}")).To(Equal("True"))

			By("Confirming the Secret still carries the corrupted key and untouched cert " +
				"(operator does not fight the rejection)")
			Expect(utils.GetResourceStatus("secret", certName, namespace, "{.data.tls\\.crt}")).To(Equal(goodCert))
			Expect(utils.GetResourceStatus("secret", certName, namespace, "{.data.tls\\.key}")).
				To(Equal(base64.StdEncoding.EncodeToString(mismatchedKeyPEM)),
					"expected the operator to leave the mismatched key exactly as injected, never reverting it")
		})
})

// haPodRestartCount reads the HA pod's first-container restart count, used to
// assert that native TLS rotation via WS never restarts the pod.
func haPodRestartCount(namespace, haName string) string {
	return utils.GetResourceStatus("pod", "", namespace,
		"{.items[?(@.metadata.labels.app\\.kubernetes\\.io/instance=='"+haName+"')]"+
			".status.containerStatuses[0].restartCount}")
}

// haMountedCertBase64 reads the native TLS certificate HA's own container
// actually has mounted at the well-known ssl_certificate path and re-encodes
// it as base64 — directly comparable to a Secret's raw .data.tls\.crt value
// (also base64) — so a rotation assertion can confirm the pod's filesystem
// reflects the new certificate content, not just that the operator's
// TLSReady condition flipped back to True.
func haMountedCertBase64(namespace, haName string) (string, error) {
	cmd := exec.Command("kubectl", "exec", haName+"-0", "-n", namespace, "-c", "home-assistant", "--",
		"cat", "/config/ssl/tls.crt")
	out, err := utils.Run(cmd)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString([]byte(out)), nil
}

// generateMismatchedTLSKeyPEM returns a syntactically valid RSA private key
// (PEM-encoded, PKCS#1) that does not correspond to any certificate HA has —
// a well-formed key HA's TLS stack can load without crashing, but that fails
// the actual handshake, used to exercise the auto-revert scenario without
// crash-looping the whole HA process (see the "Swapping in..." step above).
func generateMismatchedTLSKeyPEM() []byte {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	Expect(err).NotTo(HaveOccurred())
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}
