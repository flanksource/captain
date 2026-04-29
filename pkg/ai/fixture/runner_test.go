package fixture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaudeScript emits a canned stream-json response depending on --model.
const fakeClaudeScript = `#!/bin/sh
set -eu
model=""
prev=""
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$ARGS_LOG"
  if [ "$prev" = "--model" ]; then
    model="$arg"
  fi
  prev="$arg"
done
printf 'STDIN_BEGIN\n' >> "$ARGS_LOG"
cat >> "$ARGS_LOG"
printf '\nSTDIN_END\n' >> "$ARGS_LOG"
printf '%s\n' '---' >> "$ARGS_LOG"

if [ "$model" = "chatty" ]; then
  cat <<'EOF'
{"type":"system","subtype":"init","session_id":"sess-chatty"}
{"type":"assistant","session_id":"sess-chatty","message":{"role":"assistant","content":[{"type":"text","text":"Let me check the cluster state."}]}}
{"type":"assistant","session_id":"sess-chatty","message":{"role":"assistant","content":[{"type":"tool_use","id":"1","name":"Bash","input":{"command":"kubectl get pods -n prod"}}]}}
{"type":"assistant","session_id":"sess-chatty","message":{"role":"assistant","content":[{"type":"tool_use","id":"2","name":"mcp__mission-control__query","input":{"query":"unhealthy pods"}}]}}
{"type":"result","subtype":"success","session_id":"sess-chatty","result":"Root cause: checkout-api deployment image is tagged latest and was force-pulled to a broken build. Rollback.","cost_usd":0.02,"duration_ms":3000}
EOF
  exit 0
fi

if [ "$model" = "direct-model" ]; then
  cat <<'EOF'
{"type":"assistant","session_id":"sess-direct","message":{"role":"assistant","content":[{"type":"tool_use","id":"1","name":"Bash","input":{"command":"kubectl get pods"}},{"type":"tool_use","id":"2","name":"Bash","input":{"command":"aws eks describe-cluster"}}],"usage":{"input_tokens":1000,"output_tokens":150,"cache_read_input_tokens":40,"cache_creation_input_tokens":20}}}
{"type":"result","subtype":"success","session_id":"sess-direct","cost_usd":0.08,"duration_ms":4000}
EOF
  exit 0
fi

cat <<'EOF'
{"type":"assistant","session_id":"sess-mcp","message":{"role":"assistant","content":[{"type":"tool_use","id":"1","name":"mcp__mission-control__query","input":{"query":"pods"}}],"usage":{"input_tokens":500,"output_tokens":120,"cache_read_input_tokens":200,"cache_creation_input_tokens":10}}}
{"type":"result","subtype":"success","session_id":"sess-mcp","cost_usd":0.01,"duration_ms":1000}
EOF
`

