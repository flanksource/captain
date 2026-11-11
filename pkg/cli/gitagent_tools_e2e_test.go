package cli

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/credproxy"
	"github.com/flanksource/captain/pkg/gitagent"
)

// toolCallingClaude stands in for the claude CLI on the agent host. It does
// what claude does with a caller-tool server: read the MCP config captain
// hands it, call the tool over streamable HTTP, and act on the result — here
// by writing it into the worktree, so the result travels back as the task's
// work. It emits the stream-json a successful turn ends with.
const toolCallingClaude = `#!/bin/sh
set -eu
config=""
while [ $# -gt 0 ]; do
  case "$1" in
    --version) echo "2.1.0 (Claude Code)"; exit 0 ;;
    --mcp-config) config="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ -z "$config" ]; then echo "no --mcp-config: the agent was not given the supervisor's tools" >&2; exit 3; fi
url=$(sed -n 's/.*"url":"\([^"]*\)".*/\1/p' "$config")
auth=$(sed -n 's/.*"Authorization":"\([^"]*\)".*/\1/p' "$config")
: > tool-result.txt
for tool in ${CAPTAIN_TEST_TOOLS:-mark}; do
  curl -fsS -X POST "$url" -H "Authorization: $auth" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"'"$tool"'","arguments":{}}}' >> tool-result.txt
  echo >> tool-result.txt
done
cat <<'EOF'
{"type":"system","subtype":"init","session_id":"sess-tools"}
{"type":"assistant","session_id":"sess-tools","message":{"role":"assistant","content":[{"type":"text","text":"called mark"}],"usage":{"input_tokens":10,"output_tokens":5}}}
{"type":"result","subtype":"success","session_id":"sess-tools","result":"called mark","cost_usd":0.001,"duration_ms":50}
EOF
`

// seedPricingCache copies this machine's OpenRouter pricing snapshot into each
// host's cache directory. Every host has a fresh HOME, and resolving a model
// otherwise downloads the whole catalog — on a slow link that alone outlasts
// the dispatch budget. The snapshot is read from the OS user's real home,
// because TestMain points HOME at a scratch directory. A missing or stale
// snapshot only costs the download, so it is logged rather than fatal.
func seedPricingCache(t *testing.T, hosts ...*host) {
	t.Helper()
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(os.Getenv("HOME"), cacheDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("cache dir %s is not under HOME (%v)", cacheDir, err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(filepath.Join(account.HomeDir, rel, "flanksource", "openrouter-pricing.json"))
	if err != nil {
		t.Logf("no pricing snapshot to seed; each host downloads the catalog: %v", err)
		return
	}
	for _, h := range hosts {
		dir := filepath.Join(h.home, rel, "flanksource")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "openrouter-pricing.json"), snapshot, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDispatchServesSupervisorToolsToTheAgent runs the whole chain a git-agent
// run's inline tool takes: the supervisor serves the prompt's tool, the
// sidecar's runner forwards it to the agent through the service tunnel, the
// agent calls it, the script runs on the supervisor, and its output returns
// to the agent — with nothing reachable once the run is over.
func TestDispatchServesSupervisorToolsToTheAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary and runs two endpoints")
	}
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "claude"), []byte(toolCallingClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	supervisor, agent, repo, _, _ := enrollPair(t)
	seedPricingCache(t, supervisor, agent)
	supervisor.configureDefaultModel(t, "cli:claude-sonnet-5-5")
	agent.configureDefaultModel(t, "cli:claude-sonnet-5-5")

	marker := filepath.Join(t.TempDir(), "supervisor-marker")
	writeAt(t, repo, "task.prompt", `---
sandbox: git-agent
tools:
  mark:
    description: Leave a marker on the supervisor host
    readonly: true
    exec: |
      echo supervisor-ran > `+marker+`
      echo marked-by-supervisor
---
{{role "user"}}
Call mark.
`)

	dispatch := exec.Command(supervisor.bin, "prompt", "run", "./task.prompt", "--sandbox", "git-agent", "--timeout", "3m")
	dispatch.Dir = repo
	dispatch.Env = supervisor.env()
	out, err := dispatch.CombinedOutput()
	if err != nil {
		t.Fatalf("the dispatch did not conclude: %v\n%s\n%s", err, out, agentLogs(t, agent))
	}

	ran, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(ran)) != "supervisor-ran" {
		t.Fatalf("the tool's script did not run on the supervisor (%v): %q\n%s\n%s", err, ran, out, agentLogs(t, agent))
	}
	branch := ""
	for _, line := range strings.Split(gitIn(t, repo, "branch", "--format=%(refname:short)"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "captain/") {
			branch = strings.TrimSpace(line)
		}
	}
	if branch == "" {
		t.Fatalf("accepted work was not integrated:\n%s\n%s", gitIn(t, repo, "branch"), out)
	}
	if result := gitIn(t, repo, "show", branch+":tool-result.txt"); !strings.Contains(result, "marked-by-supervisor") {
		t.Fatalf("the agent did not receive the tool's output:\n%s", result)
	}

	mailbox := mailboxPathForRepo(t, supervisor, repo)
	services, _ := filepath.Glob(filepath.Join(mailbox, "captain", "tasks", "*", "services.json"))
	if len(services) != 0 {
		t.Fatalf("the run ended but its services are still published: %v", services)
	}
}

