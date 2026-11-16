package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/przemekhys/homeassistant-operator/internal/migration"
)

// The fixed mount and loopback upstream avoid passing credentials in arguments
// or permitting a configuration-controlled proxy to arbitrary destinations.
func runOnboardingProxy(ctx context.Context) error {
	const dir = "/var/run/migration-proxy/"
	server, err := tls.LoadX509KeyPair(dir+"server.crt", dir+"server.key")
	if err != nil {
		return fmt.Errorf("proxy server credential unavailable")
	}
	caBytes, err := os.ReadFile(dir + "client-ca.crt")
	if err != nil {
		return fmt.Errorf("proxy client CA unavailable")
	}
	ca := x509.NewCertPool()
	if !ca.AppendCertsFromPEM(caBytes) {
		return fmt.Errorf("proxy client CA invalid")
	}
	pinnedBytes, err := os.ReadFile(dir + "client.crt")
	if err != nil {
		return fmt.Errorf("proxy client identity unavailable")
	}
	block, rest := pem.Decode(pinnedBytes)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		return fmt.Errorf("proxy client identity invalid")
	}
	pinned, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("proxy client identity invalid")
	}
	handler, err := migration.NewOnboardingProxy("http://127.0.0.1:8123", pinned)
	if err != nil {
		return err
	}
	tlsConfig, err := migration.OnboardingProxyTLSConfig(server, ca)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: ":8124", Handler: handler, TLSConfig: tlsConfig, ReadHeaderTimeout: 10 * time.Second}
	result := make(chan error, 1)
	go func() { result <- srv.ListenAndServeTLS("", "") }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("proxy shutdown incomplete")
		}
		return nil
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("proxy listener unavailable")
	}
}
