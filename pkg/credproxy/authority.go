package credproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"
)

// authorityLifetime bounds a run CA. It outlives any run that uses it, and its
// key never leaves the process, so nothing is gained from rotating it sooner.
const authorityLifetime = 24 * time.Hour

// Authority is a per-proxy certificate authority. It lets the proxy terminate a
// CONNECT tunnel to a granted HTTPS destination, so the requests inside stay
// inspectable. Only its certificate is ever handed out — to the clients that
// must trust the tunnel; the private key stays in memory.
type Authority struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

func NewAuthority() (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("credproxy: generate CA key: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serialNumber(),
		Subject:               pkix.Name{CommonName: "captain credential proxy"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(authorityLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("credproxy: create CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("credproxy: parse CA certificate: %w", err)
	}
	return &Authority{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		leaves:  map[string]*tls.Certificate{},
	}, nil
}

// CertPEM is the CA certificate a client trusts to accept the proxy's tunnels.
func (a *Authority) CertPEM() []byte { return a.certPEM }

// leaf returns the server certificate the proxy presents for host, minted once
// per host and signed by the authority.
func (a *Authority) leaf(host string) (*tls.Certificate, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cached, ok := a.leaves[host]; ok {
		return cached, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("credproxy: generate key for %s: %w", host, err)
	}
	template := &x509.Certificate{
		SerialNumber: serialNumber(),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    a.cert.NotBefore,
		NotAfter:     a.cert.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, fmt.Errorf("credproxy: sign certificate for %s: %w", host, err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("credproxy: parse certificate for %s: %w", host, err)
	}
	leaf := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}
	a.leaves[host] = leaf
	return leaf, nil
}

func serialNumber() *big.Int {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(fmt.Sprintf("credproxy: crypto/rand failed: %v", err))
	}
	return serial
}
