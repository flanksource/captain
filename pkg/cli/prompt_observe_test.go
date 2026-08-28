package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/fixture/mcpproxy"
	"github.com/flanksource/captain/pkg/ai/observation"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	"k8s.io/client-go/tools/clientcmd"
)

type observationUsageProvider struct {
	usage *api.Usage
}

func (p observationUsageProvider) GetModel() string       { return "test-model" }
func (p observationUsageProvider) GetBackend() ai.Backend { return ai.BackendOpenAI }

func (p observationUsageProvider) Execute(ctx context.Context, _ ai.Request) (*ai.Response, error) {
	observation.RecordUsage(ctx, p.usage)
	return &ai.Response{Model: p.GetModel()}, nil
}

func TestApplyObservationMetricsPreservesDisjointUsageAndProviderCost(t *testing.T) {
	result := api.RuntimeObservation{Metrics: api.ObservationMetrics{
		CostUSD: api.ObservationCostFact{State: api.ObservationFactUnknown, Unit: "USD"},
		Usage:   api.ObservationUsageFact{State: api.ObservationFactUnknown, Semantics: "disjoint-v1"},
	}}
	usage := api.Usage{
		InputTokens: 11, OutputTokens: 12, ReasoningTokens: 13,
		CacheReadTokens: 14, CacheWriteTokens: 15,
	}

	applyObservationMetrics(&result, api.BackendClaudeCLI, "claude-sonnet-5", &usage, 0.25)

	if result.Metrics.Usage.State != api.ObservationFactKnown || result.Metrics.Usage.Buckets == nil {
		t.Fatalf("usage fact = %#v, want known buckets", result.Metrics.Usage)
	}
	want := api.ObservationUsageBuckets{
		InputTokens: 11, OutputTokens: 12, ReasoningTokens: 13,
		CacheReadTokens: 14, CacheWriteTokens: 15,
	}
	if got := *result.Metrics.Usage.Buckets; got != want {
		t.Fatalf("usage buckets = %#v, want %#v", got, want)
	}
	if result.Metrics.CostUSD.State != api.ObservationFactKnown ||
		result.Metrics.CostUSD.Value == nil || *result.Metrics.CostUSD.Value != 0.25 ||
		result.Metrics.CostUSD.Source != "provider" {
		t.Fatalf("cost fact = %#v, want provider-reported 0.25", result.Metrics.CostUSD)
	}
}

func TestExecuteObservationProviderDistinguishesMissingFromZeroUsage(t *testing.T) {
	tests := []struct {
		name      string
		usage     *api.Usage
		wantState api.ObservationFactState
	}{
		{name: "omitted", usage: nil, wantState: api.ObservationFactUnknown},
		{name: "present zero", usage: &api.Usage{}, wantState: api.ObservationFactKnown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := observation.NewRecorder()
			ctx := observation.ContextWithRecorder(context.Background(), recorder)
			run := executeObservationProvider(ctx, observationUsageProvider{usage: test.usage}, ai.Request{}, false, recorder)
			result := api.RuntimeObservation{Metrics: api.ObservationMetrics{
				Usage: api.ObservationUsageFact{State: api.ObservationFactUnknown, Semantics: "disjoint-v1"},
			}}
			applyObservationMetrics(&result, api.BackendOpenAI, "test-model", run.usage, 0)

			if result.Metrics.Usage.State != test.wantState {
				t.Fatalf("usage state = %q, want %q", result.Metrics.Usage.State, test.wantState)
			}
			if test.usage == nil {
				if result.Metrics.Usage.Buckets != nil {
					t.Fatalf("omitted usage buckets = %#v, want nil", result.Metrics.Usage.Buckets)
				}
				return
			}
			if result.Metrics.Usage.Buckets == nil || *result.Metrics.Usage.Buckets != (api.ObservationUsageBuckets{}) {
				t.Fatalf("known-zero usage buckets = %#v, want all five zero buckets", result.Metrics.Usage.Buckets)
			}
		})
	}
}

