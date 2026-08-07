package commit

import (
	"fmt"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// turn simulates one loop iteration: the agent edits a file, then the runner
// dispatches PhaseTurn.
func turn(t *testing.T, h *Hook, hc *agent.HookContext, dir, rel, body string) {
	t.Helper()
	write(t, dir, rel, body)
	changed(hc, rel)
	if err := h.Post(hc, agent.PhaseTurn); err != nil {
		t.Fatalf("turn commit for %s: %v", rel, err)
	}
}

// finish simulates the runner closing out a run: the agent sweep, then the run
// phase where the chain collapses.
func finish(t *testing.T, h *Hook, hc *agent.HookContext) error {
	t.Helper()
	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		return err
	}
	return h.Post(hc, agent.PhaseRun)
}

func TestHookPhasesByPolicy(t *testing.T) {
	cases := []struct {
		name   string
		commit api.Commit
		want   []agent.Phase
	}{
		{
			// A per-turn policy needs the agent phase to sweep a turn that errored
			// before dispatch, and the run phase to collapse the chain.
			name:   "per-turn fixup takes all three",
			commit: api.Commit{On: api.CommitOnTurn},
			want:   []agent.Phase{agent.PhaseTurn, agent.PhaseAgent, agent.PhaseRun},
		},
		{
			name:   "per-turn without squash needs no run phase",
			commit: api.Commit{On: api.CommitOnTurn, Squash: boolPtr(false)},
			want:   []agent.Phase{agent.PhaseTurn, agent.PhaseAgent},
		},
		{
			name:   "agent policy takes only the agent phase",
			commit: api.Commit{On: api.CommitOnAgent},
			want:   []agent.Phase{agent.PhaseAgent},
		},
		{
			name:   "run policy takes only the run phase",
			commit: api.Commit{On: api.CommitOnRun},
			want:   []agent.Phase{agent.PhaseRun},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := New(tc.commit).Phases()
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("Phases() = %v, want %v", got, tc.want)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// TestPerTurnChainSquashesToOneCommit is the headline behaviour: every turn is
// durable while the run is in flight, and what leaves the run is a single
// reviewable commit.
func TestPerTurnChainSquashesToOneCommit(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, Message: "feat: add greeting"})

	turn(t, h, hc, dir, "greet.go", "package main\n")
	if got := commitCount(t, dir); got != 2 {
		t.Fatalf("after turn 1: %d commits, want 2 (seed + anchor)", got)
	}
	turn(t, h, hc, dir, "greet.go", "package main\n\nfunc Greet() {}\n")
	turn(t, h, hc, dir, "greet_test.go", "package main\n")

	// Mid-run the chain is visible, which is what makes an interrupted run
	// recoverable.
	if got := commitCount(t, dir); got != 4 {
		t.Fatalf("mid-run: %d commits, want 4 (seed + anchor + 2 fixups)", got)
	}
	if got := subjects(t, dir)[0]; !strings.HasPrefix(got, "fixup! ") {
		t.Fatalf("mid-run HEAD subject = %q, want a fixup!", got)
	}

	if err := finish(t, h, hc); err != nil {
		t.Fatalf("finish: %v", err)
	}
	got := subjects(t, dir)
	if len(got) != 2 || got[0] != "feat: add greeting" {
		t.Fatalf("after squash: subjects = %v, want [feat: add greeting, chore: seed]", got)
	}
	if !isClean(t, dir) {
		t.Errorf("tree should be clean after committing every turn:\n%s", mustGit(t, dir, "status", "--porcelain"))
	}
	head := filesInHead(t, dir, "HEAD")
	if len(head) != 2 {
		t.Errorf("squashed commit touched %v, want both greet.go and greet_test.go", head)
	}
}

// TestSquashDisabledKeepsChain covers the reviewer-facing option: turn-by-turn
// history is preserved verbatim.
func TestSquashDisabledKeepsChain(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, Squash: boolPtr(false), Message: "feat: staged work"})

	turn(t, h, hc, dir, "a.go", "package a\n")
	turn(t, h, hc, dir, "b.go", "package b\n")
	if err := finish(t, h, hc); err != nil {
		t.Fatalf("finish: %v", err)
	}

	got := subjects(t, dir)
	want := []string{"fixup! feat: staged work", "feat: staged work", "chore: seed"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("subjects = %v, want %v", got, want)
	}
}

// TestWorkSurvivesFailedRun is the defect this design exists to fix: a run that
// fails verification used to leave nothing behind.
func TestWorkSurvivesFailedRun(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, Message: "fix: attempt"})

	turn(t, h, hc, dir, "fix.go", "package main\n")
	hc.Failed = true
	hc.Verified = false
	if err := finish(t, h, hc); err != nil {
		t.Fatalf("finish after failure: %v", err)
	}

	if got := subjects(t, dir); len(got) != 2 || got[0] != "fix: attempt" {
		t.Errorf("failed run left %v, want the turn's work committed", got)
	}
}

