// TLS for the HTTPS transport (§8). The certificate lives beside the SSH host
// key and plays the same role: it is the thing an agent pins so that reaching
// the supervisor proves it is the supervisor, not merely something at that
// address.

package gitagent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

const (
	tlsCertName = "tls_cert.pem"
	tlsKeyName  = "tls_key.pem"
	// tlsValidity is deliberately long. The certificate is pinned by the agents
	// that hold it rather than chained to a CA, so replacing it costs a
	// re-enrollment of every agent — an expiry would arrive as an unexplained
	// outage months after anyone touched this.
	tlsValidity = 10 * 365 * 24 * time.Hour
)

// TLSCredential is the endpoint's certificate plus what a client needs in order
// to trust it.
type TLSCredential struct {
	Certificate tls.Certificate
	Leaf        *x509.Certificate
	// CertPath is the PEM a client passes as http.sslCAInfo. The certificate is
	// its own trust anchor, so this file is both the leaf and the CA.
	CertPath string
	KeyPath  string
	// PublicKeyPin is the sha256//<base64> form git and curl accept for
	// http.pinnedPubkey. Pinning is optional for a client — sslCAInfo already
	// fixes trust to this exact certificate — but it survives a re-issue under
	// the same key, which sslCAInfo does not.
	PublicKeyPin string
}

// EnsureTLSCredential loads the endpoint's certificate from dir, generating a
// self-signed one on first use that covers hosts as well as every address this
// machine can plausibly be reached on.
//
// It never silently re-issues. An agent is handed this exact certificate when
// its token is minted, so rotating it invalidates every enrolled agent at once
// — and the failure surfaces hours later as an unexplained push rejection
// rather than at the moment of the change. A certificate that does not cover a
// requested address is therefore an error naming the fix.
func EnsureTLSCredential(dir string, hosts []string) (*TLSCredential, error) {
	certPath, keyPath := filepath.Join(dir, tlsCertName), filepath.Join(dir, tlsKeyName)
	credential, err := LoadTLSCredential(certPath, keyPath)
	if err == nil {
		return credential, credential.Covers(hosts)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	err = withFileLock(certPath+".lock", 0o600, func() error {
		// Another captain may have generated it while this one waited.
		existing, loadErr := LoadTLSCredential(certPath, keyPath)
		if loadErr == nil {
			credential = existing
			return credential.Covers(hosts)
		}
		if !os.IsNotExist(loadErr) {
			return loadErr
		}
		credential, loadErr = generateTLSCredential(certPath, keyPath, hosts)
		return loadErr
	})
	if err != nil {
		return nil, err
	}
	return credential, nil
}

// LoadTLSCredential reads a certificate and key, which may be a real one an
// operator supplied rather than a generated self-signed pair.
func LoadTLSCredential(certPath, keyPath string) (*TLSCredential, error) {
	// Statted first so a missing pair is os.ErrNotExist, which EnsureTLSCredential
	// distinguishes from a broken one — LoadX509KeyPair wraps both alike.
	for _, path := range []string{certPath, keyPath} {
		if _, err := os.Stat(path); err != nil {
			return nil, err
		}
	}
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate %s: %w", certPath, err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse TLS certificate %s: %w", certPath, err)
	}
	certificate.Leaf = leaf
	pin, err := publicKeyPin(leaf)
	if err != nil {
		return nil, err
	}
	return &TLSCredential{
		Certificate: certificate, Leaf: leaf,
		CertPath: certPath, KeyPath: keyPath, PublicKeyPin: pin,
	}, nil
}

// Covers reports whether the certificate is valid for every host an agent will
// dial, naming the first one it is not.
func (c *TLSCredential) Covers(hosts []string) error {
	for _, host := range hosts {
		if host == "" {
			continue
		}
		if err := c.Leaf.VerifyHostname(host); err != nil {
			return fmt.Errorf(
				"the TLS certificate at %s does not cover %q (it covers %s); "+
					"delete it and its key to re-issue, or supply your own certificate — "+
					"note that re-issuing means every enrolled agent must be re-enrolled",
				c.CertPath, host, certificateCoverage(c.Leaf))
		}
	}
	return nil
}

// certificateCoverage lists the names a certificate answers to, so a refusal
// says what is covered rather than only what is not.
func certificateCoverage(cert *x509.Certificate) string {
	names := slices.Clone(cert.DNSNames)
	for _, ip := range cert.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) == 0 {
		return "nothing"
	}
	sort.Strings(names)
	return fmt.Sprint(names)
}

// PEM returns the certificate in the form a client stores as its trust anchor.
func (c *TLSCredential) PEM() ([]byte, error) {
	return os.ReadFile(c.CertPath)
}

func generateTLSCredential(certPath, keyPath string, hosts []string) (*TLSCredential, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate TLS key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate TLS serial: %w", err)
	}
	dnsNames, ips := tlsSubjectNames(hosts)
	now := time.Now().UTC()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "captain git-agent", Organization: []string{"captain"}},
		// Backdated an hour so a client whose clock runs slightly behind does
		// not reject a certificate generated moments ago.
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.Add(tlsValidity),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// Its own trust anchor: a client passes this file as sslCAInfo, and a
		// chain of one only verifies if the leaf is also a CA.
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create TLS certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode TLS key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	// The key first: a certificate on disk without its key would make
	// LoadTLSCredential fail as broken rather than absent, and the retry path
	// would never regenerate it.
	if err := writeFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(certPath, certPEM, 0o644); err != nil {
		return nil, err
	}
	return LoadTLSCredential(certPath, keyPath)
}

// tlsSubjectNames is everything a freshly generated certificate covers: the
// addresses the caller named, plus every address this host answers on. The
// generous default exists because the alternative — discovering at first push
// that the certificate omits the address agents actually dial — costs a
// re-enrollment of all of them to fix.
func tlsSubjectNames(extra []string) (dnsNames []string, ips []net.IP) {
	// host.docker.internal is what a docker sidecar dials to reach the host, and
	// it resolves only inside a container — so it is never this machine's own
	// name and has to be named here rather than discovered from an interface.
	names := map[string]struct{}{"localhost": {}, "host.docker.internal": {}}
	addresses := map[string]net.IP{}
	add := func(value string) {
		if value = trimBrackets(value); value == "" {
			return
		}
		if ip := net.ParseIP(value); ip != nil {
			addresses[ip.String()] = ip
			return
		}
		names[value] = struct{}{}
	}
	add("127.0.0.1")
	add("::1")
	if hostname, err := os.Hostname(); err == nil {
		add(hostname)
	}
	if interfaceAddrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range interfaceAddrs {
			if network, ok := addr.(*net.IPNet); ok {
				add(network.IP.String())
			}
		}
	}
	for _, host := range extra {
		add(host)
	}
	for name := range names {
		dnsNames = append(dnsNames, name)
	}
	for _, ip := range addresses {
		ips = append(ips, ip)
	}
	sort.Strings(dnsNames)
	sort.Slice(ips, func(i, j int) bool { return ips[i].String() < ips[j].String() })
	return dnsNames, ips
}

func trimBrackets(value string) string {
	if len(value) > 1 && value[0] == '[' && value[len(value)-1] == ']' {
		return value[1 : len(value)-1]
	}
	return value
}

// publicKeyPin renders the SubjectPublicKeyInfo digest in the sha256//<base64>
// form git and curl accept.
func publicKeyPin(cert *x509.Certificate) (string, error) {
	spki, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return "", fmt.Errorf("encode TLS public key: %w", err)
	}
	sum := sha256.Sum256(spki)
	return "sha256//" + base64.StdEncoding.EncodeToString(sum[:]), nil
}