func TestObservationCaptureWritesOnlyNormalizedBoundedArtifacts(t *testing.T) {
	dir := t.TempDir()
	session := &observationCaptureSession{
		mcpCapture:  api.ObservationExternalCapture{Status: api.ObservationCaptureComplete, Events: []api.ObservationExternalEvent{}},
		kubeCapture: api.ObservationExternalCapture{Status: api.ObservationCaptureNotRequested, Events: []api.ObservationExternalEvent{}},
		artifactDir: filepath.Join(dir, "artifacts"), artifactBase: dir,
	}
	session.recordMCP(mcpproxy.ObservationEvent{
		Server: "server with spaces", HTTPMethod: "POST", RPCMethod: "tools/call",
		Tool: "query", Status: 200,
	})
	result := api.RuntimeObservation{Artifacts: []api.ObservationArtifact{}}
	session.Apply(&result, []api.ObservationToolEvent{{ToolCallID: "call-1", Name: "mcp__server_with_spaces__query"}})
	if result.Capture.MCP.Status != api.ObservationCaptureComplete || len(result.Capture.MCP.Events) != 1 {
		t.Fatalf("MCP capture = %#v", result.Capture.MCP)
	}
	event := result.Capture.MCP.Events[0]
	if event.Target != "server_with_spaces" || event.Method != "tools/call" || event.CorrelationID != "call-1" {
		t.Fatalf("normalized MCP event = %#v", event)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].SizeBytes <= 0 || result.Artifacts[0].SHA256 == "" {
		t.Fatalf("artifacts = %#v", result.Artifacts)
	}
	data, err := os.ReadFile(filepath.Join(dir, result.Artifacts[0].Path))
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	for _, forbidden := range []string{"Authorization", "Bearer", "prompt", "arguments", "credential"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("normalized artifact contains %q: %s", forbidden, data)
		}
	}
}

