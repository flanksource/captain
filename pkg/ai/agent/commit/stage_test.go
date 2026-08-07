package commit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// TestSharedTreeCommitsOnlyAgentFiles is the isolation invariant: a run that
// shares the caller's checkout may commit what the agent touched and nothing
// else, however dirty the rest of the tree is.
func TestSharedTreeCommitsOnlyAgentFiles(t *testing.T) {
	dir := newRepo(t)
	hc := shared(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: agent work"})

	write(t, dir, "mine.go", "// the user's own uncommitted work\n")
	write(t, dir, "agent.go", "package main\n")
	changed(hc, "agent.go")

	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}

	if got := filesInHead(t, dir, "HEAD"); fmt.Sprint(got) != fmt.Sprint([]string{"agent.go"}) {
		t.Errorf("commit touched %v, want only agent.go", got)
	}
	if status := mustGit(t, dir, "status", "--porcelain"); !strings.Contains(status, "mine.go") {
		t.Errorf("the user's file should still be uncommitted, status:\n%s", status)
	}
}

// TestSharedTreeCommitsFromSubdirectory: a run launched from a subdirectory of
// the repo — a monorepo package, say — records its edits relative to that
// subdirectory, while git reports dirt relative to the repo root whatever the
// cwd. Attribution has to reconcile the two bases; treating them as one
// namespace makes every such run refuse to commit work it demonstrably did.
//
// Both recorded forms are covered because claude.RelativePath emits either,
// depending on how far outside the run's directory the edited file sits.
func TestSharedTreeCommitsFromSubdirectory(t *testing.T) {
	cases := []struct {
		name string
		sub  string
		// record builds the entry recordEvent would have stored for
		// <repo>/packages/agent.go, given the repo root.
		record func(root string) string
	}{
		{
			name:   "one level up, recorded relative",
			sub:    "apps",
			record: func(string) string { return "../packages/agent.go" },
		},
		{
			name: "further away, recorded absolute",
			sub:  filepath.Join("apps", "storybook"),
			record: func(root string) string {
				return filepath.Join(root, "packages", "agent.go")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			if err := os.MkdirAll(filepath.Join(dir, tc.sub), 0o755); err != nil {
				t.Fatalf("create subdirectory: %v", err)
			}
			hc := shared(filepath.Join(dir, tc.sub)) // Repo and Cwd are the subdirectory
			h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: agent work"})

			write(t, dir, "mine.go", "// the user's own uncommitted work\n")
			write(t, dir, "packages/agent.go", "package main\n")
			changed(hc, tc.record(canonicalDir(dir)))

			if err := h.Post(hc, agent.PhaseAgent); err != nil {
				t.Fatalf("agent phase: %v", err)
			}
			if got := filesInHead(t, dir, "HEAD"); fmt.Sprint(got) != fmt.Sprint([]string{"packages/agent.go"}) {
				t.Errorf("commit touched %v, want only packages/agent.go", got)
			}
			if status := mustGit(t, dir, "status", "--porcelain"); !strings.Contains(status, "mine.go") {
				t.Errorf("the user's file should still be uncommitted, status:\n%s", status)
			}
		})
	}
}

// TestRefusalReportsWhatWasRecorded is the test that protects the user's
// working tree: when nothing in a dirty shared checkout can be attributed to
// the run, the only safe move is to fail loudly rather than sweep it all in.
// The two ways of coming up empty need different messages, because they call
// for different fixes. Nothing recorded is that safety refusal working as
// designed; files recorded but none of them dirty here means the run edited
// another tree, and reporting that as "0 recorded" sends the reader hunting for
// a bug in the agent instead.
func TestRefusalReportsWhatWasRecorded(t *testing.T) {
	cases := []struct {
		name    string
		record  []string
		wantErr string
	}{
		{
			name:    "the agent recorded nothing",
			wantErr: "recorded no file edits",
		},
		{
			name:    "the agent edited a different tree",
			record:  []string{"/somewhere/else/agent.go"},
			wantErr: "/somewhere/else/agent.go",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			hc := shared(dir)
			h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: nothing of mine"})

			write(t, dir, "mine.go", "// the user's own uncommitted work\n")
			changed(hc, tc.record...)

			err := h.Post(hc, agent.PhaseAgent)
			if err == nil {
				t.Fatalf("expected an error; instead the tree was committed as %v", subjects(t, dir))
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should mention %q, got: %v", tc.wantErr, err)
			}
			if commitCount(t, dir) != 1 {
				t.Errorf("nothing should have been committed, subjects: %v", subjects(t, dir))
			}
		})
	}
}

