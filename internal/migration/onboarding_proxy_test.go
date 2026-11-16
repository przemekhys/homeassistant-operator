package migration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

func TestOnboardingProxyRequiresVerifiedPinnedClient(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caBytes, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caBytes)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(serial int64, usage x509.ExtKeyUsage) (tls.Certificate, *x509.Certificate) {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{ProxyServerName}}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, cert
	}
	serverCert, _ := issue(2, x509.ExtKeyUsageServerAuth)
	worker, workerCert := issue(3, x509.ExtKeyUsageClientAuth)
	other, _ := issue(4, x509.ExtKeyUsageClientAuth)
	sharedKey := serverCert.PrivateKey.(*ecdsa.PrivateKey)
	sharedTemplate := &x509.Certificate{SerialNumber: big.NewInt(5), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	sharedDER, err := x509.CreateCertificate(rand.Reader, sharedTemplate, ca, &sharedKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	sharedPrivate, err := x509.MarshalPKCS8PrivateKey(sharedKey)
	if err != nil {
		t.Fatal(err)
	}
	sharedPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: sharedPrivate})
	shared := &ProxyCredentials{CA: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caBytes}), ServerCertificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCert.Certificate[0]}), ServerKey: sharedPEM, WorkerCertificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: sharedDER}), WorkerKey: sharedPEM}
	if err := ValidateProxyCredentials(shared, now); err == nil {
		t.Fatal("shared server and worker key accepted")
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	var upstreamCalls atomic.Int32
	var redirect atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.Host == "attacker.invalid" {
			t.Error("untrusted host reached HA")
		}
		if r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Forwarded-Host") != "" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("untrusted forwarded headers reached HA")
		}
		if redirect.Load() {
			http.Redirect(w, r, "/api/onboarding/backup/info", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	handler, err := NewOnboardingProxy(upstream.URL, workerCert)
	if err != nil {
		t.Fatal(err)
	}
	config, err := OnboardingProxyTLSConfig(serverCert, pool)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewUnstartedServer(handler)
	proxy.TLS = config
	proxy.StartTLS()
	defer proxy.Close()
	for _, tc := range []struct {
		name   string
		cert   *tls.Certificate
		status int
		fail   bool
	}{
		{"pinned", &worker, http.StatusNoContent, false}, {"other CA-signed", &other, http.StatusForbidden, false}, {"no certificate", nil, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tlsConfig := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
			if tc.cert != nil {
				tlsConfig.Certificates = []tls.Certificate{*tc.cert}
			}
			c := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}, Timeout: 5 * time.Second}
			req, err := http.NewRequest(http.MethodGet, proxy.URL+"/api/onboarding", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Forwarded", "host=attacker.invalid")
			req.Header.Set("X-Forwarded-Host", "attacker.invalid")
			req.Host = "attacker.invalid"
			req.Header.Set("Idempotency-Key", "do-not-retry")
			response, err := c.Do(req)
			if tc.fail {
				if err == nil {
					response.Body.Close()
					t.Fatal("missing client certificate accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", response.StatusCode, tc.status)
			}
		})
	}
	trusted := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{worker}, MinVersion: tls.VersionTLS12}}, Timeout: 5 * time.Second}
	for _, path := range []string{"/api/config", "/api/onboarding/backup/restore", "/api/onboarding%2fbackup/info"} {
		resp, err := trusted.Get(proxy.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("unexpected route %s: %d", path, resp.StatusCode)
		}
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream received %d requests", upstreamCalls.Load())
	}
	serverLeaf, err := x509.ParseCertificate(serverCert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	migrationClient, err := haclient.NewMigrationOnboardingClient(proxy.URL, pool, serverLeaf, worker, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrationClient.MigrationOnboardingBackupInfo(context.Background()); err != nil {
		t.Fatal("pinned mTLS client rejected", err)
	}
	wrongClient, err := haclient.NewMigrationOnboardingClient(proxy.URL, pool, workerCert, worker, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := wrongClient.MigrationOnboardingBackupInfo(context.Background()); err == nil {
		t.Fatal("foreign server certificate accepted")
	}
	redirect.Store(true)
	before := upstreamCalls.Load()
	if err := migrationClient.MigrationOnboardingBackupInfo(context.Background()); err == nil || upstreamCalls.Load() != before+1 {
		t.Fatal("onboarding redirect was followed")
	}
	redirect.Store(false)
	if _, err := NewOnboardingProxy("http://example.com:8123", workerCert); err == nil {
		t.Fatal("non-loopback upstream accepted")
	}
	if _, err := OnboardingProxyTLSConfig(serverCert, nil); err == nil {
		t.Fatal("missing client CA accepted")
	}
	var attempted atomic.Int32
	lost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempted.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer lost.Close()
	lostHandler, err := NewOnboardingProxy(lost.URL, workerCert)
	if err != nil {
		t.Fatal(err)
	}
	lostProxy := httptest.NewUnstartedServer(lostHandler)
	lostProxy.TLS = config.Clone()
	lostProxy.StartTLS()
	defer lostProxy.Close()
	req, err := http.NewRequest(http.MethodPost, lostProxy.URL+"/api/onboarding/backup/restore", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "must-not-enable-retry")
	response, err := trusted.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || attempted.Load() != 1 {
		t.Fatalf("ambiguous restore was retried: status=%d calls=%d", response.StatusCode, attempted.Load())
	}
}
