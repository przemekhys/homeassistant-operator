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
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os/exec"
	"strings"
	"time"

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

		By("Creating bootstrap credentials Secret (reconcileHTTPConfigViaWS needs the bootstrap " +
			"API token to attempt http/config at all — without it every reconcile short-circuits " +
			"straight to reasonWSConfigUnsupported, regardless of what HA is actually doing)")
		credsYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: ha-native-creds
  namespace: %s
type: Opaque
stringData:
  username: admin
  password: e2e-native-tls-pwd-123456
`, namespace)
		Expect(utils.ApplyYAML(credsYAML, namespace)).To(Succeed())
	})

	AfterEach(func() {
		Expect(utils.ForceDeleteNamespace(namespace)).To(Succeed())
	})

	// Labeled "bootstrap" (on top of the Describe-level "tls"/"native" labels)
	// so it can run in its own CI job/Makefile target: this is the only tls
	// spec that combines spec.bootstrap with spec.alpha.tls.native on the same
	// instance, a combination no other spec exercises. haScheme(ha) flips to
	// "https" as soon as TLSReady is True — which reconcileHTTPConfigViaWS
	// sets via the WSConfigUnsupported fallback the moment cert-manager issues
	// the certificate, before the pod has necessarily picked up and restarted
	// onto the injected YAML ssl_certificate/ssl_key. Bootstrap's own
	// scheme-aware HTTP client then has to wait out that separate,
	// asynchronous YAML-injection-plus-restart cycle before its first
	// successful health check — real extra latency a plain-HTTP bootstrap
	// never pays. Combined with 4-5 other tls specs sharing a job, this pushed
	// a real CI run to blow a 13-minute budget before bootstrap ever
	// completed.
	It("issues a native TLS certificate and reports TLSReady", Label("bootstrap"), func() {
		By("Creating a HomeAssistant with native TLS enabled and bootstrap enabled " +
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
  alpha:
    tls:
      native:
        enabled: true
        issuerRef:
          name: %s
          kind: ClusterIssuer
        dnsNames:
          - %s.example.com
  bootstrap:
    enabled: true
    credentials:
      secretRef:
        name: ha-native-creds
    createApiToken: true
    apiTokenSecretName: %s-homeassistant-api-token
    ownerName: "E2E Native TLS"
    language: "en"
  %s`, haName, namespace, clusterIssuer, haName, haName, utils.GetDefaultHAResourceRequests())
		Expect(utils.ApplyYAML(haYAML, namespace)).To(Succeed())

		By("Waiting for bootstrap to complete (reconcileHTTPConfigViaWS needs the API token)")
		Eventually(func(g Gomega) {
			output := utils.Kubectl("get", "ha", haName, "-n", namespace,
				"-o", "jsonpath={.status.bootstrap.completed}")
			g.Expect(output).To(Equal("true"))
		}, utils.BootstrapTimeout, utils.DefaultEventuallyPollingInterval).Should(Succeed())

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
			// Bring-your-own (spec.alpha.tls.native.secretName), not
			// cert-manager: cert-manager watches its own issued Secrets and
			// re-issues fresh material the moment it notices the stored key
			// no longer matches the certificate — which is exactly the fault
			// this test injects below. That self-healing races the thing
			// actually under test (HA's own WS-based auto-revert) and
			// silently overwrites the injected key before the assertions
			// below can observe it. A Secret cert-manager never owns doesn't
			// have that problem.
			certName := haName + "-byo-tls"
			By("Creating a self-signed bring-your-own TLS Secret (kept outside cert-manager on purpose)")
			certPEM, keyPEM := generateSelfSignedCertPEM(haName + "." + namespace + ".svc.cluster.local")
			secretYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: kubernetes.io/tls
stringData:
  tls.crt: |
%s
  tls.key: |
%s
  ca.crt: |
%s
`, certName, namespace, indentPEM(certPEM), indentPEM(keyPEM), indentPEM(certPEM))
			Expect(utils.ApplyYAML(secretYAML, namespace)).To(Succeed())

			By("Creating a HomeAssistant with bring-your-own native TLS enabled and bootstrap enabled " +
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
  alpha:
    tls:
      native:
        enabled: true
        secretName: %s
  bootstrap:
    enabled: true
    credentials:
      secretRef:
        name: ha-native-creds
    createApiToken: true
    apiTokenSecretName: %s-homeassistant-api-token
    ownerName: "E2E Native TLS Revert"
    language: "en"
  %s`, haName, namespace, certName, haName, utils.GetDefaultHAResourceRequests())
			Expect(utils.ApplyYAML(haYAML, namespace)).To(Succeed())

			By("Waiting for bootstrap to complete (reconcileHTTPConfigViaWS needs the API token)")
			Eventually(func(g Gomega) {
				output := utils.Kubectl("get", "ha", haName, "-n", namespace,
					"-o", "jsonpath={.status.bootstrap.completed}")
				g.Expect(output).To(Equal("true"))
			}, utils.BootstrapTimeout, utils.DefaultEventuallyPollingInterval).Should(Succeed())

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
			restartCountBefore := haPodRestartCount(namespace, haName)
			Expect(utils.PatchResource("secret", certName, namespace, "merge", patch)).To(Succeed())

			By("Waiting for HA to auto-revert and the operator to report TLSConfigReverted")
			Eventually(func() string {
				return utils.GetResourceStatus("homeassistants", haName, namespace,
					"{.status.conditions[?(@.type=='TLSReady')].reason}")
			}, utils.NativeTLSAutoRevertTimeout, utils.DefaultEventuallyPollingInterval).Should(Equal("TLSConfigReverted"))

			By("Confirming HA is still reachable/TLSReady=True throughout (never permanently unavailable)")
			Expect(utils.GetResourceStatus("homeassistants", haName, namespace,
				"{.status.conditions[?(@.type=='TLSReady')].status}")).To(Equal("True"))

			By("Confirming the pod was never restarted (LivenessProbe must not kill the container while " +
				"HA is still running its own internal auto-revert timer)")
			Expect(haPodRestartCount(namespace, haName)).To(Equal(restartCountBefore),
				"a liveness-probe-triggered restart would cut HA's in-process auto-revert timer short, "+
					"forcing the same failed trial to start over on every boot")

			By("Confirming the Secret still carries the corrupted key and untouched cert " +
				"(operator does not fight the rejection)")
			Expect(utils.GetResourceStatus("secret", certName, namespace, "{.data.tls\\.crt}")).To(Equal(goodCert))
			Expect(utils.GetResourceStatus("secret", certName, namespace, "{.data.tls\\.key}")).
				To(Equal(base64.StdEncoding.EncodeToString(mismatchedKeyPEM)),
					"expected the operator to leave the mismatched key exactly as injected, never reverting it")
		})
})

// generateSelfSignedCertPEM returns a self-signed leaf certificate (also
// usable as its own trust root, IsCA: true) and its matching private key, PEM
// encoded, valid for dnsName. Used for the bring-your-own native TLS Secret in
// the auto-revert test — kept outside cert-manager on purpose, see that It's
// own comment for why.
func generateSelfSignedCertPEM(dnsName string) (certPEM, keyPEM []byte) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	Expect(err).NotTo(HaveOccurred())

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dnsName},
		DNSNames:              []string{dnsName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}

// indentPEM re-indents PEM content by 4 spaces so it nests correctly under a
// YAML literal block scalar (`key: |`) inside a Secret manifest built with
// fmt.Sprintf.
func indentPEM(pemBytes []byte) string {
	lines := strings.Split(strings.TrimRight(string(pemBytes), "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

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
