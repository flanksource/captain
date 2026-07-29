package worktree

import (
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

func TestWorktreeMerge_Validate(t *testing.T) {
	for _, ok := range AllWorktreeMerges() {
		if err := ok.Validate(); err != nil {
			t.Errorf("merge policy %q should be valid: %v", ok, err)
		}
	}
	if err := WorktreeMerge("").Validate(); err != nil {
		t.Errorf("empty merge policy should be valid (defaults to never): %v", err)
	}
	if err := WorktreeMerge("sometimes").Validate(); err == nil {
		t.Error("unknown merge policy should fail validation")
	}
}

func TestWorktreeMerge_ShouldMerge(t *testing.T) {
	cases := []struct {
		policy        WorktreeMerge
		failed        bool
		verified      bool
		wantShouldRun bool
	}{
		{WorktreeMergeAlways, true, false, true},
		{WorktreeMergeAlways, false, true, true},
		{WorktreeMergeNever, false, true, false},
		{"", false, true, false},
		{WorktreeMergeOnSuccess, false, false, true},
		{WorktreeMergeOnSuccess, true, true, false},
		{WorktreeMergeOnVerify, false, true, true},
		{WorktreeMergeOnVerify, false, false, false},
	}
	for _, c := range cases {
		if got := c.policy.shouldMerge(c.failed, c.verified); got != c.wantShouldRun {
			t.Errorf("%q.shouldMerge(failed=%v, verified=%v) = %v, want %v", c.policy, c.failed, c.verified, got, c.wantShouldRun)
		}
	}
}

func TestWorktreeCleanup_Validate(t *testing.T) {
	for _, ok := range AllWorktreeCleanups() {
		if err := ok.Validate(); err != nil {
			t.Errorf("cleanup policy %q should be valid: %v", ok, err)
		}
	}
	if err := WorktreeCleanup("").Validate(); err != nil {
		t.Errorf("empty cleanup policy should be valid (defaults to always): %v", err)
	}
	if err := WorktreeCleanup("sometimes").Validate(); err == nil {
		t.Error("unknown cleanup policy should fail validation")
	}
}

func TestWorktreeCleanup_ShouldCleanup(t *testing.T) {
	cases := []struct {
		policy        WorktreeCleanup
		merged        bool
		verified      bool
		wantShouldRun bool
	}{
		{WorktreeCleanupAlways, false, false, true},
		{"", false, false, true},
		{WorktreeCleanupNever, true, true, false},
		{WorktreeCleanupOnMerge, true, false, true},
		{WorktreeCleanupOnMerge, false, true, false},
		{WorktreeCleanupOnVerify, false, true, true},
		{WorktreeCleanupOnVerify, true, false, false},
	}
	for _, c := range cases {
		if got := c.policy.shouldCleanup(c.merged, c.verified); got != c.wantShouldRun {
			t.Errorf("%q.shouldCleanup(merged=%v, verified=%v) = %v, want %v", c.policy, c.merged, c.verified, got, c.wantShouldRun)
		}
	}
}

// TestPlugin_PostNoopWithoutPreRun checks the Post-called-without-PreRun guard,
// which must not invoke `wt` at all (so it passes even when `wt` isn't
// installed). A workspace still standing on the caller's own cwd — not on the
// plugin's branch — is how Post recognises that no worktree was created.
func TestPlugin_PostNoopWithoutPreRun(t *testing.T) {
	p := &Plugin{Branch: "unused"}
	hc := &agent.HookContext{Response: &ai.Response{Workspace: &api.Workspace{Cwd: t.TempDir()}}}
	if err := p.Post(hc, agent.PhaseRun); err != nil {
		t.Errorf("Post without a PreRun'd worktree should be a no-op, got: %v", err)
	}
}

// TestPlugin_TearsDownAtRunPhase pins the teardown to the last phase: a hook
// that commits the isolated worktree runs at PhaseAgent and must still find the
// tree there.
func TestPlugin_TearsDownAtRunPhase(t *testing.T) {
	got := (&Plugin{Branch: "unused"}).Phases()
	if len(got) != 1 || got[0] != agent.PhaseRun {
		t.Errorf("worktree teardown must be run-phase only, got %v", got)
	}
}
