package api

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
	Commits   []Commit       `json:"commits,omitempty" yaml:"commits,omitempty"`     // commits made during the run
	Diff      string         `json:"diff,omitempty" yaml:"diff,omitempty"`           // working diff
	Plan      string         `json:"plan,omitempty" yaml:"plan,omitempty"`           // plan the agent produced (path or content)
	SessionID string         `json:"sessionId,omitempty" yaml:"sessionId,omitempty"` // agent session
	Metadata  map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// Commit is one git commit made during a run.
type Commit struct {
	SHA     string `json:"sha,omitempty" yaml:"sha,omitempty"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
}

// AddCommit appends a commit; nil-safe convenience for hooks.
func (w *Workspace) AddCommit(sha, message string) {
	if w == nil {
		return
	}
	w.Commits = append(w.Commits, Commit{SHA: sha, Message: message})
}
