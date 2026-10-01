package api

import "time"

// Workspace is the runtime state of a run's working directory — the output
// counterpart to Spec.Setup (the input checkout/worktree config). It records
// where the run executed, the git details, and what it changed / committed /
// planned, and travels on Response so callers see what the run did to the tree.
// It reconciles what used to be scattered across the agent run-context and the
// worktree plugin's result.
type Workspace struct {
	Cwd  string `json:"cwd,omitempty" yaml:"cwd,omitempty"`   // resolved dir (a worktree path when set up)
	Repo string `json:"repo,omitempty" yaml:"repo,omitempty"` // repo root
	// Worktree is the isolated tree the run was moved into; nil means the run
	// worked in the caller's own checkout. Its presence is what later hooks read
	// as isolation.
	Worktree  *WorktreeState `json:"worktree,omitempty" yaml:"worktree,omitempty"`
	Changed   []string       `json:"changed,omitempty" yaml:"changed,omitempty"`     // agent-changed files (repo-relative)
	Commits   []CommitRecord `json:"commits,omitempty" yaml:"commits,omitempty"`     // commits made during the run
	Notices   []Notice       `json:"notices,omitempty" yaml:"notices,omitempty"`     // lifecycle lines hooks reported
	Diff      string         `json:"diff,omitempty" yaml:"diff,omitempty"`           // working diff
	Plan      string         `json:"plan,omitempty" yaml:"plan,omitempty"`           // plan the agent produced (path or content)
	SessionID string         `json:"sessionId,omitempty" yaml:"sessionId,omitempty"` // agent session
	Metadata  map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// WorktreeState is what became of the git worktree a run was isolated in: where
// it was branched from, what setup put on it, where the branch ended, and
// whether teardown removed or kept it. The agent's own work is Setup..Head —
// Base..Setup is the setup snapshot of work-in-progress copied from the source
// checkout, which is not the agent's.
type WorktreeState struct {
	Repo   string `json:"repo,omitempty" yaml:"repo,omitempty"`     // the repository the worktree was added to
	Path   string `json:"path,omitempty" yaml:"path,omitempty"`     // the worktree's directory
	Branch string `json:"branch,omitempty" yaml:"branch,omitempty"` // the branch it has checked out
	Base   string `json:"base,omitempty" yaml:"base,omitempty"`     // sha the branch was created from
	Setup  string `json:"setup,omitempty" yaml:"setup,omitempty"`   // sha after the setup snapshot; == Base when there was none
	Head   string `json:"head,omitempty" yaml:"head,omitempty"`     // branch tip at teardown
	// Kept is set when teardown left the worktree in place, for KeptReason;
	// Dirty lists the uncommitted paths it held at that point.
	Kept       bool     `json:"kept,omitempty" yaml:"kept,omitempty"`
	KeptReason string   `json:"keptReason,omitempty" yaml:"keptReason,omitempty"`
	Dirty      []string `json:"dirty,omitempty" yaml:"dirty,omitempty"`
	// Removed is set when teardown removed the worktree; BranchDeleted when it
	// also deleted a branch that held nothing past Setup.
	Removed       bool `json:"removed,omitempty" yaml:"removed,omitempty"`
	BranchDeleted bool `json:"branchDeleted,omitempty" yaml:"branchDeleted,omitempty"`
}

// WorkspaceRecord is the durable projection of a run's Workspace: where it ran,
// the worktree it was isolated in, and what it committed. The transient detail —
// notices (stored on the transcript), the diff, the plan, metadata — stays off
// it.
type WorkspaceRecord struct {
	Cwd      string         `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	Worktree *WorktreeState `json:"worktree,omitempty" yaml:"worktree,omitempty"`
	Commits  []CommitRecord `json:"commits,omitempty" yaml:"commits,omitempty"`
}

// NewWorkspaceRecord projects w onto its durable record; nil for a run that
// reported no workspace.
func NewWorkspaceRecord(w *Workspace) *WorkspaceRecord {
	if w == nil {
		return nil
	}
	return &WorkspaceRecord{Cwd: w.Cwd, Worktree: w.Worktree, Commits: w.Commits}
}

// CommitRecord is one git commit made during a run — the result, as opposed to
// the Commit policy that decided to make it.
type CommitRecord struct {
	SHA     string `json:"sha,omitempty" yaml:"sha,omitempty"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
}

// AddCommit appends a commit; nil-safe convenience for hooks.
func (w *Workspace) AddCommit(sha, message string) {
	if w == nil {
		return
	}
	w.Commits = append(w.Commits, CommitRecord{SHA: sha, Message: message})
}

// Notice is one thing a lifecycle hook did, reported in the run's own voice —
// "committed abc1234", "nothing to stage". Hooks act between the model's turns,
// where the provider transcript has nothing to say, so without these a run's
// commits, pushes and teardowns are invisible to anyone reading it back.
//
// At is the moment it happened, which is what lets a notice be sorted back into
// its place among the turns it sits between rather than clumping at the end.
type Notice struct {
	At    time.Time `json:"at" yaml:"at"`
	Phase string    `json:"phase,omitempty" yaml:"phase,omitempty"`
	Text  string    `json:"text" yaml:"text"`
	// Kind is the event kind this notice was reported as, so a reader can tell a
	// verify verdict from a commit line without matching on prose. Empty means
	// EventSystem — the generic lifecycle narration most hooks emit.
	Kind EventKind `json:"kind,omitempty" yaml:"kind,omitempty"`
	// Report is the typed verdict a verify notice reports on. Text is that
	// verdict's headline; the tree, the checklist and the counters live here, so a
	// stored transcript carries the same document the live stream did instead of
	// one sentence about it.
	Report *VerifyReport `json:"report,omitempty" yaml:"report,omitempty"`
}

// AddNotice appends a generic lifecycle notice; nil-safe convenience for hooks.
func (w *Workspace) AddNotice(at time.Time, phase, text string) {
	w.AddKindNotice(at, phase, text, EventSystem)
}

// AddKindNotice appends a notice reported under a specific event kind.
func (w *Workspace) AddKindNotice(at time.Time, phase, text string, kind EventKind) {
	w.AddNoticeRecord(Notice{At: at, Phase: phase, Text: text, Kind: kind})
}

// AddNoticeRecord appends a fully-formed notice; nil-safe. It is the way to
// record one that carries a typed report alongside its prose.
func (w *Workspace) AddNoticeRecord(n Notice) {
	if w == nil {
		return
	}
	w.Notices = append(w.Notices, n)
}