// TestDispatchRunsSidecarToolsBesideTheAgent places tools on the sidecar: one
// that needs nothing, and one whose grant value the supervisor releases once
// to a proxy on the sidecar. The agent sees all of them, the supervisor's
// included, as one server, and the granted value appears nowhere it could
// read — not in the task's files, not in its logs, not in its work.
func TestDispatchRunsSidecarToolsBesideTheAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary and runs two endpoints")
	}
	const grantValue = "e2e-released-grant-value"
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "claude"), []byte(toolCallingClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTAIN_TEST_TOOLS", "mark where probe")
	supervisor, agent, repo, _, _ := enrollPair(t)
	seedPricingCache(t, supervisor, agent)
	supervisor.configureDefaultModel(t, "cli:claude-sonnet-5-5")
	agent.configureDefaultModel(t, "cli:claude-sonnet-5-5")

	marker := filepath.Join(t.TempDir(), "supervisor-marker")
	// Outside the repository: the dispatch ships the repository's dirty state,
	// and a prompt kept in it would carry its static grant value with it.
	prompts := t.TempDir()
	writeAt(t, prompts, "task.prompt", `---
sandbox: git-agent
tools:
  mark:
    description: Leave a marker on the supervisor host
    readonly: true
    exec: |
      echo supervisor-ran > `+marker+`
      echo marked-by-supervisor
  where:
    description: Say where the tool runs
    readonly: true
    placement: sidecar
    exec: echo "sidecar-cwd=$(basename "$(dirname "$PWD")")/$(basename "$PWD")"
  probe:
    description: Show what a proxied tool is given
    readonly: true
    placement: sidecar
    credentials: sidecar-proxied
    grants:
      - name: gh
        url: https://api.example.com
        methods: [GET]
        paths: [/repos/]
        headers:
          - name: Authorization
            valueFrom: {value: `+grantValue+`}
    exec: |
      echo "placeholder=$GH_AUTHORIZATION"
      case "$HTTPS_PROXY" in http://captain:*@127.0.0.1:*) echo proxy=authenticated ;; *) echo "proxy=missing" ;; esac
---
{{role "user"}}
Call mark, where and probe.
`)

	dispatch := exec.Command(supervisor.bin, "prompt", "run", filepath.Join(prompts, "task.prompt"), "--sandbox", "git-agent", "--timeout", "3m")
	dispatch.Dir = repo
	dispatch.Env = supervisor.env()
	out, err := dispatch.CombinedOutput()
	if err != nil {
		t.Fatalf("the dispatch did not conclude: %v\n%s\n%s", err, out, agentLogs(t, agent))
	}
	if ran, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(ran)) != "supervisor-ran" {
		t.Fatalf("the supervisor tool did not run on the supervisor (%v): %q", err, ran)
	}
	branch := ""
	for _, line := range strings.Split(gitIn(t, repo, "branch", "--format=%(refname:short)"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "captain/") {
			branch = strings.TrimSpace(line)
		}
	}
	if branch == "" {
		t.Fatalf("accepted work was not integrated:\n%s\n%s", gitIn(t, repo, "branch"), out)
	}
	result := gitIn(t, repo, "show", branch+":tool-result.txt")
	placeholder := credproxy.Grant{Name: "gh"}.Placeholder("Authorization")
	for _, want := range []string{"marked-by-supervisor", "placeholder=" + placeholder, "proxy=authenticated"} {
		if !strings.Contains(result, want) {
			t.Errorf("tool results lack %q:\n%s", want, result)
		}
	}
	if !regexp.MustCompile(`sidecar-cwd=t-[0-9a-f]+/worktree`).MatchString(result) {
		t.Errorf("the sidecar tool did not run in the task's worktree:\n%s", result)
	}
	if strings.Contains(result, grantValue) {
		t.Fatalf("the granted value reached the agent's work:\n%s", result)
	}
	sidecarTasks := filepath.Join(agent.home, ".captain", "sandbox", gitagent.ServedReposDirName, SidecarRepoName, "captain", "tasks")
	leaked := ""
	_ = filepath.WalkDir(sidecarTasks, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if data, readErr := os.ReadFile(path); readErr == nil && strings.Contains(string(data), grantValue) {
			leaked += path + "\n"
		}
		return nil
	})
	if leaked != "" {
		t.Fatalf("the granted value was written on the sidecar:\n%s", leaked)
	}
}

