package gitagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
)

func TestHookExecEnv(t *testing.T) {
	got := HookExecEnv([]string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"LANG=C.UTF-8",
		"LC_ALL=C",
		"GIT_DIR=.",
		"GIT_QUARANTINE_PATH=/q",
		"ANTHROPIC_API_KEY=sk-secret",
		"OPENAI_API_KEY=sk-secret",
		"AWS_SECRET_ACCESS_KEY=secret",
		"CAPTAIN_TASK=t-1",
	})
	want := []string{"PATH=/usr/bin", "HOME=/home/u", "LANG=C.UTF-8", "LC_ALL=C"}
	if len(got) != len(want) {
		t.Fatalf("HookExecEnv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("HookExecEnv = %v, want %v", got, want)
		}
	}
}

func TestResolveHookWrap_EmptyMeansNone(t *testing.T) {
	factory, err := ResolveHookWrap("", t.TempDir())
	if err != nil || factory != nil {
		t.Fatalf("ResolveHookWrap(\"\") = %v, %v; want nil factory, nil error", factory, err)
	}
}

func TestResolveHookWrap_UnknownKindFailsAtResolveTime(t *testing.T) {
	if _, err := ResolveHookWrap("no-such-sandbox", t.TempDir()); err == nil {
		t.Fatal("unknown hook sandbox kind must fail at resolve time")
	}
}

// vetTreeFixture builds a repo with two commits and returns their OIDs, so
// vetTree has a real from→to range to materialize.
func vetTreeFixture(t *testing.T) (repo, from, to string) {
	t.Helper()
	ctx := context.Background()
	repo = t.TempDir()
	env := os.Environ()
	mustGit := func(args ...string) string {
		out, err := runGit(ctx, repo, env, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(out)
	}
	mustGit("init", "-q")
	mustGit("config", "user.name", "t")
	mustGit("config", "user.email", "t@example.com")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit("add", "-A")
	mustGit("commit", "-q", "-m", "one")
	from = mustGit("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit("add", "-A")
	mustGit("commit", "-q", "-m", "two")
	to = mustGit("rev-parse", "HEAD")
	return repo, from, to
}

// A hook-sandbox factory that cannot confine the workspace must fail the vet
// closed: status error, and error rejects (R5.2/R7.5).
func TestVetTreeFailsClosedWhenHookSandboxFails(t *testing.T) {
	repo, from, to := vetTreeFixture(t)
	host := HookHost{WrapFor: func(context.Context, string) (verify.CommandWrapFunc, func() error, error) {
		return nil, nil, errors.New("no confinement available")
	}}
	verdict := vetTree(context.Background(), repo, vetRequest{
		host:     host,
		workflow: &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}},
		tier:     "sidecar", task: "t-1", attempt: 1,
		from: from, to: to,
	})
	if verdict.Status != StatusError || !verdict.Rejects() {
		t.Fatalf("verdict = %+v, want a rejecting error status", verdict)
	}
	if len(verdict.Findings) == 0 || !strings.Contains(verdict.Findings[0].Message, "no confinement available") {
		t.Fatalf("findings = %+v, want the sandbox failure surfaced", verdict.Findings)
	}
}

// The wrapper must be constructed against the materialized tree — the hook
// command observing the pushed files proves the confinement target and the
// execution directory agree — and the factory's close must run.
func TestVetTreeBuildsTheWrapperForTheMaterializedTree(t *testing.T) {
	repo, from, to := vetTreeFixture(t)
	var confined string
	closed := false
	host := HookHost{WrapFor: func(_ context.Context, dir string) (verify.CommandWrapFunc, func() error, error) {
		confined = dir
		wrap := func(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
			return cmd, args, env, nil
		}
		return wrap, func() error { closed = true; return nil }, nil
	}}
	verdict := vetTree(context.Background(), repo, vetRequest{
		host:     host,
		workflow: &api.Workflow{Verify: &api.Verify{Commands: []string{"test -f a.txt && test -f b.txt"}}},
		tier:     "sidecar", task: "t-1", attempt: 1,
		from: from, to: to,
	})
	if verdict.Status != StatusAccepted {
		t.Fatalf("verdict = %+v, want accepted", verdict)
	}
	if confined == "" {
		t.Fatal("the factory never saw a workspace directory")
	}
	if !closed {
		t.Fatal("the factory's close must run after the hook set")
	}
}

// Prompt-only workflows must not construct a sandbox at all: there is no exec
// hook to confine, and a hook sandbox failure would reject pushes that run no
// commands.
func TestVetTreeSkipsTheSandboxWithoutExecHooks(t *testing.T) {
	repo, from, to := vetTreeFixture(t)
	host := HookHost{WrapFor: func(context.Context, string) (verify.CommandWrapFunc, func() error, error) {
		t.Fatal("no sandbox may be constructed for a workflow without exec hooks")
		return nil, nil, nil
	}}
	verdict := vetTree(context.Background(), repo, vetRequest{
		host:     host,
		workflow: &api.Workflow{},
		tier:     "sidecar", task: "t-1", attempt: 1,
		from: from, to: to,
	})
	if verdict.Status != StatusAccepted {
		t.Fatalf("verdict = %+v, want accepted", verdict)
	}
}