func installFakeClaude(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func setupFixtureDir(t *testing.T, yaml string) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	installFakeClaude(t, tmp)

	fixturePath := filepath.Join(tmp, "fixture.yaml")
	if err := os.WriteFile(fixturePath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "settings.json"), []byte(`{"env":{"FOO":"bar"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "mcp.json"), []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmp, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}

	argsLog := filepath.Join(tmp, "args.log")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARGS_LOG", argsLog)
	return fixturePath, argsLog
}

func TestExecute_ComparesBaseline(t *testing.T) {
	yaml := `name: mc-benchmark
description: MC vs direct bash
prompt: Which cluster is unhealthy?
baseline: direct
defaults:
  timeout: 5s
  promptCaching: true
  settings: settings.json
  mcpConfig:
    - mcp.json
  addDir:
    - workspace
  extraArgs:
    - --verbose
runs:
  - name: direct
    model: direct-model
    tools: [Bash, Read]
    allowedTools:
      - Bash(kubectl *)
      - Bash(aws *)
  - name: mission-control
    model: mcp-model
    tools: [default]
    allowedTools:
      - mcp__mission-control__*
`
	path, argsLog := setupFixtureDir(t, yaml)

	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), f, Options{})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(result.Rows))
	}

	direct := result.Rows[0]
	if direct.Name != "direct" || direct.Status != "OK" {
		t.Errorf("unexpected direct row: %+v", direct)
	}
	if direct.BashCalls != 2 || direct.MCPCalls != 0 {
		t.Errorf("direct tool counts: bash=%d mcp=%d", direct.BashCalls, direct.MCPCalls)
	}
	if direct.Speedup != "1.00x" || direct.Cheaper != "1.00x" {
		t.Errorf("direct ratios: speedup=%s cheaper=%s", direct.Speedup, direct.Cheaper)
	}

	mcp := result.Rows[1]
	if mcp.MCPCalls != 1 || mcp.BashCalls != 0 {
		t.Errorf("mcp tool counts: bash=%d mcp=%d", mcp.BashCalls, mcp.MCPCalls)
	}
	if mcp.Speedup != "4.00x" || mcp.Cheaper != "8.00x" {
		t.Errorf("mcp ratios: speedup=%s cheaper=%s", mcp.Speedup, mcp.Cheaper)
	}
	if mcp.CacheRead != 200 {
		t.Errorf("mcp cache read = %d, want 200", mcp.CacheRead)
	}

	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	args := string(logged)
	for _, want := range []string{
		"--verbose",
		"--exclude-dynamic-system-prompt-sections",
		"--strict-mcp-config",
		filepath.Join(filepath.Dir(path), "settings.json"),
		filepath.Join(filepath.Dir(path), "mcp.json"),
		filepath.Join(filepath.Dir(path), "workspace"),
		"Bash(kubectl *)",
		"mcp__mission-control__*",
		"STDIN_BEGIN\nWhich cluster is unhealthy?\nSTDIN_END",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("logged args missing %q", want)
		}
	}
	if strings.Contains(args, "--max-turns") {
		t.Errorf("args contain removed --max-turns flag:\n%s", args)
	}
}

func TestExecute_AllowedToolsOverridesBypassPermissions(t *testing.T) {
	yaml := `prompt: hi
defaults:
  timeout: 5s
  permissionMode: bypassPermissions
runs:
  - name: restricted
    model: direct-model
    allowedTools:
      - Bash(kubectl *)
  - name: open
    model: direct-model
  - name: explicit
    model: direct-model
    permissionMode: acceptEdits
    allowedTools:
      - Read
`
	path, argsLog := setupFixtureDir(t, yaml)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), f, Options{}); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	blocks := strings.Split(string(logged), "---\n")
	if len(blocks) < 3 {
		t.Fatalf("expected 3 run blocks, got %d", len(blocks))
	}
	restricted, open, explicit := blocks[0], blocks[1], blocks[2]

	if strings.Contains(restricted, "bypassPermissions") {
		t.Errorf("allowedTools must demote bypassPermissions; args:\n%s", restricted)
	}
	if !strings.Contains(restricted, "--permission-mode\ndefault") {
		t.Errorf("allowedTools run should fall back to default mode; args:\n%s", restricted)
	}
	if !strings.Contains(open, "--permission-mode\nbypassPermissions") {
		t.Errorf("run without allowedTools should keep bypassPermissions; args:\n%s", open)
	}
	if !strings.Contains(explicit, "--permission-mode\nacceptEdits") {
		t.Errorf("explicit non-bypass mode must be preserved; args:\n%s", explicit)
	}
}

func TestExecute_MCPOnlyWhenMCPConfigSet(t *testing.T) {
	yaml := `prompt: hi
runs:
  - name: direct
    model: direct-model
    timeout: 5s
  - name: explicit
    model: direct-model
    timeout: 5s
    mcpConfig:
      - '{"mcpServers":{"keep":{}}}'
`
	path, argsLog := setupFixtureDir(t, yaml)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), f, Options{}); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	s := string(args)
	if strings.Count(s, "--strict-mcp-config") != 2 {
		t.Errorf("every run must pass --strict-mcp-config; args:\n%s", s)
	}
	if !strings.Contains(s, `{"mcpServers":{}}`) {
		t.Errorf("run with no mcpConfig should inject empty config; args:\n%s", s)
	}
	if !strings.Contains(s, `{"mcpServers":{"keep":{}}}`) {
		t.Errorf("explicit mcpConfig must be preserved; args:\n%s", s)
	}
}

func TestExecute_RepeatAveragesAndCapturesArtifacts(t *testing.T) {
	yaml := `prompt: hi
baseline: a
runs:
  - name: a
    model: direct-model
    timeout: 5s
    repeat: 3
  - name: b
    model: mcp-model
    timeout: 5s
    repeat: 2
`
	path, _ := setupFixtureDir(t, yaml)
	artifactDir := filepath.Join(t.TempDir(), "artifacts")

	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), f, Options{ArtifactDir: artifactDir})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.Rows[0].Repeat != 3 {
		t.Errorf("row a Repeat = %d, want 3", result.Rows[0].Repeat)
	}
	if result.Rows[1].Repeat != 2 {
		t.Errorf("row b Repeat = %d, want 2", result.Rows[1].Repeat)
	}
	if result.Rows[0].DurationStdevMS != 0 {
		t.Errorf("identical runs should produce stdev=0, got %v", result.Rows[0].DurationStdevMS)
	}
	if result.Rows[0].DurationMeanMS != 4000 {
		t.Errorf("duration mean = %v, want 4000", result.Rows[0].DurationMeanMS)
	}

	for _, want := range []string{"a-1.jsonl", "a-2.jsonl", "a-3.jsonl", "b-1.jsonl", "b-2.jsonl"} {
		info, err := os.Stat(filepath.Join(artifactDir, want))
		if err != nil {
			t.Errorf("artifact %s missing: %v", want, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("artifact %s is empty", want)
		}
	}
}

func TestExecute_WritesProgressPerIteration(t *testing.T) {
	yaml := `prompt: hi
runs:
  - name: a
    model: direct-model
    timeout: 5s
    repeat: 2
  - name: b
    model: mcp-model
    timeout: 5s
    repeat: 1
`
	path, _ := setupFixtureDir(t, yaml)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	if _, err := Execute(context.Background(), f, Options{Progress: &progress}); err != nil {
		t.Fatal(err)
	}
	out := progress.String()
	for _, want := range []string{
		`run "a" iteration 1/2`,
		`run "a" iteration 2/2`,
		`run "b" iteration 1/1`,
		"ok in",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("progress missing %q\n---\n%s", want, out)
		}
	}
}

func TestExecute_StreamsEventsToProgress(t *testing.T) {
	yaml := `prompt: investigate
runs:
  - name: trace
    model: chatty
    timeout: 5s
`
	path, _ := setupFixtureDir(t, yaml)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	if _, err := Execute(context.Background(), f, Options{Progress: &progress}); err != nil {
		t.Fatal(err)
	}
	out := progress.String()
	for _, want := range []string{
		"session sess-chatty",
		"💭 Let me check the cluster state.",
		"→ Bash kubectl get pods -n prod",
		"→ mcp__mission-control__query unhealthy pods",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("progress missing %q\n---\n%s", want, out)
		}
	}
}

func TestExecute_CapturesResultText(t *testing.T) {
	yaml := `prompt: investigate
runs:
  - name: trace
    model: chatty
    timeout: 5s
`
	path, _ := setupFixtureDir(t, yaml)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Rows[0].Result, "Root cause") {
		t.Errorf("row Result missing final text: %q", result.Rows[0].Result)
	}
}

func TestExecute_DefaultsModelInheritedByRuns(t *testing.T) {
	yaml := `prompt: hi
defaults:
  timeout: 5s
  model: claude-sonnet-4-6
runs:
  - name: a
  - name: b
    model: claude-opus-override
`
	path, argsLog := setupFixtureDir(t, yaml)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), f, Options{}); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	blocks := strings.Split(string(logged), "---\n")
	if len(blocks) < 2 {
		t.Fatalf("expected 2 run blocks, got %d", len(blocks))
	}
	if !strings.Contains(blocks[0], "--model\nclaude-sonnet-4-6") {
		t.Errorf("run a should inherit defaults.model:\n%s", blocks[0])
	}
	if !strings.Contains(blocks[1], "--model\nclaude-opus-override") {
		t.Errorf("run b should override with its own model:\n%s", blocks[1])
	}
}

func TestExecute_FixtureLevelRepeatApplies(t *testing.T) {
	yaml := `prompt: hi
repeat: 2
runs:
  - name: a
    model: direct-model
    timeout: 5s
`
	path, _ := setupFixtureDir(t, yaml)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows[0].Repeat != 2 {
		t.Errorf("Repeat = %d, want 2 (inherited from fixture)", result.Rows[0].Repeat)
	}
}

const kubectlClaudeScript = `#!/bin/sh
set -eu
# Pull the proxy URL out of KUBECONFIG and hit it once to simulate kubectl
# making an API call. Emit a stream-json with a Bash tool_use referencing kubectl.
proxy_url=$(grep "server:" "$KUBECONFIG" | head -1 | awk '{print $2}')
curl -sk -o /dev/null "$proxy_url/api/v1/namespaces/prod/pods?limit=10" || true

cat <<'EOF2'
{"type":"system","subtype":"init","session_id":"sess-kc"}
{"type":"assistant","session_id":"sess-kc","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"kubectl get pods -n prod"}}],"usage":{"input_tokens":10,"output_tokens":5}}}
{"type":"user","session_id":"sess-kc","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"NAME    READY   STATUS\nfoo-1    1/1   Running"}]}}
{"type":"result","subtype":"success","session_id":"sess-kc","cost_usd":0.001,"duration_ms":50}
EOF2
`

func TestExecute_CapturesKubectlProxy(t *testing.T) {
	hits := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("upstream missing auth: %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "claude"), []byte(kubectlClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	kubeconfig := filepath.Join(tmp, "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: ctx
clusters:
  - name: c
    cluster:
      server: %s
      insecure-skip-tls-verify: true
contexts:
  - name: ctx
    context:
      cluster: c
      user: u
users:
  - name: u
    user:
      token: test-token
`, upstream.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(tmp, "fixture.yaml")
	yaml := fmt.Sprintf(`prompt: investigate
captureKubernetesProxy: true
kubeconfig: %s
runs:
  - name: probe
    model: kubectl-model
    timeout: 10s
`, kubeconfig)
	if err := os.WriteFile(fixturePath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	f, err := Load(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), f, Options{ArtifactDir: filepath.Join(tmp, "artifacts")})
	if err != nil {
		t.Fatal(err)
	}
	row := result.Rows[0]
	if row.KubectlCalls != 1 {
		t.Errorf("KubectlCalls = %d, want 1", row.KubectlCalls)
	}
	if row.KubectlAPICalls < 1 {
		t.Errorf("KubectlAPICalls = %d, want >=1", row.KubectlAPICalls)
	}
	if hits == 0 {
		t.Errorf("upstream got 0 hits")
	}
	var kubectlCalls []ToolCallEntry
	for _, c := range row.ToolCallLog {
		if c.IsKubectl {
			kubectlCalls = append(kubectlCalls, c)
		}
	}
	if len(kubectlCalls) == 0 || kubectlCalls[0].Command != "kubectl get pods -n prod" {
		t.Errorf("ToolCallLog kubectl entries = %v, want first command %q", kubectlCalls, "kubectl get pods -n prod")
	}
	if len(row.KubectlAPILog) == 0 {
		t.Fatalf("KubectlAPILog empty")
	}
	first := row.KubectlAPILog[0]
	if first.Method != "GET" || first.URL != "/api/v1/namespaces/prod/pods?limit=10" || first.Status != 200 {
		t.Errorf("KubectlAPILog[0] = %+v, want GET /api/v1/namespaces/prod/pods?limit=10 200", first)
	}

	logData, err := os.ReadFile(row.KubectlLogPath)
	if err != nil {
		t.Fatalf("read kubectl log: %v", err)
	}
	var sawReq bool
	for _, line := range strings.Split(strings.TrimSpace(string(logData)), "\n") {
		var ev struct {
			Type string `json:"type"`
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("bad log line %q: %v", line, err)
			continue
		}
		if ev.Type == "request" && strings.Contains(ev.Path, "/api/v1/namespaces/prod/pods") {
			sawReq = true
		}
	}
	if !sawReq {
		t.Errorf("kubectl log missing API request entry:\n%s", logData)
	}
}
