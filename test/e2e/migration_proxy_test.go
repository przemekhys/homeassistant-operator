package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/test/utils"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

func TestMigrationOnboardingProxyOnCluster(t *testing.T) {
	if os.Getenv("HA_MIGRATION_PROXY_CLUSTER") != "true" {
		t.Skip("requires disposable proxy qualification cluster")
	}
	image := os.Getenv("MIGRATION_WORKER_IMAGE")
	if image == "" {
		t.Fatal("unique worker image required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{ha.AddToScheme, corev1.AddToScheme, batchv1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	kube, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ns := "migration-proxy-" + utils.RandomString(8)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	create := func(obj client.Object) {
		t.Helper()
		if err := kube.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	create(namespace)
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("MIGRATION_BACKUP_KEEP_FAILED") == "true" {
			t.Log("retained proxy qualification namespace:", ns)
			return
		}
		backupCleanupNamespace(t, kube, namespace)
	})
	ca, server, worker, other := proxyTestCredentials(t)
	foreignExpected := "403"
	if os.Getenv("HA_MIGRATION_PROXY_GENERATED") == "true" {
		generated, err := migration.GenerateProxyCredentials(time.Now(), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := migration.ValidateProxyCredentials(generated, time.Now()); err != nil {
			t.Fatal(err)
		}
		ca = generated.CA
		server = [2][]byte{generated.ServerCertificate, generated.ServerKey}
		worker = [2][]byte{generated.WorkerCertificate, generated.WorkerKey}
		// The alternate client is signed by the fixture's different CA.
		foreignExpected = "denied"
	}
	create(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: ns}, Data: map[string][]byte{"server.crt": server[0], "server.key": server[1], "client-ca.crt": ca, "client.crt": worker[0]}})
	create(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: ns}, Data: map[string][]byte{"ca.crt": ca, "client.crt": worker[0], "client.key": worker[1]}})
	create(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: ns}, Data: map[string][]byte{"ca.crt": ca, "client.crt": other[0], "client.key": other[1]}})
	tcp, proxyPort := corev1.ProtocolTCP, intstr.FromInt32(8124)
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: ns}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"role": "target"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "worker"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &proxyPort}}}}}}
	create(policy)
	no := false
	target := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: ns, Labels: map[string]string{"role": "target"}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, Containers: []corev1.Container{
		{Name: "ha", Image: "ghcr.io/home-assistant/home-assistant:2026.11.0", VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/config"}}},
		{Name: "proxy", Image: image, Command: []string{"/migrate-worker"}, Args: []string{"--mode", "onboarding-proxy"}, VolumeMounts: []corev1.VolumeMount{{Name: "server", MountPath: "/var/run/migration-proxy", ReadOnly: true}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: proxyPort}}, PeriodSeconds: 1}},
	}, Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "server", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "server"}}}}}}
	create(target)
	backupEventually(t, ctx, func() bool {
		if kube.Get(ctx, client.ObjectKeyFromObject(target), target) != nil {
			return false
		}
		for _, condition := range target.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, "proxy target ready")
	for _, tc := range []struct{ name, secret, expected string }{{"trusted", "worker", "200"}, {"foreign-cert", "other", foreignExpected}, {"spoof-no-cert", "", "denied"}, {"direct-ha", "", "blocked"}} {
		script := proxyProbeScript(target.Status.PodIP, tc.expected, tc.secret != "")
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: tc.name, Namespace: ns, Labels: map[string]string{"role": "worker"}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "probe", Image: "ghcr.io/home-assistant/home-assistant:2026.11.0", Command: []string{"python", "-c", script}}}}}
		if tc.secret != "" {
			pod.Spec.Volumes = []corev1.Volume{{Name: "credential", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: tc.secret}}}}
			pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "credential", MountPath: "/credential", ReadOnly: true}}
		}
		create(pod)
		backupEventually(t, ctx, func() bool {
			if kube.Get(ctx, client.ObjectKeyFromObject(pod), pod) != nil {
				return false
			}
			if pod.Status.Phase == corev1.PodFailed {
				t.Fatalf("%s proxy probe failed", tc.name)
			}
			return pod.Status.Phase == corev1.PodSucceeded
		}, "proxy probe "+tc.name)
	}
	endpoint, stop := backupPortForward(t, ctx, ns, target.Name, 8124)
	defer stop()
	keyPair, err := tls.X509KeyPair(worker[0], worker[1])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("test CA invalid")
	}
	serverBlock, _ := pem.Decode(server[0])
	if serverBlock == nil {
		t.Fatal("test server certificate invalid")
	}
	serverCert, err := x509.ParseCertificate(serverBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	api, err := haclient.NewMigrationOnboardingClient("https://"+strings.TrimPrefix(endpoint, "http://"), roots, serverCert, keyPair, "migration-proxy.test")
	if err != nil {
		t.Fatal(err)
	}
	backupEventually(t, ctx, func() bool { return api.CheckOnboardingStatus(ctx) == nil }, "pinned Go client onboarding")
	otherBlock, _ := pem.Decode(other[0])
	if otherBlock == nil {
		t.Fatal("test alternate certificate invalid")
	}
	otherCert, err := x509.ParseCertificate(otherBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	wrongPin, err := haclient.NewMigrationOnboardingClient("https://"+strings.TrimPrefix(endpoint, "http://"), roots, otherCert, keyPair, "migration-proxy.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := wrongPin.CheckOnboardingStatus(ctx); err == nil {
		t.Fatal("foreign pinned server identity accepted")
	}
}

func proxyProbeScript(ip, expected string, hasCredential bool) string {
	return fmt.Sprintf(`import socket,ssl,sys,time
ip=%q; expected=%q; credential=%s
if expected=='denied':
 for i in range(20):
  try:
   s=socket.create_connection((ip,8124),timeout=2); s.close(); break
  except OSError: time.sleep(1)
 else: sys.exit(1)
for i in range(60 if expected=='200' else 5):
 try:
  if expected=='blocked':
   s=socket.create_connection((ip,8123),timeout=2); s.close(); sys.exit(1)
  c=ssl.create_default_context(cafile='/credential/ca.crt' if credential else None)
  if credential: c.load_cert_chain('/credential/client.crt','/credential/client.key')
  else: c.check_hostname=False; c.verify_mode=ssl.CERT_NONE
  with socket.create_connection((ip,8124),timeout=3) as raw:
   with c.wrap_socket(raw,server_hostname='migration-proxy.test') as s:
    s.sendall(b'GET /api/onboarding HTTP/1.1\r\nHost: migration-proxy.test\r\nConnection: close\r\n\r\n'); result=s.recv(256)
  if expected=='denied': sys.exit(1)
  if (' '+expected+' ').encode() in result: sys.exit(0)
 except (OSError,ssl.SSLError):
  if expected=='denied': sys.exit(0)
 time.sleep(1)
sys.exit(0 if expected=='blocked' else 1)
`, ip, expected, map[bool]string{true: "True", false: "False"}[hasCredential])
}

func proxyTestCredentials(t *testing.T) ([]byte, [2][]byte, [2][]byte, [2][]byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "disposable migration CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(serial int64, usage x509.ExtKeyUsage) [2][]byte {
		keyPair, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{"migration-proxy.test"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		encoded, err := x509.CreateCertificate(rand.Reader, cert, ca, &keyPair.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		private, err := x509.MarshalPKCS8PrivateKey(keyPair)
		if err != nil {
			t.Fatal(err)
		}
		return [2][]byte{pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})}
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), issue(2, x509.ExtKeyUsageServerAuth), issue(3, x509.ExtKeyUsageClientAuth), issue(4, x509.ExtKeyUsageClientAuth)
}