// TestAgentPhaseSweepsWorkFromAnErroredTurn covers the one turn the runner
// cannot dispatch: RunUntil returns on an iteration error without calling
// BuildRequest again, so PhaseAgent is what makes that work durable.
func TestAgentPhaseSweepsWorkFromAnErroredTurn(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, Message: "feat: partial"})

	turn(t, h, hc, dir, "one.go", "package main\n")
	// The next turn wrote a file and then the provider errored — no PhaseTurn.
	write(t, dir, "two.go", "package main\n")
	changed(hc, "two.go")
	hc.Failed = true

	if err := finish(t, h, hc); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if !isClean(t, dir) {
		t.Errorf("agent-phase sweep left work uncommitted:\n%s", mustGit(t, dir, "status", "--porcelain"))
	}
	if !contains(filesInHead(t, dir, "HEAD"), "two.go") {
		t.Errorf("swept commit should contain two.go, got %v", filesInHead(t, dir, "HEAD"))
	}
}

// TestFailedTurnCommitIsReportedOnce is the counterpart to the sweep above: the
// turn phase resolved the paths and failed on them, so the sweep has nothing new
// to try. Letting it run again re-derives the identical error, and the runner
// joins that copy onto the one already propagating from the turn — the caller
// then prints the same failure twice, once as `turn hook "commit:turn"` and once
// as `agent hook "commit:turn"`.
func TestFailedTurnCommitIsReportedOnce(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, Message: "fix: attempt"})
	attempts := 0
	h.Do = func(*agent.HookContext, Plan) (string, error) {
		attempts++
		return "", fmt.Errorf("pre-commit gate rejected the change set")
	}

	write(t, dir, "fix.go", "package main\n")
	changed(hc, "fix.go")
	turnErr := h.Post(hc, agent.PhaseTurn)
	if turnErr == nil {
		t.Fatal("turn phase should surface the commit failure")
	}
	hc.Failed = true

	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Errorf("agent sweep repeated the turn's failure: %v", err)
	}
	if attempts != 1 {
		t.Errorf("commit attempted %d times, want 1 — the sweep must not retry a failure", attempts)
	}
}

// TestCommitsAreNarratedOnTheWorkspace covers the other half of the silence
// problem: hooks act between turns, where the provider transcript has nothing to
// say, so a run's commits used to leave no trace a reader could follow.
func TestCommitsAreNarratedOnTheWorkspace(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, Message: "feat: add greeting"})

	turn(t, h, hc, dir, "one.go", "package main\n")
	turn(t, h, hc, dir, "two.go", "package main\n")
	if err := finish(t, h, hc); err != nil {
		t.Fatalf("finish: %v", err)
	}

	var got []string
	for _, notice := range hc.Workspace().Notices {
		got = append(got, notice.Text)
		if notice.At.IsZero() {
			t.Errorf("notice %q has no timestamp; it cannot be sorted back among the turns", notice.Text)
		}
	}
	// The anchor turn, its fixup, and the squash that collapses them — the whole
	// shape of a per-turn policy, readable from the notices alone.
	want := []string{
		"[post-turn] committing 1 file(s)",
		"[post-turn] committed",
		"[post-turn] committing 1 file(s)",
		"[post-turn] committed",
		"[post-run] squashed 1 fixup(s) into ",
	}
	if len(got) != len(want) {
		t.Fatalf("notices = %v, want %d entries shaped like %v", got, len(want), want)
	}
	for i, prefix := range want {
		if !strings.HasPrefix(got[i], prefix) {
			t.Errorf("notice %d = %q, want prefix %q", i, got[i], prefix)
		}
	}
}

// TestAgentSweepStillRunsAfterAnUnrelatedTurnError guards the narrowing above:
// only a *commit* failure disarms the sweep. A turn that failed for any other
// reason never reached the hook, and its work must still be made durable.
func TestAgentSweepStillRunsAfterAnUnrelatedTurnError(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, Message: "feat: partial"})

	// The provider errored mid-turn: the file landed, PhaseTurn never dispatched.
	write(t, dir, "one.go", "package main\n")
	changed(hc, "one.go")
	hc.Failed = true

	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent sweep: %v", err)
	}
	if !isClean(t, dir) {
		t.Errorf("agent-phase sweep left work uncommitted:\n%s", mustGit(t, dir, "status", "--porcelain"))
	}
}

func TestOutcomeGates(t *testing.T) {
	cases := []struct {
		name       string
		when       api.CommitWhen
		failed     bool
		verified   bool
		wantCommit bool
	}{
		{name: "always commits after a failure", when: api.CommitWhenAlways, failed: true, wantCommit: true},
		{name: "onSuccess skips a failed run", when: api.CommitWhenSuccess, failed: true, wantCommit: false},
		{name: "onSuccess commits a clean run", when: api.CommitWhenSuccess, verified: true, wantCommit: true},
		{name: "onVerify skips an unverified run", when: api.CommitWhenVerify, verified: false, wantCommit: false},
		{name: "onVerify commits a verified run", when: api.CommitWhenVerify, verified: true, wantCommit: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			hc := isolated(dir)
			hc.Failed, hc.Verified = tc.failed, tc.verified
			h := New(api.Commit{On: api.CommitOnAgent, When: tc.when, Message: "feat: gated"})

			write(t, dir, "gated.go", "package main\n")
			changed(hc, "gated.go")
			if err := h.Post(hc, agent.PhaseAgent); err != nil {
				t.Fatalf("agent phase: %v", err)
			}

			committed := commitCount(t, dir) == 2
			if committed != tc.wantCommit {
				t.Errorf("committed = %v, want %v (subjects: %v)", committed, tc.wantCommit, subjects(t, dir))
			}
		})
	}
}

