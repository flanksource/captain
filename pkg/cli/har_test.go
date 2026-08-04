package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/commons/har"
	"github.com/flanksource/commons/properties"
)

// withProperty sets a -P property for one test. Properties are process-global,
// so every test that sets one must restore it.
func withProperty(t *testing.T, key, value string) {
	t.Helper()
	properties.Set(key, value)
	t.Cleanup(func() { properties.Set(key, "") })
}

// withCleanRegistry gives a test its own collectors. The package registry is
// process-wide because http.DefaultTransport is, and it retains collectors so a
// second Flush rewrites rather than truncates — which across tests would mean
// one test flushing another's archive into a deleted temp dir.
func withCleanRegistry(t *testing.T) {
	t.Helper()
	previous := harRegistry
	harRegistry = har.NewRegistry()
	t.Cleanup(func() { harRegistry = previous })
}

// captureThrough issues one request against a JSON server through the HAR
// transport captain installs, and returns the entries written to path.
func captureThrough(t *testing.T, path string) []har.Entry {
	t.Helper()
	withCleanRegistry(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(server.Close)

	transport, err := harRegistry.Transport(harFeature, http.DefaultTransport)
	if err != nil {
		t.Fatalf("Transport: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	FlushHAR()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no HAR written to %s: %v", path, err)
	}
	var file har.File
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("HAR is not valid JSON: %v", err)
	}
	return file.Log.Entries
}

// TestHARDisabledWithoutProperty pins the default: captain must not pay for HAR
// capture, and the transport chain must be left exactly as it was.
func TestHARDisabledWithoutProperty(t *testing.T) {
	base := http.DefaultTransport
	transport, err := harRegistry.Transport(harFeature, base)
	if err != nil {
		t.Fatalf("Transport: %v", err)
	}
	if transport != base {
		t.Error("transport must be unchanged when http.har is unset")
	}
}

// TestHARCapturesProviderTraffic is the end-to-end contract of -Phttp.har: a
// request through captain's default transport lands in a readable archive.
func TestHARCapturesProviderTraffic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.har")
	withProperty(t, "http.har", path)

	entries := captureThrough(t, path)

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !strings.HasSuffix(entries[0].Request.URL, "/v1/models") {
		t.Errorf("unexpected captured URL: %s", entries[0].Request.URL)
	}
	if entries[0].Response.Content.Text != `{"data":[]}` {
		t.Errorf("expected the response body, got %q", entries[0].Response.Content.Text)
	}
}

// TestHARFeaturePropertyOverridesGlobal covers -Phttp.captain.har, which lets a
// user capture captain's own traffic without touching a shared http.har.
func TestHARFeaturePropertyOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	featurePath := filepath.Join(dir, "captain.har")
	withProperty(t, "http.har", filepath.Join(dir, "global.har"))
	withProperty(t, "http."+harFeature+".har", featurePath)

	if entries := captureThrough(t, featurePath); len(entries) != 1 {
		t.Fatalf("expected 1 entry in the feature archive, got %d", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "global.har")); !os.IsNotExist(err) {
		t.Error("the global archive must not be written when a feature override is set")
	}
}

// TestHARInvalidLevelFailsFast covers the reason EnableHTTPWireLogging returns
// an error: a typo'd level must stop the run, not capture the wrong thing.
func TestHARInvalidLevelFailsFast(t *testing.T) {
	withProperty(t, "http.har", filepath.Join(t.TempDir(), "trace.har"))
	withProperty(t, "http.har.level", "verbose")

	if _, err := harRegistry.Transport(harFeature, http.DefaultTransport); err == nil {
		t.Fatal("expected an error for an unrecognised http.har.level")
	}
}
