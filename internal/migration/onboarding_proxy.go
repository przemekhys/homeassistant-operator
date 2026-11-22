package migration

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// NewOnboardingProxy serves only clients authenticated by the caller's TLS
// listener and pinned to one certificate. It is not a substitute for isolation
// of HA's direct port, admission controls, or lifecycle management.
func NewOnboardingProxy(upstream string, clientCertificate *x509.Certificate) (http.Handler, error) {
	endpoint, err := url.Parse(upstream)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || clientCertificate == nil || len(clientCertificate.Raw) == 0 {
		return nil, fmt.Errorf("invalid onboarding proxy configuration")
	}
	host, port, err := net.SplitHostPort(endpoint.Host)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || port == "" {
		return nil, fmt.Errorf("onboarding upstream must use loopback")
	}
	expected := sha256.Sum256(clientCertificate.Raw)
	proxy := httputil.NewSingleHostReverseProxy(endpoint)
	// Fresh connections and removal of idempotency headers prevent the default
	// HTTP transport from treating HA mutations as replayable after a failure.
	proxy.Transport = &http.Transport{Proxy: nil, DisableKeepAlives: true}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "onboarding upstream unavailable", http.StatusBadGateway)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "client authentication required", http.StatusForbidden)
			return
		}
		actual := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			http.Error(w, "client authentication required", http.StatusForbidden)
			return
		}
		route, allowed := onboardingProxyRoute(r.Method, r.URL.Path)
		if r.URL.RawPath != "" || !allowed {
			http.Error(w, "onboarding route unavailable", http.StatusNotFound)
			return
		}
		// The route name is selected exclusively from constants rather than copied
		// from a request. Headers and bodies are never logged. This lets an
		// operator prove how many mutating POSTs reached the target without
		// relying on HA readiness or a 2xx receipt.
		log.Printf("onboarding proxy request: %s", route)
		// Forwarded headers are not identity evidence and must not be supplied by
		// an untrusted client to the HA upstream.
		r.Header.Del("Forwarded")
		// ReverseProxy otherwise adds X-Forwarded-For after this handler;
		// fresh HA rejects it unless trusted proxies have been configured.
		r.Header["X-Forwarded-For"] = nil
		r.Header.Del("X-Forwarded-Host")
		r.Header.Del("X-Forwarded-Proto")
		r.Header.Del("Idempotency-Key")
		r.Header.Del("X-Idempotency-Key")
		r.Host = endpoint.Host
		proxy.ServeHTTP(w, r)
	}), nil
}

func onboardingProxyRoute(method, path string) (string, bool) {
	switch {
	case method == http.MethodGet && path == "/api/onboarding":
		return "onboarding status", true
	case method == http.MethodGet && path == "/api/onboarding/backup/info":
		return "backup information", true
	case method == http.MethodPost && path == "/api/onboarding/backup/upload":
		return "backup upload", true
	case method == http.MethodPost && path == "/api/onboarding/backup/restore":
		return "backup restore", true
	default:
		return "", false
	}
}

// OnboardingProxyTLSConfig requires CA verification before the handler pins
// the exact worker certificate. Callers own certificate rotation and lifetime.
func OnboardingProxyTLSConfig(server tls.Certificate, clientCA *x509.CertPool) (*tls.Config, error) {
	if len(server.Certificate) == 0 || server.PrivateKey == nil || clientCA == nil {
		return nil, fmt.Errorf("onboarding TLS credentials unavailable")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCA}, nil
}
