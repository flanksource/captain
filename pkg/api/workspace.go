package api

import "time"

// Workspace is the runtime state of a run's working directory — the output
// counterpart to Spec.Setup (the input checkout/worktree config). It records
// where the run executed, the git details, and what it changed / committed /
// planned, and travels on Response so callers see what the run did to the tree.
// It reconciles what used to be scattered across the agent run-context and the
// worktree plugin's result.
type Workspace struct {
	Cwd       string         `json:"cwd,omitempty" yaml:"cwd,omitempty"`             // resolved dir (a worktree path when set up)
	Repo      string         `json:"repo,omitempty" yaml:"repo,omitempty"`           // repo root
	Branch    string         `json:"branch,omitempty" yaml:"branch,omitempty"`       // worktree branch
	Base      string         `json:"base,omitempty" yaml:"base,omitempty"`           // worktree base ref
	Changed   []string       `json:"changed,omitempty" yaml:"changed,omitempty"`     // agent-changed files (repo-relative)
	Commits   []CommitRecord `json:"commits,omitempty" yaml:"commits,omitempty"`     // commits made during the run
	Notices   []Notice       `json:"notices,omitempty" yaml:"notices,omitempty"`     // lifecycle lines hooks reported
	Diff      string         `json:"diff,omitempty" yaml:"diff,omitempty"`           // working diff
	Plan      string         `json:"plan,omitempty" yaml:"plan,omitempty"`           // plan the agent produced (path or content)
	SessionID string         `json:"sessionId,omitempty" yaml:"sessionId,omitempty"` // agent session
	Metadata  map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
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