func TestObservationCaptureKeepsSharedUpstreamAliasesDistinct(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mcp.json")
	config := `{"mcpServers":{"alpha":{"url":"` + upstream.URL + `/mcp"},"beta":{"url":"` + upstream.URL + `/mcp"}}}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write MCP config: %v", err)
	}
	session, err := startObservationCapture(
		map[string]string{"mcp-config": configPath, "artifacts": filepath.Join(dir, "artifacts")},
		api.Model{Backend: api.BackendClaudeCLI},
		ai.Request{Setup: &shell.Setup{Cwd: dir}},
		ai.Config{Model: api.Model{Backend: api.BackendClaudeCLI}},
		"observation-1",
	)
	if err != nil {
		t.Fatalf("startObservationCapture: %v", err)
	}
	defer session.Close()
	data, err := os.ReadFile(session.runtime.MCPConfigs[0])
	if err != nil {
		t.Fatalf("read rewritten MCP config: %v", err)
	}
	var rewritten struct {
		Servers map[string]struct {
			URL string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &rewritten); err != nil {
		t.Fatalf("decode rewritten MCP config: %v", err)
	}
	if rewritten.Servers["alpha"].URL == rewritten.Servers["beta"].URL {
		t.Fatalf("shared-upstream aliases use one observation proxy: %#v", rewritten.Servers)
	}
	payload := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"query"}}`
	for _, alias := range []string{"alpha", "beta"} {
		request, err := http.NewRequest(http.MethodPost, rewritten.Servers[alias].URL, strings.NewReader(payload))
		if err != nil {
			t.Fatalf("create %s request: %v", alias, err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("request %s proxy: %v", alias, err)
		}
		response.Body.Close()
	}
	result := api.RuntimeObservation{Artifacts: []api.ObservationArtifact{}}
	session.Apply(&result, []api.ObservationToolEvent{
		{ToolCallID: "call-alpha", Name: "mcp__alpha__query"},
		{ToolCallID: "call-beta", Name: "mcp__beta__query"},
	})
	if len(result.Capture.MCP.Events) != 2 {
		t.Fatalf("MCP events = %#v", result.Capture.MCP.Events)
	}
	correlations := map[string]string{}
	for _, event := range result.Capture.MCP.Events {
		correlations[event.Target] = event.CorrelationID
	}
	if correlations["alpha"] != "call-alpha" || correlations["beta"] != "call-beta" {
		t.Fatalf("MCP correlations = %#v", correlations)
	}
}

func TestObservationCaptureTruncatesExternalEvents(t *testing.T) {
	dir := t.TempDir()
	session := &observationCaptureSession{
		mcpCapture:  api.ObservationExternalCapture{Status: api.ObservationCaptureComplete, Events: []api.ObservationExternalEvent{}},
		kubeCapture: api.ObservationExternalCapture{Status: api.ObservationCaptureNotRequested, Events: []api.ObservationExternalEvent{}},
		artifactDir: filepath.Join(dir, "artifacts"), artifactBase: dir,
	}
	for range maxObservationExternalEvents + 1 {
		session.recordMCP(mcpproxy.ObservationEvent{Server: "server", HTTPMethod: "POST", Status: 200})
	}
	result := api.RuntimeObservation{Artifacts: []api.ObservationArtifact{}}
	session.Apply(&result, nil)
	if result.Capture.MCP.Status != api.ObservationCapturePartial || result.Capture.MCP.ReasonCode != "capture_truncated" {
		t.Fatalf("MCP capture = %#v, want partial capture_truncated", result.Capture.MCP)
	}
	if len(result.Capture.MCP.Events) != maxObservationExternalEvents {
		t.Fatalf("MCP events = %d, want %d", len(result.Capture.MCP.Events), maxObservationExternalEvents)
	}
}

func TestObservationCaptureRoutesKubernetesTraffic(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "config")
	config := "apiVersion: v1\nkind: Config\ncurrent-context: test\nclusters:\n- name: test\n  cluster:\n    server: " + upstream.URL + "\ncontexts:\n- name: test\n  context:\n    cluster: test\n"
	if err := os.WriteFile(kubeconfig, []byte(config), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	session, err := startObservationCapture(
		map[string]string{"capture-kubernetes": "true", "kubeconfig": kubeconfig, "artifacts": filepath.Join(dir, "artifacts")},
		api.Model{Backend: api.BackendCodexCLI},
		ai.Request{Setup: &shell.Setup{Cwd: dir}},
		ai.Config{Model: api.Model{Backend: api.BackendCodexCLI}},
		"observation-1",
	)
	if err != nil {
		t.Fatalf("startObservationCapture: %v", err)
	}
	defer session.Close()
	rewritten, err := clientcmd.LoadFromFile(session.runtime.Environment["KUBECONFIG"])
	if err != nil {
		t.Fatalf("load rewritten kubeconfig: %v", err)
	}
	response, err := http.Get(rewritten.Clusters["captain-proxy"].Server + "/api/v1/namespaces/prod/pods?token=not-observed")
	if err != nil {
		t.Fatalf("request Kubernetes proxy: %v", err)
	}
	response.Body.Close()
	result := api.RuntimeObservation{Artifacts: []api.ObservationArtifact{}}
	session.Apply(&result, nil)
	if result.Capture.Kubernetes.Status != api.ObservationCaptureComplete || len(result.Capture.Kubernetes.Events) != 1 {
		t.Fatalf("Kubernetes capture = %#v", result.Capture.Kubernetes)
	}
	event := result.Capture.Kubernetes.Events[0]
	if event.Method != http.MethodGet || event.Resource != "pods" || event.Status != "200" {
		t.Fatalf("Kubernetes event = %#v", event)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Kind != "kubernetes_capture" {
		t.Fatalf("artifacts = %#v", result.Artifacts)
	}
}

func TestObservationCaptureRejectsUnsupportedMCPConfigBeforeDispatch(t *testing.T) {
	session, err := startObservationCapture(
		map[string]string{"mcp-config": "mcp.json"},
		api.Model{Backend: api.BackendOpenAI},
		ai.Request{Setup: &shell.Setup{Cwd: t.TempDir()}},
		ai.Config{Model: api.Model{Backend: api.BackendOpenAI}},
		"observation-1",
	)
	if err != nil {
		t.Fatalf("startObservationCapture: %v", err)
	}
	defer session.Close()
	if session.blockingCode != "mcp_config_unsupported" || session.mcpCapture.Status != api.ObservationCaptureUnsupported {
		t.Fatalf("capture session = %#v", session)
	}
}
