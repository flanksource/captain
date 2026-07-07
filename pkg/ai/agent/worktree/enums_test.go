package worktree

import "testing"

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

// TestPlugin_PostRunNoopWithoutPreRun checks the PostRun-called-without-PreRun
// guard, which must not invoke `wt` at all (so it passes even when `wt` isn't
// installed).
func TestPlugin_PostRunNoopWithoutPreRun(t *testing.T) {
	p := &Plugin{Branch: "unused"}
	if err := p.PostRun(nil); err != nil {
		t.Errorf("PostRun without a PreRun'd worktree should be a no-op, got: %v", err)
	}
}
