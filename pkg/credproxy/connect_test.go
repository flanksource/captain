package credproxy

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
)

// tunnelWorld is a world whose proxy terminates CONNECT with a run CA, and a
// client that trusts only that CA — the configuration an inline tool gets.
func tunnelWorld(t *testing.T) (*world, *http.Client) {
	t.Helper()
	w := newWorld(t, secret)
	authority, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	w.handler.Authority = authority
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(authority.CertPEM()) {
		t.Fatal("authority PEM did not parse")
	}
	proxyURL, err := url.Parse(w.proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}}
	t.Cleanup(transport.CloseIdleConnections)
	return w, &http.Client{Transport: transport}
}

func TestConnectTerminatesAGrantedHTTPSDestination(t *testing.T) {
	w, client := tunnelWorld(t)
	req, _ := http.NewRequest(http.MethodGet, w.upstream.URL+"/repos/acme/captain", nil)
	req.Header.Set("Authorization", w.grant.Placeholder("Authorization"))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK || string(body) != "upstream says hi" {
		t.Fatalf("status = %d body = %q", resp.StatusCode, body)
	}
	if got := w.upstreamHeaderValues("Authorization"); len(got) != 1 || got[0] != secret {
		t.Fatalf("upstream Authorization = %v, want the substituted secret", got)
	}
}

func TestTunnelledRequestsRunTheGrantPipeline(t *testing.T) {
	w, client := tunnelWorld(t)
	for name, target := range map[string]string{
		"outside the granted paths": w.upstream.URL + "/repos/other/captain",
		"placeholder in the URL":    w.upstream.URL + "/repos/acme/x?t=" + w.grant.Placeholder("Authorization"),
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := client.Get(target)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
	if n := w.upstreamRequestCount(); n != 0 {
		t.Fatalf("upstream saw %d requests, want none", n)
	}
}

func TestConnectToAnUngrantedDestinationIsRefused(t *testing.T) {
	w, _ := tunnelWorld(t)
	req, _ := http.NewRequest(http.MethodConnect, w.proxy.URL, nil)
	req.URL.Opaque = "example.com:443"
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT: status = %d, want 403", resp.StatusCode)
	}
}

// A request inside the tunnel names the destination twice: the CONNECT
// authority and its own Host. Honouring a different Host would let a granted
// tunnel front for an ungranted destination.
func TestTunnelledRequestForAnotherHostIsRefused(t *testing.T) {
	w, _ := tunnelWorld(t)
	target := w.upstream.Listener.Addr().String()
	conn, err := net.Dial("tcp", w.proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	reader := bufio.NewReader(conn)
	established, err := http.ReadResponse(reader, nil)
	if err != nil || established.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT: %v %v", established, err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(w.handler.Authority.CertPEM())
	host, _, _ := net.SplitHostPort(target)
	tlsConn := tls.Client(conn, &tls.Config{RootCAs: roots, ServerName: host})
	fmt.Fprintf(tlsConn, "GET /repos/acme/x HTTP/1.1\r\nHost: evil.example\r\nConnection: close\r\n\r\n")

	resp, err := http.ReadResponse(bufio.NewReader(tlsConn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if n := w.upstreamRequestCount(); n != 0 {
		t.Fatalf("upstream saw %d requests, want none", n)
	}
}

func TestAuthorityIssuesALeafPerHostSignedByItself(t *testing.T) {
	authority, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(authority.CertPEM())
	for _, host := range []string{"api.example.com", "127.0.0.1"} {
		leaf, err := authority.leaf(host)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots}); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
	}
}
