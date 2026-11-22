package migration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

func TestProxyCredentialsGenerationAndValidation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	a, err := GenerateProxyCredentials(now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateProxyCredentials(a, now); err != nil {
		t.Fatal(err)
	}
	b, err := GenerateProxyCredentials(now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.ServerCertificate) == string(b.ServerCertificate) || string(a.WorkerCertificate) == string(b.WorkerCertificate) {
		t.Fatal("distinct credentials reused")
	}
	for _, mutate := range []func(*ProxyCredentials){
		func(c *ProxyCredentials) { c.ServerKey = b.ServerKey },
		func(c *ProxyCredentials) { c.WorkerKey = b.WorkerKey },
		func(c *ProxyCredentials) { c.CA = b.CA },
		func(c *ProxyCredentials) { c.WorkerCertificate = c.ServerCertificate; c.WorkerKey = c.ServerKey },
		func(c *ProxyCredentials) { c.CA = append(append([]byte(nil), c.CA...), []byte("foreign")...) },
	} {
		copy := *a
		mutate(&copy)
		if err := ValidateProxyCredentials(&copy, now); err == nil || strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Fatal("invalid credentials accepted or leaked")
		}
	}
	if err := ValidateProxyCredentials(a, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired credentials accepted")
	}
	for _, lifetime := range []time.Duration{0, 4 * time.Minute, 25 * time.Hour} {
		if _, err := GenerateProxyCredentials(now, lifetime); err == nil {
			t.Fatal("invalid lifetime accepted")
		}
	}
}

func TestGeneratedProxyCredentialsAuthenticateBothSides(t *testing.T) {
	now := time.Now()
	credentials, err := GenerateProxyCredentials(now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	server, err := tls.X509KeyPair(credentials.ServerCertificate, credentials.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := tls.X509KeyPair(credentials.WorkerCertificate, credentials.WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	workerLeaf, err := x509.ParseCertificate(worker.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	serverLeaf, err := x509.ParseCertificate(server.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	caBlock, _ := pem.Decode(credentials.CA)
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"backups":[{"backup_id":"id","database_included":true,"homeassistant_included":true,"homeassistant_version":"2026.11.0","folders":[],"agents":{"agent":{"protected":true,"size":1}}}],"state":"idle","last_action_event":null}`))
	}))
	defer upstream.Close()
	handler, err := NewOnboardingProxy(upstream.URL, workerLeaf)
	if err != nil {
		t.Fatal(err)
	}
	config, err := OnboardingProxyTLSConfig(server, roots)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewUnstartedServer(handler)
	proxy.TLS = config
	proxy.StartTLS()
	defer proxy.Close()
	api, err := haclient.NewMigrationOnboardingClient(proxy.URL, roots, serverLeaf, worker, ProxyServerName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.MigrationOnboardingBackupInfo(context.Background(), haclient.MigrationBackupExpectation{BackupID: "id", AgentID: "agent", Size: 1, RestoreDatabase: true}); err != nil {
		t.Fatal(err)
	}
}
