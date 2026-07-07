package cli

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/worktree"
)

func pluginNames(hooks []any) []string {
	out := make([]string, len(hooks))
	for i, h := range hooks {
		if n, ok := h.(interface{ Name() string }); ok {
			out[i] = n.Name()
		}
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
			} else if !strings.Contains(err.Error(), agent.ScopeList()) {
				t.Errorf("scopeFromFlag(%q) err = %v, want mention of valid scopes %q", in, err, agent.ScopeList())
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
	if wt.Merge != worktree.WorktreeMergeOnSuccess {
		t.Errorf("Merge = %q, want %q", wt.Merge, worktree.WorktreeMergeOnSuccess)
	}
	if wt.Cleanup != worktree.WorktreeCleanupOnMerge {
		t.Errorf("Cleanup = %q, want %q", wt.Cleanup, worktree.WorktreeCleanupOnMerge)
	}
	if names := pluginNames(plugins); len(names) != 1 || names[0] != "worktree" {
		t.Errorf("plugins = %v, want [worktree]", names)
	}
}

func TestBuildAgentPlugins_WorktreeWithoutCommit(t *testing.T) {
	opts := AIAgentOptions{Worktree: true}
	_, wt, err := buildAgentPlugins(opts, nil)
	if err != nil {
		t.Fatalf("buildAgentPlugins: %v", err)
	}
	if wt.Merge != "" {
		t.Errorf("Merge = %q, want the zero value (never merge without --commit)", wt.Merge)
	}
	if wt.Cleanup != worktree.WorktreeCleanupOnVerify {
		t.Errorf("Cleanup = %q, want %q", wt.Cleanup, worktree.WorktreeCleanupOnVerify)
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

func TestVerifyPassed(t *testing.T) {
	if !verifyPassed(nil) {
		t.Error("no verdicts should pass")
	}
	if !verifyPassed([]agent.VerifyResult{{Valid: false}, {Valid: true}}) {
		t.Error("last verdict valid should pass")
	}
	if verifyPassed([]agent.VerifyResult{{Valid: true}, {Valid: false}}) {
		t.Error("last verdict not valid should fail")
	}
}
