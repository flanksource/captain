package commit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/workspace"
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

// TestIgnoredRecordedEditsAreNotARefusal: an agent whose every edit landed in a
// directory the repo ignores has nothing to commit, and that is an outcome, not a
// refusal. Counting those paths toward attribution used to fail the run over the
// caller's unrelated dirty file — a scratch-writing agent could not coexist with
// any uncommitted work at all.
func TestIgnoredRecordedEditsAreNotARefusal(t *testing.T) {
	dir := newRepo(t)
	hc := shared(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: scratch only"})

	write(t, dir, ".gitignore", ".tmp/\n")
	mustGit(t, dir, "add", ".gitignore")
	mustGit(t, dir, "commit", "-m", "chore: ignore scratch")
	before := commitCount(t, dir)

	write(t, dir, ".tmp/status.json", "{}\n")
	write(t, dir, "mine.go", "// the caller's own uncommitted work\n")
	changed(hc, ".tmp/status.json")

	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("a run that only wrote ignored files should be a no-op: %v", err)
	}
	if got := commitCount(t, dir); got != before {
		t.Errorf("cut %d commit(s), want none; subjects: %v", got-before, subjects(t, dir))
	}
	if isClean(t, dir) {
		t.Error("the caller's mine.go was swept into a commit; it must still be dirty")
	}
}

// TestRefusalNamesOnlyCommittableRecordedEdits: when the refusal is still right,
// the paths it names have to be ones the reader can act on. Listing the ignored
// scratch files alongside them — or instead of them, once elide caps the list —
// points the investigation at files that were never candidates.
func TestRefusalNamesOnlyCommittableRecordedEdits(t *testing.T) {
	dir := newRepo(t)
	hc := shared(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: nothing dirty of mine"})

	write(t, dir, ".gitignore", ".tmp/\n")
	write(t, dir, "src/real.go", "package src\n")
	mustGit(t, dir, "add", ".gitignore", "src/real.go")
	mustGit(t, dir, "commit", "-m", "chore: seed src")

	write(t, dir, ".tmp/scratch.json", "{}\n")
	write(t, dir, "mine.go", "// the caller's own uncommitted work\n")
	changed(hc, ".tmp/scratch.json", "src/real.go")

	err := h.Post(hc, agent.PhaseAgent)
	if err == nil {
		t.Fatalf("expected a refusal; instead the tree was committed as %v", subjects(t, dir))
	}
	if !strings.Contains(err.Error(), "src/real.go") {
		t.Errorf("error should name the committable recorded edit, got: %v", err)
	}
	if strings.Contains(err.Error(), ".tmp/scratch.json") {
		t.Errorf("error should not name an ignored path that was never a candidate, got: %v", err)
	}
}

// TestTrackedButIgnoredEditsAreSkippedNotFatal covers the awkward middle case a
// force-added build bundle creates: it is tracked, so `git status` reports it
// dirty, but `git add` still refuses it for matching an ignore rule. Attributing
// it therefore does not produce a commit — it kills the run while staging. It is
// dropped instead, and a sibling edit in the same turn still gets committed.
func TestTrackedButIgnoredEditsAreSkippedNotFatal(t *testing.T) {
	dir := newRepo(t)
	hc := shared(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: rebuild"})

	write(t, dir, ".gitignore", "dist/\n")
	write(t, dir, "dist/bundle.js", "// v1\n")
	write(t, dir, "src/app.go", "package src\n")
	mustGit(t, dir, "add", ".gitignore", "src/app.go")
	mustGit(t, dir, "add", "-f", "dist/bundle.js")
	mustGit(t, dir, "commit", "-m", "chore: seed bundle")

	write(t, dir, "dist/bundle.js", "// v2\n")
	write(t, dir, "src/app.go", "package src // rebuilt\n")
	changed(hc, "dist/bundle.js", "src/app.go")

	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}
	head := filesInHead(t, dir, "HEAD")
	if !contains(head, "src/app.go") {
		t.Errorf("the stageable edit should have been committed, HEAD holds: %v", head)
	}
	if contains(head, "dist/bundle.js") {
		t.Errorf("git add refuses an ignored path, so it must not be in the commit: %v", head)
	}
}

// TestOnlyTrackedIgnoredEditIsANoOp: when the ignored bundle is the *only* thing
// the turn touched there is nothing left to commit, and that has to be an outcome
// rather than a `git add` failure that takes the whole run down.
func TestOnlyTrackedIgnoredEditIsANoOp(t *testing.T) {
	dir := newRepo(t)
	hc := shared(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "chore: rebuild"})

	write(t, dir, ".gitignore", "dist/\n")
	write(t, dir, "dist/bundle.js", "// v1\n")
	mustGit(t, dir, "add", ".gitignore")
	mustGit(t, dir, "add", "-f", "dist/bundle.js")
	mustGit(t, dir, "commit", "-m", "chore: seed bundle")
	before := commitCount(t, dir)

	write(t, dir, "dist/bundle.js", "// v2\n")
	changed(hc, "dist/bundle.js")

	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("a turn that only touched an unstageable path should be a no-op: %v", err)
	}
	if got := commitCount(t, dir); got != before {
		t.Errorf("cut %d commit(s), want none; subjects: %v", got-before, subjects(t, dir))
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

// TestConfiguredGatesRejectDangerousFiles: the policy's gate specs resolve
// through the workspace registry and refuse before anything is committed.
func TestConfiguredGatesRejectDangerousFiles(t *testing.T) {
	const maxBytes = 1 << 10
	gates := []workspace.GateSpec{
		{Name: workspace.SecretNamesGateName},
		{Name: workspace.MaxSizeGateName, Config: map[string]any{"bytes": maxBytes}},
	}
	cases := []struct {
		name, path, body, wantGate string
	}{
		{name: "dotenv", path: ".env", body: "TOKEN=hunter2\n", wantGate: workspace.SecretNamesGateName},
		{name: "private key", path: "certs/server.pem", body: "-----BEGIN PRIVATE KEY-----\n", wantGate: workspace.SecretNamesGateName},
		{name: "oversized artifact", path: "dist/bundle.js", body: strings.Repeat("x", maxBytes+1), wantGate: workspace.MaxSizeGateName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			hc := isolated(dir)
			h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: oops", Gates: gates})

			write(t, dir, tc.path, tc.body)
			var gateErr *workspace.GateError
			if err := h.Post(hc, agent.PhaseAgent); !errors.As(err, &gateErr) || gateErr.Gate != tc.wantGate {
				t.Fatalf("Post() = %v, want the %s gate to refuse %s", err, tc.wantGate, tc.path)
			}
			if commitCount(t, dir) != 1 {
				t.Errorf("nothing should have been committed, subjects: %v", subjects(t, dir))
			}
		})
	}
}