// TestIsolatedTreeCommitsEverything: in a worktree the branch is disposable and
// holds no work but the agent's, including files it changed via a shell command
// rather than an edit tool — which is why staging there is not restricted to the
// recorded set.
func TestIsolatedTreeCommitsEverything(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: everything"})

	write(t, dir, "recorded.go", "package main\n")
	write(t, dir, "via-shell.go", "package main\n")
	changed(hc, "recorded.go") // only one was recorded by a tool call

	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}
	if got := filesInHead(t, dir, "HEAD"); len(got) != 2 {
		t.Errorf("commit touched %v, want both files", got)
	}
}

func TestExplicitStageOverridesIsolationDefault(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir) // would default to committing the whole tree
	h := New(api.Commit{On: api.CommitOnAgent, Stage: api.CommitStageChanged, Message: "feat: narrow"})

	write(t, dir, "recorded.go", "package main\n")
	write(t, dir, "stray.go", "package main\n")
	changed(hc, "recorded.go")

	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}
	if got := filesInHead(t, dir, "HEAD"); fmt.Sprint(got) != fmt.Sprint([]string{"recorded.go"}) {
		t.Errorf("commit touched %v, want only the recorded file", got)
	}
}

// TestDeletionsAreCommitted: an agent that removes a file must produce a commit
// that deletes it, not one that silently keeps it. Both removal shapes are
// covered because they reach staging in different states — an agent that shells
// out to `rm` leaves the index entry intact, while one that runs `git rm` has
// already staged the deletion and left nothing on disk for a pathspec to match.
func TestDeletionsAreCommitted(t *testing.T) {
	cases := []struct {
		name   string
		remove func(t *testing.T, dir string)
	}{
		{
			name:   "removed from the worktree",
			remove: func(t *testing.T, dir string) { mustRemove(t, dir, "README.md") },
		},
		{
			name:   "already staged by git rm",
			remove: func(t *testing.T, dir string) { mustGit(t, dir, "rm", "--quiet", "README.md") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			hc := isolated(dir)
			h := New(api.Commit{On: api.CommitOnAgent, Message: "refactor: drop the readme"})

			tc.remove(t, dir)
			if err := h.Post(hc, agent.PhaseAgent); err != nil {
				t.Fatalf("agent phase: %v", err)
			}
			if out := mustGit(t, dir, "show", "--name-status", "--format=", "HEAD"); out != "D\tREADME.md" {
				t.Errorf("commit should record the deletion, got %q", out)
			}
			if !isClean(t, dir) {
				t.Errorf("deletion left uncommitted:\n%s", mustGit(t, dir, "status", "--porcelain"))
			}
		})
	}
}

func TestCheapGatesRejectDangerousFiles(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		body    string
		wantErr string
	}{
		{name: "dotenv", path: ".env", body: "TOKEN=hunter2\n", wantErr: "credentials"},
		{name: "environment-specific dotenv", path: ".env.production", body: "TOKEN=hunter2\n", wantErr: "credentials"},
		{name: "private key", path: "certs/server.pem", body: "-----BEGIN PRIVATE KEY-----\n", wantErr: "credentials"},
		{name: "oversized artifact", path: "dist/bundle.js", body: strings.Repeat("x", DefaultMaxFileSize+1), wantErr: "over"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			hc := isolated(dir)
			h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: oops"})

			write(t, dir, tc.path, tc.body)
			err := h.Post(hc, agent.PhaseAgent)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Post() = %v, want an error containing %q", err, tc.wantErr)
			}
			if commitCount(t, dir) != 1 {
				t.Errorf("nothing should have been committed, subjects: %v", subjects(t, dir))
			}
		})
	}
}

