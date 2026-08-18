package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
)

func sandboxMux() *http.ServeMux {
	mux := http.NewServeMux()
	registerSandboxHandlers(mux)
	return mux
}

// loopbackRequest mimics a same-origin browser call from the local webapp, which
// is what validateLocalConfigurationRequest admits.
func loopbackRequest(method, target string, body string) *http.Request {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, reader)
	r.RemoteAddr = "127.0.0.1:54321"
	r.Host = "127.0.0.1:9020"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func serveSandbox(t *testing.T, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	sandboxMux().ServeHTTP(w, r)
	return w
}

func TestSandboxCatalogRouteServesEveryAdapter(t *testing.T) {
	isolatedConfig(t)

	w := serveSandbox(t, loopbackRequest(http.MethodGet, "/api/captain/sandboxes", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	var catalog SandboxCatalog
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	byKind := map[string]SandboxCatalogEntry{}
	for _, entry := range catalog.Kinds {
		byKind[entry.Kind] = entry
	}
	for _, kind := range []string{"none", "srt", "container", "git-agent"} {
		if _, ok := byKind[kind]; !ok {
			t.Errorf("catalog missing %q", kind)
		}
	}
	if got := byKind["git-agent"].Capabilities; len(got) == 0 {
		t.Error("git-agent must advertise its capabilities so the editor can gate the agent picker")
	}
}

// An empty roster must serialize as [] rather than null so the page can iterate
// unconditionally — the same invariant RunGitAgentList guarantees for the CLI.
func TestGitAgentAgentsRouteEmitsAnArrayWhenEmpty(t *testing.T) {
	isolatedConfig(t)

	w := serveSandbox(t, loopbackRequest(http.MethodGet, "/api/captain/sandbox/git-agent/agents", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "[]" {
		t.Errorf("body = %s, want []", got)
	}
}

func TestGitAgentAddRouteReturnsTheJoinHandOff(t *testing.T) {
	isolatedConfig(t)
	gitAgentTokenDB(t)

	w := serveSandbox(t, loopbackRequest(http.MethodPost,
		"/api/captain/sandbox/git-agent/agents", `{"name":"worker-01"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	var result GitAgentAddResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode add result: %v", err)
	}
	if !strings.Contains(result.JoinCommand, "--token ") {
		t.Errorf("join command = %q, want a captain token", result.JoinCommand)
	}
	if result.HostFingerprint == "" || !strings.Contains(result.JoinCommand, result.HostFingerprint) {
		t.Errorf("join command must pin the host key: %+v", result)
	}
	// A7.1: the hand-off carries a token, never key material.
	if strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Error("enrollment response leaked a private key")
	}
	// The public handle identifies the credential in listings and revocations;
	// the secret itself must not cross this boundary.
	if result.TokenID == "" {
		t.Error("the hand-off must name the token so the UI can revoke it")
	}
	if strings.Contains(w.Body.String(), `"token"`) {
		t.Errorf("the raw token crossed the JSON boundary: %s", w.Body)
	}
}

func TestGitAgentAddRouteDryRunLeavesConfigUntouched(t *testing.T) {
	isolatedConfig(t)

	w := serveSandbox(t, loopbackRequest(http.MethodPost,
		"/api/captain/sandbox/git-agent/agents", `{"name":"worker-01","dryRun":true}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	var result GitAgentAddResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode add result: %v", err)
	}
	if !result.DryRun {
		t.Error("result must report that it was a dry run")
	}
	cfg, _, err := captainconfig.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if backend, ok := cfg.Sandbox.Backends["git-agent"]; ok {
		if pending, _ := backend.Options["pending"].(map[string]any); len(pending) > 0 {
			t.Errorf("dry run recorded a pending enrollment: %+v", pending)
		}
	}
}

func TestGitAgentAddRouteRejectsAMissingName(t *testing.T) {
	isolatedConfig(t)

	w := serveSandbox(t, loopbackRequest(http.MethodPost,
		"/api/captain/sandbox/git-agent/agents", `{"name":"  "}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body)
	}
}

// A typo'd key must not be silently ignored: dropping "endpoint" would enroll
// the agent against the backend default instead of the address the caller asked
// for, and the mistake would only surface at first dispatch.
func TestGitAgentAddRouteRejectsUnknownFields(t *testing.T) {
	isolatedConfig(t)

	w := serveSandbox(t, loopbackRequest(http.MethodPost,
		"/api/captain/sandbox/git-agent/agents", `{"name":"worker-01","endpiont":"ssh://x:7422"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body)
	}
}

func TestGitAgentRevokeRouteRemovesAnEnrolledAgent(t *testing.T) {
	isolatedConfig(t)
	if err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		cfg.Sandbox.Backends = map[string]captainconfig.SandboxBackend{
			"git-agent": {Kind: "git-agent", Options: map[string]any{
				"agents": map[string]any{"worker-01": map[string]any{"fingerprint": "SHA256:aaa"}},
			}},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	w := serveSandbox(t, loopbackRequest(http.MethodDelete,
		"/api/captain/sandbox/git-agent/agents/worker-01", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	var result GitAgentRevokeResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode revoke result: %v", err)
	}
	if !result.Revoked || result.Fingerprint != "SHA256:aaa" {
		t.Errorf("revoke result = %+v, want the revoked fingerprint", result)
	}
}

func TestGitAgentRevokeRouteIsNotFoundForAnUnknownAgent(t *testing.T) {
	isolatedConfig(t)

	w := serveSandbox(t, loopbackRequest(http.MethodDelete,
		"/api/captain/sandbox/git-agent/agents/ghost", ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", w.Code, w.Body)
	}
}

// Enrollment mints a credential and revocation rewrites ~/.captain.yaml, so both
// carry the same loopback + same-origin guard as the provider-token routes. A
// page on another origin must not be able to drive them.
func TestSandboxMutatingRoutesRejectRemoteAndCrossOrigin(t *testing.T) {
	isolatedConfig(t)

	cases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"remote client", func(r *http.Request) { r.RemoteAddr = "203.0.113.7:44321" }},
		{"non-loopback host", func(r *http.Request) { r.Host = "captain.example.com" }},
		{"cross origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }},
	}
	for _, tc := range cases {
		t.Run("add/"+tc.name, func(t *testing.T) {
			r := loopbackRequest(http.MethodPost, "/api/captain/sandbox/git-agent/agents", `{"name":"worker-01"}`)
			tc.mutate(r)
			if w := serveSandbox(t, r); w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403; body = %s", w.Code, w.Body)
			}
		})
		t.Run("revoke/"+tc.name, func(t *testing.T) {
			r := loopbackRequest(http.MethodDelete, "/api/captain/sandbox/git-agent/agents/worker-01", "")
			tc.mutate(r)
			if w := serveSandbox(t, r); w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403; body = %s", w.Code, w.Body)
			}
		})
	}

	// The read-only catalog and roster stay reachable: they expose no secrets and
	// the dashboard may be viewed from another host.
	r := loopbackRequest(http.MethodGet, "/api/captain/sandbox/git-agent/agents", "")
	r.RemoteAddr = "203.0.113.7:44321"
	if w := serveSandbox(t, r); w.Code != http.StatusOK {
		t.Errorf("read-only roster status = %d, want 200", w.Code)
	}
}

// taskRouteDB points the default database context at an embedded postgres so
// the history routes have something to read.
func taskRouteDB(t *testing.T) *database.DB {
	t.Helper()
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_sandbox_routes"})
	db, err := database.Open(t.Context(), database.WithDSN(handle.DSN()), database.WithMigrations())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	setCaptainDBForTest(db)
	t.Cleanup(func() {
		setCaptainDBForTest(nil)
		resetCaptainContextsForTest()
		_ = db.Close()
	})
	return db
}

func TestGitAgentTaskRoutesServeHistory(t *testing.T) {
	isolatedConfig(t)
	db := taskRouteDB(t)

	id, err := db.UpsertGitAgentTask(t.Context(), database.UpsertGitAgentTaskInput{
		TaskID: "task-1", Mailbox: "mailboxes/aaa.git", Base: "main",
		DispatchCommit: "deadbeef", Backend: "prod-pool", Agent: "worker-01",
		Status: database.GitAgentTaskRunning,
	})
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if err := db.RecordGitAgentAttempt(t.Context(), database.RecordGitAgentAttemptInput{
		TaskID: id, Attempt: 1, Tier: "supervisor", Status: database.GitAgentVerdictRejected,
		Findings: []map[string]any{{"hook": "verify", "message": "make lint failed"}},
	}); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}

	list := serveSandbox(t, loopbackRequest(http.MethodGet, "/api/captain/sandbox/git-agent/tasks", ""))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body)
	}
	var tasks []database.GitAgentTask
	if err := json.Unmarshal(list.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != "task-1" {
		t.Fatalf("tasks = %+v, want the seeded task", tasks)
	}

	detail := serveSandbox(t, loopbackRequest(http.MethodGet,
		"/api/captain/sandbox/git-agent/tasks/task-1?mailbox=mailboxes/aaa.git", ""))
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", detail.Code, detail.Body)
	}
	var got database.GitAgentTaskDetail
	if err := json.Unmarshal(detail.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if len(got.Attempts) != 1 || got.Attempts[0].Tier != "supervisor" {
		t.Fatalf("attempts = %+v, want the supervisor verdict", got.Attempts)
	}
	if got.Attempts[0].Findings[0]["message"] != "make lint failed" {
		t.Errorf("findings = %+v, want the hook message", got.Attempts[0].Findings)
	}
}

func TestGitAgentTaskRouteFiltersAndMissingTask(t *testing.T) {
	isolatedConfig(t)
	db := taskRouteDB(t)

	for _, spec := range []struct{ task, agent string }{{"task-1", "worker-01"}, {"task-2", "worker-02"}} {
		if _, err := db.UpsertGitAgentTask(t.Context(), database.UpsertGitAgentTaskInput{
			TaskID: spec.task, Mailbox: "mailboxes/aaa.git", Base: "main",
			DispatchCommit: "deadbeef", Agent: spec.agent,
		}); err != nil {
			t.Fatalf("seed %s: %v", spec.task, err)
		}
	}

	filtered := serveSandbox(t, loopbackRequest(http.MethodGet,
		"/api/captain/sandbox/git-agent/tasks?agent=worker-02", ""))
	var tasks []database.GitAgentTask
	if err := json.Unmarshal(filtered.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != "task-2" {
		t.Fatalf("filtered tasks = %+v, want only worker-02's", tasks)
	}

	missing := serveSandbox(t, loopbackRequest(http.MethodGet,
		"/api/captain/sandbox/git-agent/tasks/ghost", ""))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", missing.Code, missing.Body)
	}
}
