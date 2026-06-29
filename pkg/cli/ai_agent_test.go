package cli

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai/agent"
)

func pluginNames(ps []agent.Plugin) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return out
}

func TestScopeFromFlag(t *testing.T) {
	cases := map[string]struct {
		want    agent.Scope
		wantErr bool
	}{
		"":        {agent.ScopeAll, false},
		"all":     {agent.ScopeAll, false},
		"changed": {agent.ScopeChanged, false},
		"bogus":   {"", true},
	}
	for in, exp := range cases {
		got, err := scopeFromFlag(in)
		if exp.wantErr {
			if err == nil {
				t.Errorf("scopeFromFlag(%q) err = nil, want error", in)
			}
			continue
		}
		if err != nil || got != exp.want {
			t.Errorf("scopeFromFlag(%q) = %q, %v; want %q, nil", in, got, err, exp.want)
		}
	}
}

func TestBuildAgentPlugins_CommitRequiresWorktree(t *testing.T) {
	_, _, err := buildAgentPlugins(AIAgentOptions{Commit: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("err = %v, want a --commit-requires-worktree error", err)
	}
}

func TestBuildAgentPlugins_WorktreeBranchAndCommit(t *testing.T) {
	opts := AIAgentOptions{
		Worktree: true,
		Commit:   true,
	}
	opts.Prompt = "fix the failing lint\nsecond line ignored"
	plugins, wt, err := buildAgentPlugins(opts, nil)
	if err != nil {
		t.Fatalf("buildAgentPlugins: %v", err)
	}
	if wt == nil {
		t.Fatal("worktree plugin not returned")
	}
	if !strings.HasPrefix(wt.Branch, "captain/agent-") {
		t.Errorf("default branch = %q, want captain/agent-<ts> prefix", wt.Branch)
	}
	if wt.CommitMsg != "captain: fix the failing lint" {
		t.Errorf("CommitMsg = %q, want one-line prompt subject", wt.CommitMsg)
	}
	if names := pluginNames(plugins); len(names) != 1 || names[0] != "worktree" {
		t.Errorf("plugins = %v, want [worktree]", names)
	}
}

func TestBuildAgentPlugins_VerifyAndJudge(t *testing.T) {
	opts := AIAgentOptions{
		Verify: []string{"make lint", "  ", "go test ./..."},
		Judge:  "the change must include a test",
	}
	plugins, wt, err := buildAgentPlugins(opts, nil)
	if err != nil {
		t.Fatalf("buildAgentPlugins: %v", err)
	}
	if wt != nil {
		t.Errorf("worktree plugin = %v, want nil without --worktree", wt)
	}
	want := []string{"verify:make lint", "verify:go test ./...", "judge"}
	got := pluginNames(plugins)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("plugin names = %v, want %v (blank --verify entries skipped)", got, want)
	}
}

func TestVerdictsPassed(t *testing.T) {
	if !verdictsPassed(nil, nil) {
		t.Error("no verdicts + no error should pass")
	}
	if verdictsPassed(nil, errSentinel) {
		t.Error("no verdicts + error should fail")
	}
	if !verdictsPassed([]agent.Verdict{{OK: false}, {OK: true}}, nil) {
		t.Error("last verdict OK should pass")
	}
	if verdictsPassed([]agent.Verdict{{OK: true}, {OK: false}}, nil) {
		t.Error("last verdict not OK should fail")
	}
}

func TestCommitSubject(t *testing.T) {
	if got := commitSubject("  one line  \nrest"); got != "one line" {
		t.Errorf("commitSubject = %q, want %q", got, "one line")
	}
	long := strings.Repeat("x", 100)
	if got := commitSubject(long); len(got) != 72 {
		t.Errorf("commitSubject len = %d, want 72 (capped)", len(got))
	}
}

var errSentinel = errSentinelType("boom")

type errSentinelType string

func (e errSentinelType) Error() string { return string(e) }
