package kubeproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxy_ForwardsAndLogs(t *testing.T) {
	hits := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("upstream missing auth: got %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"kind":"PodList"}`))
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	kubeconfig := filepath.Join(tmp, "kubeconfig")
	writeKubeconfig(t, kubeconfig, upstream.URL, "test-token")

	proxy, err := Start(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	logFile, err := os.Create(filepath.Join(tmp, "kubectl.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	proxy.SetLogger(NewRequestLogger(logFile))
	var observed []ObservationEvent
	proxy.SetObserver(func(event ObservationEvent) { observed = append(observed, event) })

	req, _ := http.NewRequest("GET", proxy.URL()+"/api/v1/namespaces/prod/pods?limit=10", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 || !strings.Contains(string(body), "PodList") {
		t.Errorf("proxy did not forward: status=%d body=%s", resp.StatusCode, body)
	}
	if hits != 1 {
		t.Errorf("upstream hits = %d, want 1", hits)
	}

	if err := logFile.Sync(); err != nil {
		t.Fatal(err)
	}
	logData, _ := os.ReadFile(logFile.Name())
	var ev RequestEvent
	if err := json.Unmarshal(bytes.TrimSpace(logData), &ev); err != nil {
		t.Fatalf("log not JSON: %v\n%s", err, logData)
	}
	if ev.Method != "GET" || ev.Path != "/api/v1/namespaces/prod/pods" || ev.Query != "limit=10" || ev.Status != 200 {
		t.Errorf("log event mismatch: %+v", ev)
	}
	if len(observed) != 1 || observed[0].Method != "GET" || observed[0].Resource != "pods" || observed[0].Status != 200 {
		t.Errorf("observation event mismatch: %+v", observed)
	}
}

func TestProxy_WriteKubeconfig(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	srcKubeconfig := filepath.Join(tmp, "src-kubeconfig")
	writeKubeconfig(t, srcKubeconfig, upstream.URL, "")

	proxy, err := Start(srcKubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	out, err := proxy.WriteKubeconfig(tmp)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), proxy.URL()) {
		t.Errorf("generated kubeconfig missing proxy URL %q:\n%s", proxy.URL(), data)
	}
}

func writeKubeconfig(t *testing.T, path, server, token string) {
	t.Helper()
	user := "user: {}"
	if token != "" {
		user = fmt.Sprintf("user:\n      token: %s", token)
	}
	contents := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: ctx
clusters:
  - name: cluster
    cluster:
      server: %s
      insecure-skip-tls-verify: true
contexts:
  - name: ctx
    context:
      cluster: cluster
      user: u
users:
  - name: u
    %s
`, server, user)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