// TestDispatchProxiesSidecarToolsThroughTheSupervisor places a proxied tool on
// the sidecar: its kubernetes connection and grants stay on the supervisor,
// whose proxies the tool reaches through the task's tunnel. The kubeconfig's
// token answers the tool's request and is written nowhere on the sidecar.
func TestDispatchProxiesSidecarToolsThroughTheSupervisor(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary and runs two endpoints")
	}
	const kubeToken = "e2e-supervisor-kube-token"
	kubeAPI := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+kubeToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"kind":"NamespaceList"}`))
	}))
	t.Cleanup(kubeAPI.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: kubeAPI.Certificate().Raw})
	kubeconfig, err := json.Marshal(fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: fake\n"+
		"clusters:\n- name: fake\n  cluster:\n    server: %s\n    certificate-authority-data: %s\n"+
		"contexts:\n- name: fake\n  context: {cluster: fake, user: fake}\n"+
		"users:\n- name: fake\n  user: {token: %s}\n", kubeAPI.URL, base64.StdEncoding.EncodeToString(ca), kubeToken))
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "claude"), []byte(toolCallingClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTAIN_TEST_TOOLS", "cluster")
	supervisor, agent, repo, _, _ := enrollPair(t)
	seedPricingCache(t, supervisor, agent)
	supervisor.configureDefaultModel(t, "cli:claude-sonnet-5-5")
	agent.configureDefaultModel(t, "cli:claude-sonnet-5-5")

	prompts := t.TempDir()
	writeAt(t, prompts, "task.prompt", `---
sandbox: git-agent
tools:
  cluster:
    description: List namespaces through the supervisor's proxies
    readonly: true
    placement: sidecar
    credentials: proxied
    connections:
      kubernetes:
        kubeconfig: {value: `+string(kubeconfig)+`}
    grants:
      - name: gh
        url: https://api.example.com
        methods: [GET]
        paths: [/repos/]
    exec: |
      server=$(sed -n 's/^ *server: //p' "$KUBECONFIG")
      token=$(sed -n 's/^ *token: //p' "$KUBECONFIG")
      echo "namespaces=$(curl -fsS -H "Authorization: Bearer $token" "$server/api/v1/namespaces" 2>&1) (kubeconfig=${KUBECONFIG:-unset} server=$server)"
      echo "ungranted=$(curl -s -o /dev/null -w '%{http_connect}' https://ungranted.example.com/ || true)"
---
{{role "user"}}
Call cluster.
`)

	dispatch := exec.Command(supervisor.bin, "prompt", "run", filepath.Join(prompts, "task.prompt"), "--sandbox", "git-agent", "--timeout", "3m")
	dispatch.Dir = repo
	dispatch.Env = supervisor.env()
	out, err := dispatch.CombinedOutput()
	if err != nil {
		t.Fatalf("the dispatch did not conclude: %v\n%s\n%s", err, out, agentLogs(t, agent))
	}
	branch := ""
	for _, line := range strings.Split(gitIn(t, repo, "branch", "--format=%(refname:short)"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "captain/") {
			branch = strings.TrimSpace(line)
		}
	}
	if branch == "" {
		t.Fatalf("accepted work was not integrated:\n%s\n%s", gitIn(t, repo, "branch"), out)
	}
	result := gitIn(t, repo, "show", branch+":tool-result.txt")
	// 403 is the supervisor's egress proxy refusing an ungranted host: the
	// tool reached it, through the tunnel, with its secret.
	for _, want := range []string{`namespaces={\"kind\":\"NamespaceList\"}`, "ungranted=403"} {
		if !strings.Contains(result, want) {
			t.Errorf("tool results lack %q:\n%s\n%s", want, result, agentLogs(t, agent))
		}
	}
	sidecarTasks := filepath.Join(agent.home, ".captain", "sandbox", gitagent.ServedReposDirName, SidecarRepoName, "captain", "tasks")
	leaked := ""
	_ = filepath.WalkDir(sidecarTasks, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if data, readErr := os.ReadFile(path); readErr == nil && strings.Contains(string(data), kubeToken) {
			leaked += path + "\n"
		}
		return nil
	})
	if leaked != "" || strings.Contains(result, kubeToken) {
		t.Fatalf("the supervisor's kube token reached the sidecar:\n%s%s", leaked, result)
	}
}
