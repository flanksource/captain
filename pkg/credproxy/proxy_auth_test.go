package credproxy

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/url"
	"testing"
)

const proxySecret = "per-tool-proxy-secret"

// securedWorld is tunnelWorld whose proxy requires proxySecret, and a client
// factory that reaches it through a proxy URL carrying the given credential.
func securedWorld(t *testing.T) (*world, func(credential string) *http.Client) {
	t.Helper()
	w, _ := tunnelWorld(t)
	w.handler.Secret = proxySecret
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(w.handler.Authority.CertPEM())
	return w, func(credential string) *http.Client {
		proxyURL, err := url.Parse(ProxyURL(w.proxy.Listener.Addr().String(), credential))
		if err != nil {
			t.Fatal(err)
		}
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport}
	}
}

func TestProxyWithASecretRefusesUnauthenticatedClients(t *testing.T) {
	for name, credential := range map[string]string{"no credential": "", "wrong credential": "guessed"} {
		t.Run(name, func(t *testing.T) {
			w, clientWith := securedWorld(t)
			client := clientWith(credential)

			resp, err := client.Get("http://" + w.upstream.Listener.Addr().String() + "/repos/acme/captain")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusProxyAuthRequired {
				t.Fatalf("absolute-form request: status = %d, want 407", resp.StatusCode)
			}
			if _, err := client.Get(w.upstream.URL + "/repos/acme/captain"); err == nil {
				t.Fatal("CONNECT without the proxy secret opened a tunnel")
			}
			if n := w.upstreamRequestCount(); n != 0 {
				t.Fatalf("upstream saw %d requests, want none", n)
			}
		})
	}
}

func TestProxyWithASecretServesTheClientThatCarriesIt(t *testing.T) {
	w, clientWith := securedWorld(t)
	req, _ := http.NewRequest(http.MethodGet, w.upstream.URL+"/repos/acme/captain", nil)
	req.Header.Set("Authorization", w.grant.Placeholder("Authorization"))

	resp, err := clientWith(proxySecret).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "upstream says hi" {
		t.Fatalf("status = %d body = %q", resp.StatusCode, body)
	}
	if got := w.upstreamHeaderValues("Proxy-Authorization"); len(got) != 1 || got[0] != "" {
		t.Fatalf("upstream received Proxy-Authorization %v; the proxy secret must stop at the proxy", got)
	}
}
