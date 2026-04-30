package fixture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai/fixture/mcpproxy"
)

func TestRewriteMCPConfig_NoMCPServersKey(t *testing.T) {
	in := []byte(`{"foo":"bar"}`)
	out, infos, err := rewriteMCPConfig(in, map[string]*mcpproxy.Proxy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Errorf("infos = %v, want empty", infos)
	}
	if string(out) != string(in) {
		t.Errorf("config rewritten unnecessarily: %s", out)
	}
}

func TestRewriteMCPConfig_StdioOnly(t *testing.T) {
	in := []byte(`{"mcpServers":{"local":{"command":"./bin","args":["x"]}}}`)
	proxies := map[string]*mcpproxy.Proxy{}
	out, infos, err := rewriteMCPConfig(in, proxies, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 || len(proxies) != 0 {
		t.Errorf("stdio-only config should not start any proxy; got %d proxies", len(proxies))
	}
	// We return the original bytes unchanged when nothing was rewritten.
	if string(out) != string(in) {
		t.Errorf("stdio-only config was modified: %s", out)
	}
}

func TestRewriteMCPConfig_HTTPRewrite(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	in := []byte(`{"mcpServers":{"mc":{"url":"` + upstream.URL + `/api","headers":{"Authorization":"Bearer x"}}}}`)
	proxies := map[string]*mcpproxy.Proxy{}
	out, infos, err := rewriteMCPConfig(in, proxies, map[string]string{"Accept": "text/markdown"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, p := range proxies {
			p.Close()
		}
	}()
	if len(infos) != 1 || infos[0].Server != "mc" || infos[0].Upstream != upstream.URL+"/api" {
		t.Fatalf("infos = %+v, want one entry for 'mc'", infos)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	servers := got["mcpServers"].(map[string]any)
	mc := servers["mc"].(map[string]any)
	newURL := mc["url"].(string)
	if !strings.HasPrefix(newURL, "http://127.0.0.1:") {
		t.Errorf("url not rewritten to local proxy: %q", newURL)
	}
	if !strings.HasSuffix(newURL, "/api") {
		t.Errorf("rewritten url should preserve upstream path /api, got %q", newURL)
	}
	// Original headers (auth) must remain in the rewritten config.
	headers := mc["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer x" {
		t.Errorf("Authorization header lost in rewrite: %v", headers)
	}
}

func TestRewriteMCPConfig_DedupesByUpstreamURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer upstream.Close()

	in1 := []byte(`{"mcpServers":{"a":{"url":"` + upstream.URL + `/api"}}}`)
	in2 := []byte(`{"mcpServers":{"b":{"url":"` + upstream.URL + `/api"}}}`)
	proxies := map[string]*mcpproxy.Proxy{}
	if _, _, err := rewriteMCPConfig(in1, proxies, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rewriteMCPConfig(in2, proxies, nil); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, p := range proxies {
			p.Close()
		}
	}()
	if len(proxies) != 1 {
		t.Errorf("expected dedup by upstream URL, got %d proxies", len(proxies))
	}
}

func TestReadMCPConfigContent_InlineAndPath(t *testing.T) {
	dir := t.TempDir()
	inline := `{"mcpServers":{}}`
	got, label, err := readMCPConfigContent(dir, inline)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != inline {
		t.Errorf("inline content = %q", got)
	}
	if label != "(inline)" {
		t.Errorf("label = %q, want (inline)", label)
	}
}