// TestCleanTreeCommitsNothing: a turn that only read files is a normal outcome,
// not a failure, and must not produce an empty commit.
func TestCleanTreeCommitsNothing(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn})

	if err := h.Post(hc, agent.PhaseTurn); err != nil {
		t.Fatalf("turn on a clean tree: %v", err)
	}
	if err := finish(t, h, hc); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got := commitCount(t, dir); got != 1 {
		t.Errorf("%d commits, want just the seed — a read-only run commits nothing", got)
	}
	if len(hc.Workspace().Commits) != 0 {
		t.Errorf("workspace recorded %v, want no commits", hc.Workspace().Commits)
	}
}

// TestRootCommitAnchorSquashes covers the repo with no history: the anchor has
// no parent, so autosquash has to rebase --root rather than <anchor>^.
func TestRootCommitAnchorSquashes(t *testing.T) {
	dir := newRepoWithoutCommits(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, Message: "feat: from nothing"})

	turn(t, h, hc, dir, "a.go", "package a\n")
	turn(t, h, hc, dir, "b.go", "package b\n")
	if err := finish(t, h, hc); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if got := subjects(t, dir); len(got) != 1 || got[0] != "feat: from nothing" {
		t.Errorf("subjects = %v, want a single root commit", got)
	}
}

// TestConflictingAutosquashAbortsAndKeepsTheChain: a rebase can only conflict
// when something landed on the branch between the anchor and its fixups — a
// concurrent commit, or a run sharing a branch. Reordering the fixup back onto
// the anchor then replays that commit against a tree it was not written for.
// Whatever git does with the conflict, the run must not hand back a repository
// stuck mid-rebase, and the turn-by-turn work must still be reachable.
func TestConflictingAutosquashAbortsAndKeepsTheChain(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, Message: "feat: rewrite the config"})

	turn(t, h, hc, dir, "config.yaml", "mode: anchor\n")
	// Someone else commits over the same line while the run is in flight.
	write(t, dir, "config.yaml", "mode: intervening\n")
	mustGit(t, dir, "add", "config.yaml")
	mustGit(t, dir, "commit", "-m", "chore: unrelated edit")
	turn(t, h, hc, dir, "config.yaml", "mode: fixup\n")

	err := finish(t, h, hc)
	if err == nil {
		t.Fatal("finish: want a loud conflict error, got nil — a silently skipped squash hides the fixup chain")
	}
	if !strings.Contains(err.Error(), "conflicted") {
		t.Errorf("error = %v, want it to name the conflict", err)
	}
	if rebaseInProgress(t, dir) {
		t.Fatal("the repository was handed back mid-rebase")
	}
	if got := subjects(t, dir); len(got) != 4 || !strings.HasPrefix(got[0], "fixup! ") {
		t.Errorf("subjects = %v, want the aborted chain intact with the fixup on top", got)
	}
	if !isClean(t, dir) {
		t.Errorf("aborted autosquash left the tree dirty:\n%s", mustGit(t, dir, "status", "--porcelain"))
	}
}

// TestDerivedSubjectIsConventional pins the fallback message: derived from the
// prompt, never from a model call that could fail mid-run.
func TestDerivedSubjectIsConventional(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent})

	write(t, dir, "x.go", "package main\n")
	changed(hc, "x.go")
	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}

	want := "chore(agent): " + testPrompt
	if got := subjects(t, dir)[0]; got != want {
		t.Errorf("subject = %q, want %q", got, want)
	}
}

func TestSubjectTruncatedToGitWidth(t *testing.T) {
	hc := isolated(t.TempDir())
	hc.Request.Prompt.User = strings.Repeat("very long instruction ", 20)
	if got := deriveSubject(hc.Request); len(got) > maxSubject {
		t.Errorf("subject is %d chars (%q), want <= %d", len(got), got, maxSubject)
	}
}

// TestDryRunWritesNothing: a dry run reports intent and leaves the repo alone.
func TestDryRunWritesNothing(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, DryRun: true})

	turn(t, h, hc, dir, "a.go", "package a\n")
	if err := finish(t, h, hc); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got := commitCount(t, dir); got != 1 {
		t.Errorf("dry run created %d commits, want 0 beyond the seed", got-1)
	}
	if isClean(t, dir) {
		t.Errorf("dry run should have left the work uncommitted in the tree")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