// TestExampleDotenvIsNotTreatedAsSecret: .env.example is documentation and is
// meant to be committed; a gate that blocks it would be unusable.
func TestExampleDotenvIsNotTreatedAsSecret(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "docs: add env example"})

	write(t, dir, ".env.example", "TOKEN=\n")
	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}
	if !contains(filesInHead(t, dir, "HEAD"), ".env.example") {
		t.Errorf("commit should contain .env.example, got %v", filesInHead(t, dir, "HEAD"))
	}
}

func TestGatesNoneCommitsWhatCheapWouldRefuse(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Gates: api.CommitGatesNone, Message: "chore: deliberate"})

	write(t, dir, ".env", "TOKEN=hunter2\n")
	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase with gates: none: %v", err)
	}
	if !contains(filesInHead(t, dir, "HEAD"), ".env") {
		t.Errorf("gates: none should commit it deliberately, got %v", filesInHead(t, dir, "HEAD"))
	}
}

// TestIgnoredFilesAreNeverStaged: the path set comes from `git status`, which
// excludes ignored files, so build output stays out at every gate level.
func TestIgnoredFilesAreNeverStaged(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Gates: api.CommitGatesNone, Message: "feat: build"})

	write(t, dir, ".gitignore", "build/\n")
	write(t, dir, "build/out.bin", "artifact\n")
	write(t, dir, "src.go", "package main\n")

	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}
	if contains(filesInHead(t, dir, "HEAD"), "build/out.bin") {
		t.Errorf("ignored file was committed: %v", filesInHead(t, dir, "HEAD"))
	}
}

// TestFullGatesRequireAHostPipeline: captain has no pre-commit pipeline of its
// own, so asking for one without supplying it must fail rather than quietly
// commit with fewer checks than were requested.
func TestFullGatesRequireAHostPipeline(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Gates: api.CommitGatesFull, Message: "feat: gated"})

	write(t, dir, "x.go", "package main\n")
	err := h.Post(hc, agent.PhaseAgent)
	if err == nil || !strings.Contains(err.Error(), "Hook.Do") {
		t.Fatalf("Post() = %v, want an error pointing at the host callback", err)
	}
}

// TestAnchorAutoRequiresAHostCallback: last-touch routing is gavel's, and
// pretending to honour it would put fixups on the wrong commits. It fails on the
// very first turn rather than after committing work under the wrong policy.
func TestAnchorAutoRequiresAHostCallback(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, Anchor: "auto"})

	write(t, dir, "x.go", "package main\n")
	err := h.Post(hc, agent.PhaseTurn)
	if err == nil || !strings.Contains(err.Error(), "Hook.Do") {
		t.Fatalf("Post() = %v, want an error pointing at the host callback", err)
	}
	if commitCount(t, dir) != 1 {
		t.Errorf("nothing should have been committed, subjects: %v", subjects(t, dir))
	}
}

// TestExplicitAnchorFixesUpOntoAnExistingCommit covers amending pre-existing
// history rather than the run's own first commit.
func TestExplicitAnchorFixesUpOntoAnExistingCommit(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Mode: api.CommitModeFixup, Anchor: "HEAD", Squash: boolPtr(false)})

	write(t, dir, "x.go", "package main\n")
	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}
	if got := subjects(t, dir)[0]; got != "fixup! chore: seed" {
		t.Errorf("subject = %q, want a fixup of the seed commit", got)
	}
}

