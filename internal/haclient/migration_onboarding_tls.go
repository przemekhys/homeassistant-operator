package haclient

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NewMigrationOnboardingClient creates a dedicated, non-retrying mTLS client.
// It pins the exact server certificate in addition to normal CA/hostname checks.
// A successful response is not evidence that upload or restore succeeded.
func NewMigrationOnboardingClient(endpoint string, roots *x509.CertPool, serverCert *x509.Certificate, worker tls.Certificate, serverName string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || roots == nil || serverCert == nil || len(serverCert.Raw) == 0 || len(worker.Certificate) == 0 || worker.PrivateKey == nil || strings.TrimSpace(serverName) == "" {
		return nil, fmt.Errorf("migration TLS configuration invalid")
	}
	expected := sha256.Sum256(serverCert.Raw)
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: serverName, Certificates: []tls.Certificate{worker}, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
			return fmt.Errorf("migration server verification unavailable")
		}
		actual := sha256.Sum256(state.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			return fmt.Errorf("migration server identity changed")
		}
		return nil
	}}
	return &Client{baseURL: u.String(), httpClient: &http.Client{
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: tlsConfig, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}
