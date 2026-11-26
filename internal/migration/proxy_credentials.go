package migration

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// ProxyServerName is deliberately independent of a mutable Kubernetes IP.
const ProxyServerName = "migration-proxy.test"

// ProxyCredentials is sensitive. Never serialize it to status, ConfigMaps,
// events or logs. No CA private key is retained after generation.
type ProxyCredentials struct {
	CA, ServerCertificate, ServerKey, WorkerCertificate, WorkerKey []byte
}

// GenerateProxyCredentials only generates in memory; it does not establish
// durable identity or authorize use of the credentials.
func GenerateProxyCredentials(now time.Time, lifetime time.Duration) (*ProxyCredentials, error) {
	if now.IsZero() || lifetime < 5*time.Minute || lifetime > 24*time.Hour {
		return nil, fmt.Errorf("proxy certificate lifetime invalid")
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("proxy certificate generation unavailable")
	}
	serial, err := randomProxySerial()
	if err != nil {
		return nil, err
	}
	caTemplate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "migration proxy CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(lifetime), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("proxy certificate generation unavailable")
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("proxy certificate generation unavailable")
	}
	server, serverKey, err := issueProxyCertificate(now, lifetime, ca, caKey, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return nil, err
	}
	worker, workerKey, err := issueProxyCertificate(now, lifetime, ca, caKey, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil, err
	}
	return &ProxyCredentials{CA: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), ServerCertificate: server, ServerKey: serverKey, WorkerCertificate: worker, WorkerKey: workerKey}, nil
}

func randomProxySerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil || serial.Sign() == 0 {
		return nil, fmt.Errorf("proxy certificate generation unavailable")
	}
	return serial, nil
}

func issueProxyCertificate(now time.Time, lifetime time.Duration, ca *x509.Certificate, caKey *ecdsa.PrivateKey, usage x509.ExtKeyUsage) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("proxy certificate generation unavailable")
	}
	serial, err := randomProxySerial()
	if err != nil {
		return nil, nil, err
	}
	cert := &x509.Certificate{SerialNumber: serial, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(lifetime), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
	if usage == x509.ExtKeyUsageServerAuth {
		cert.DNSNames = []string{ProxyServerName}
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("proxy certificate generation unavailable")
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("proxy certificate generation unavailable")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), nil
}

// ValidateProxyCredentials checks both key pairs, usages, CA and validity at a
// specified time. It does not prove ownership, provenance, or Secret binding.
func ValidateProxyCredentials(c *ProxyCredentials, now time.Time) error {
	if c == nil || now.IsZero() {
		return fmt.Errorf("proxy credentials invalid")
	}
	server, err := tls.X509KeyPair(c.ServerCertificate, c.ServerKey)
	if err != nil {
		return fmt.Errorf("proxy credentials invalid")
	}
	worker, err := tls.X509KeyPair(c.WorkerCertificate, c.WorkerKey)
	if err != nil {
		return fmt.Errorf("proxy credentials invalid")
	}
	caBlock, rest := pem.Decode(c.CA)
	if caBlock == nil || caBlock.Type != "CERTIFICATE" || len(rest) != 0 {
		return fmt.Errorf("proxy credentials invalid")
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil || !ca.IsCA {
		return fmt.Errorf("proxy credentials invalid")
	}
	serverLeaf, err := x509.ParseCertificate(server.Certificate[0])
	if err != nil || serverLeaf.IsCA {
		return fmt.Errorf("proxy credentials invalid")
	}
	workerLeaf, err := x509.ParseCertificate(worker.Certificate[0])
	if err != nil || workerLeaf.IsCA || serverLeaf.Equal(workerLeaf) {
		return fmt.Errorf("proxy credentials invalid")
	}
	serverPublic, err := x509.MarshalPKIXPublicKey(serverLeaf.PublicKey)
	if err != nil {
		return fmt.Errorf("proxy credentials invalid")
	}
	workerPublic, err := x509.MarshalPKIXPublicKey(workerLeaf.PublicKey)
	if err != nil || bytes.Equal(serverPublic, workerPublic) {
		return fmt.Errorf("proxy credentials invalid")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := serverLeaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, DNSName: ProxyServerName, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return fmt.Errorf("proxy credentials invalid")
	}
	if _, err := workerLeaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("proxy credentials invalid")
	}
	return nil
}