// TestUnconfiguredGatesCommitASecretNamedFile: no gate runs unless the policy
// lists it, so a deliberate .env commit needs no opt-out.
func TestUnconfiguredGatesCommitASecretNamedFile(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "chore: deliberate"})

	write(t, dir, ".env", "TOKEN=hunter2\n")
	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase with no gates: %v", err)
	}
	if !contains(filesInHead(t, dir, "HEAD"), ".env") {
		t.Errorf("an ungated policy should commit it, got %v", filesInHead(t, dir, "HEAD"))
	}
}

// TestUnknownGateFailsTheCommit: a misspelled gate must fail loudly rather than
// commit with fewer checks than the policy asked for.
func TestUnknownGateFailsTheCommit(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: x", Gates: []workspace.GateSpec{{Name: "secret-name"}}})

	write(t, dir, "x.go", "package main\n")
	if err := h.Post(hc, agent.PhaseAgent); err == nil || !strings.Contains(err.Error(), `unknown gate "secret-name"`) {
		t.Fatalf("Post() = %v, want an unknown-gate error", err)
	}
	if commitCount(t, dir) != 1 {
		t.Errorf("nothing should have been committed, subjects: %v", subjects(t, dir))
	}
}

// TestIgnoredFilesAreNeverStaged: the path set comes from `git status`, which
// excludes ignored files, so build output stays out at every gate level.
func TestIgnoredFilesAreNeverStaged(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: build"})

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

// TestAnchorAutoIsRefused: captain does not implement last-touch routing, and
// pretending to honour it would put fixups on the wrong commits. It fails on the
// very first turn rather than after committing work under the wrong policy.
func TestAnchorAutoIsRefused(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)
	h := New(api.Commit{On: api.CommitOnTurn, Anchor: "auto"})

	write(t, dir, "x.go", "package main\n")
	err := h.Post(hc, agent.PhaseTurn)
	if err == nil || !strings.Contains(err.Error(), "anchor: auto") {
		t.Fatalf("Post() = %v, want an error refusing anchor: auto", err)
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

// TestHostExtensionsShapeTheCommit pins the host seam gavel builds on: Message
// gets the resolved Plan, a host hook's rewrite is restaged and committed, and a
// host gate sees the final staged set.
func TestHostExtensionsShapeTheCommit(t *testing.T) {
	dir := newRepo(t)
	hc := isolated(dir)

	var got Plan
	var gated []string
	const hostMessage, formatted = "feat: composed by the host", "package main // formatted\n"
	h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: hosted"})
	h.HostMessage = func(_ *agent.HookContext, plan Plan) (string, error) {
		got = plan
		return hostMessage, nil
	}
	h.HostHooks = []workspace.Hook{testHook(func(ws *workspace.Workspace, paths []string) error {
		return os.WriteFile(filepath.Join(ws.Dir, paths[0]), []byte(formatted), 0o600)
	})}
	h.HostGates = []workspace.Gate{testGate(func(c workspace.Check) error {
		gated = c.Paths
		return nil
	})}

	write(t, dir, "hosted.go", "package main\n")
	if err := h.Post(hc, agent.PhaseAgent); err != nil {
		t.Fatalf("agent phase: %v", err)
	}

	// Dir is the resolved working-tree root, not whatever the caller passed:
	// Plan.Paths are repo-relative, so the host needs the base they hang off.
	want := Plan{Dir: canonicalDir(dir), Phase: agent.PhaseAgent, Mode: api.CommitModeCommit,
		Subject: "feat: hosted", Stage: api.CommitStageWorktree, Paths: []string{"hosted.go"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("plan = %+v, want %+v", got, want)
	}
	if fmt.Sprint(gated) != fmt.Sprint([]string{"hosted.go"}) {
		t.Errorf("host gate saw %v, want [hosted.go]", gated)
	}
	if body := mustGit(t, dir, "show", "HEAD:hosted.go"); body+"\n" != formatted {
		t.Errorf("committed %q, want the host hook's rewrite %q", body, formatted)
	}
	if log := subjects(t, dir); fmt.Sprint(log) != fmt.Sprint([]string{hostMessage, "chore: seed"}) {
		t.Errorf("subjects = %v, want the host's message on top of the seed", log)
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
