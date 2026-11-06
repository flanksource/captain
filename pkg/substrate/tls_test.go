// Specs for reaching a substrate behind a client-certificate ingress: both APIs over mutual TLS,
// the control plane's token on each call, and a certificate that comes without its key.
package substrate

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
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	ateapipb "github.com/flanksource/captain/pkg/substrate/internal/ateapi"
	"github.com/flanksource/captain/pkg/substrate/internal/ateenvv1alpha"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestSubstrateClientCertificateReachesBothAPIs(t *testing.T) {
	dir := t.TempDir()
	ca := newTestCA(t)
	ca.write(t, filepath.Join(dir, "ca.crt"))
	serverCert := ca.issue(t, "substrate", x509.ExtKeyUsageServerAuth)
	ca.issue(t, "captain", x509.ExtKeyUsageClientAuth).write(t, filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "token"), []byte("substrate-token\n"), 0o600))

	calls := &seenCalls{}
	addr := serveMutualTLS(t, ca, serverCert, calls)

	substrate, err := Dial(Options{
		EnvAPI: addr, Control: addr,
		ControlTokenFile: filepath.Join(dir, "token"), CAFile: filepath.Join(dir, "ca.crt"),
		ClientCertFile: filepath.Join(dir, "tls.crt"), ClientKeyFile: filepath.Join(dir, "tls.key"),
	})
	require.NoError(t, err)
	defer substrate.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = substrate.envs.GetEnvironment(ctx, &ateenvv1alpha.GetEnvironmentRequest{Id: "job"})
	assert.Equal(t, codes.Unimplemented, status.Code(err), "ate-env-api over mutual TLS: %v", err)
	_, err = substrate.control.CreateActorEgressPolicy(ctx, &ateapipb.CreateActorEgressPolicyRequest{})
	assert.Equal(t, codes.Unimplemented, status.Code(err), "ate-api-server over mutual TLS: %v", err)

	assert.Equal(t, []seenCall{
		{Method: "/ateenv.v1alpha.EnvironmentService/GetEnvironment", Client: "captain"},
		{Method: "/substrate.ateapi.v1.Control/CreateActorEgressPolicy", Client: "captain", Authorization: "Bearer substrate-token"},
	}, calls.all())
}

func TestSubstrateClientCertificateNeedsItsKey(t *testing.T) {
	dir := t.TempDir()
	newTestCA(t).issue(t, "captain", x509.ExtKeyUsageClientAuth).write(t, filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"))

	_, err := Dial(Options{EnvAPI: "env:443", Control: "api:443", ClientCertFile: filepath.Join(dir, "tls.crt")})
	assert.EqualError(t, err, "the substrate client certificate needs both its certificate and key files")

	_, err = Dial(Options{
		EnvAPI: "env:443", Control: "api:443",
		ClientCertFile: filepath.Join(dir, "tls.crt"), ClientKeyFile: filepath.Join(dir, "missing.key"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load the substrate client certificate "+filepath.Join(dir, "tls.crt"))
}

type seenCall struct{ Method, Client, Authorization string }

type seenCalls struct {
	mu    sync.Mutex
	calls []seenCall
}

func (s *seenCalls) all() []seenCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seenCall(nil), s.calls...)
}

// record notes each call's method, the client certificate it came with and its authorization
// header, then lets the unimplemented service answer it.
func (s *seenCalls) record(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	call := seenCall{Method: info.FullMethod}
	if p, ok := peer.FromContext(ctx); ok {
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok && len(tlsInfo.State.PeerCertificates) > 0 {
			call.Client = tlsInfo.State.PeerCertificates[0].Subject.CommonName
		}
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok && len(md.Get("authorization")) > 0 {
		call.Authorization = md.Get("authorization")[0]
	}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	s.mu.Unlock()
	return handler(ctx, req)
}

// serveMutualTLS serves both substrate APIs on localhost, refusing clients without a certificate
// from ca.
func serveMutualTLS(t *testing.T, ca *testCA, cert *testCert, calls *seenCalls) string {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{cert.tls()}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12,
		})),
		grpc.UnaryInterceptor(calls.record),
	)
	ateenvv1alpha.RegisterEnvironmentServiceServer(server, ateenvv1alpha.UnimplementedEnvironmentServiceServer{})
	ateapipb.RegisterControlServer(server, ateapipb.UnimplementedControlServer{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String()
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

type testCert struct {
	der []byte
	key *ecdsa.PrivateKey
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "substrate clients"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCA{cert: cert, key: key}
}

func (ca *testCA) write(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw}), 0o600))
}

// issue signs a certificate for name, valid for 127.0.0.1 so it can also serve.
func (ca *testCA) issue(t *testing.T, name string, usage x509.ExtKeyUsage) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	return &testCert{der: der, key: key}
}

func (c *testCert) tls() tls.Certificate {
	return tls.Certificate{Certificate: [][]byte{c.der}, PrivateKey: c.key}
}

func (c *testCert) write(t *testing.T, certPath, keyPath string) {
	t.Helper()
	keyDER, err := x509.MarshalECPrivateKey(c.key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
}