// TestDoCallbackReceivesAResolvedPlan pins the host seam: everything gavel needs
// is on the Plan, so it never re-derives paths, mode or anchor.
func TestDoCallbackReceivesAResolvedPlan(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)

	var got Plan
	h := New(api.Commit{On: api.CommitOnAgent, Gates: api.CommitGatesFull, Message: "feat: hosted"})
	h.Do = func(_ *agent.HookContext, plan Plan) (string, error) {
		got = plan
		return "0123456789abcdef0123456789abcdef01234567", nil
	}

	write(t, dir, "hosted.go", "package main\n")
	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}

	// Dir is the resolved working-tree root, not whatever the caller passed:
	// Plan.Paths are repo-relative, so the host needs the base they hang off.
	if got.Dir != canonicalDir(dir) || got.Phase != agent.PhaseAgent || got.Subject != "feat: hosted" {
		t.Errorf("plan = %+v, want dir %s with phase/subject resolved", got, canonicalDir(dir))
	}
	if got.Gates != api.CommitGatesFull || got.Stage != api.CommitStageWorktree {
		t.Errorf("plan policy = %+v, want gates full and worktree staging", got)
	}
	if fmt.Sprint(got.Paths) != fmt.Sprint([]string{"hosted.go"}) {
		t.Errorf("plan paths = %v, want [hosted.go]", got.Paths)
	}
	if commitCount(t, dir) != 1 {
		t.Errorf("the host owns the commit; captain should not have cut one: %v", subjects(t, dir))
	}
	if len(hc.Workspace().Commits) != 1 || hc.Workspace().Commits[0].SHA != got.Subject && hc.Workspace().Commits[0].Message != "feat: hosted" {
		t.Errorf("host commit not recorded on the workspace: %+v", hc.Workspace().Commits)
	}
}

// TestStagePathsCallbackOverridesSelection covers the narrow override: a host
// picks the files, the built-in committer still cuts the commit.
func TestStagePathsCallbackOverridesSelection(t *testing.T) {
	dir := newRepo(t)
	hc := shared(dir) // would otherwise refuse: nothing is attributable
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: chosen"})
	h.StagePaths = func(*agent.HookContext) ([]string, error) { return []string{"chosen.go"}, nil }

	write(t, dir, "chosen.go", "package main\n")
	write(t, dir, "ignored.go", "package main\n")

	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}
	if got := filesInHead(t, dir, "HEAD"); fmt.Sprint(got) != fmt.Sprint([]string{"chosen.go"}) {
		t.Errorf("commit touched %v, want only the callback's choice", got)
	}
}

// TestSubjectCallbackWinsOverPolicy covers the API caller supplying a message
// (a todo title, say) without touching any other decision.
func TestSubjectCallbackWinsOverPolicy(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: from the policy"})
	h.Subject = func(*agent.HookContext) (string, error) { return "feat: from the callback", nil }

	write(t, dir, "x.go", "package main\n")
	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}
	if got := subjects(t, dir)[0]; got != "feat: from the callback" {
		t.Errorf("subject = %q, want the callback's", got)
	}
}

func TestHooksForWorkflow(t *testing.T) {
	if got := HooksForWorkflow(nil); got != nil {
		t.Errorf("nil workflow should build no hooks, got %v", got)
	}
	if got := HooksForWorkflow(&api.Workflow{}); got != nil {
		t.Errorf("a workflow with no commit policy should build no hooks, got %v", got)
	}
	hooks := HooksForWorkflow(&api.Workflow{Commits: []api.Commit{{On: api.CommitOnTurn}, {On: api.CommitOnRun}}})
	if len(hooks) != 2 {
		t.Fatalf("built %d hooks, want one per policy", len(hooks))
	}
	first, ok := hooks[0].(*Hook)
	if !ok || first.Phase() != api.CommitOnTurn {
		t.Errorf("hooks[0] = %#v, want the turn policy", hooks[0])
	}
	if _, ok := hooks[0].(agent.Post); !ok {
		t.Errorf("a commit hook must satisfy agent.Post to be dispatched")
	}
}
