package api

// Context is the workspace a request runs against. Consolidates the legacy
// agent.RunContext{Cwd,Repo,ChangedFiles} and the worktree plugin's inputs/outputs.
type Context struct {
	// Dir is the working directory the provider runs in. (RunContext.Cwd / ai.Request.Cwd)
	Dir string `json:"dir,omitempty" yaml:"dir,omitempty" pretty:"label=Dir"`
	// Diff is the unified diff of the run's changes. (git.Diff / worktree.Result.Diff)
	Diff string `json:"diff,omitempty" yaml:"diff,omitempty" pretty:"label=Diff"`
	// Files are the repo-relative paths the run changed. (RunContext.ChangedFiles)
	Files []string `json:"files,omitempty" yaml:"files,omitempty" pretty:"label=Files"`
	// Git pins the run to a repository, revision, and optional pull request.
	Git *Git `json:"git,omitempty" yaml:"git,omitempty"`
	// Worktree isolates the run in a dedicated git worktree on a new branch.
	Worktree *Worktree `json:"worktree,omitempty" yaml:"worktree,omitempty"`
}

// Git pins a run to a repository, revision, and optional pull request.
type Git struct {
	Repo string `json:"repo,omitempty" yaml:"repo,omitempty" pretty:"label=Repo"` // repo root (RunContext.Repo)
	SHA  string `json:"sha,omitempty" yaml:"sha,omitempty" pretty:"label=SHA"`    // base/commit ref
	PR   string `json:"pr,omitempty" yaml:"pr,omitempty" pretty:"label=PR"`       // pull-request number or URL
}

// Worktree isolates a run in a dedicated git worktree on a new branch. Branch is
// required to enable isolation; Path/Commit are populated on teardown. Mirrors
// the legacy worktree.Plugin/Result.
type Worktree struct {
	Branch     string `json:"branch,omitempty" yaml:"branch,omitempty" pretty:"label=Branch"`           // new branch name
	Base       string `json:"base,omitempty" yaml:"base,omitempty" pretty:"label=Base"`                 // base ref; HEAD when empty
	CommitMsg  string `json:"commitMsg,omitempty" yaml:"commitMsg,omitempty" pretty:"label=Commit Msg"` // commit-all message on teardown
	KeepOnExit bool   `json:"keepOnExit,omitempty" yaml:"keepOnExit,omitempty" pretty:"label=Keep"`     // keep worktree+branch for inspection
	Path       string `json:"path,omitempty" yaml:"path,omitempty" pretty:"label=Path"`                 // output: filesystem path
	Commit     string `json:"commit,omitempty" yaml:"commit,omitempty" pretty:"label=Commit"`           // output: commit SHA
}
