package commit

import (
	"fmt"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// TestDoTakesOverTheCommit pins the interim gavel seam: Do receives the plan,
// including the policy's declared gates, cuts the commit itself, and the hook
// records what it cut from git.
func TestDoTakesOverTheCommit(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: host", Gates: api.CommitGatesFull})

	var got Plan
	h.Do = func(_ *agent.HookContext, plan Plan) (string, error) {
		got = plan
		mustGit(t, plan.Dir, append([]string{"add", "--"}, plan.Paths...)...)
		mustGit(t, plan.Dir, "commit", "-q", "-m", "feat: cut by the host")
		return mustGit(t, plan.Dir, "rev-parse", "HEAD"), nil
	}

	write(t, dir, "host.go", "package main\n")
	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}
	if fmt.Sprint(got.Paths) != "[host.go]" || !api.HasGate(got.Gates, api.FullPipelineGate) {
		t.Errorf("plan = %+v, want host.go with the full directive", got)
	}
	if log := subjects(t, dir); fmt.Sprint(log) != fmt.Sprint([]string{"feat: cut by the host", "chore: seed"}) {
		t.Errorf("subjects = %v, want the host's commit on top of the seed", log)
	}
}

// TestFullGatesWithoutDoFail: the full directive names a host pipeline captain
// does not have, so it must refuse rather than commit with no checks.
func TestFullGatesWithoutDoFail(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: x", Gates: api.CommitGatesFull})

	write(t, dir, "x.go", "package main\n")
	if err := h.Post(hc, agent.PhaseAgent); err == nil || !strings.Contains(err.Error(), "gates: full") {
		t.Fatalf("Post() = %v, want the gates: full error", err)
	}
	if commitCount(t, dir) != 1 {
		t.Errorf("nothing should have been committed, subjects: %v", subjects(t, dir))
	}
}
