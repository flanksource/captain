package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

type InfoOptions struct {
	Path   string `flag:"path" help:"Path to check (defaults to current directory)" short:"p"`
	All    bool   `flag:"all" help:"Include sessions from all projects, not just the current path" short:"a"`
	Claude bool   `flag:"claude" help:"Show only Claude sessions"`
	Codex  bool   `flag:"codex" help:"Show only Codex sessions"`
	Agents bool   `flag:"agents" help:"Count tool calls from nested sub-agents (Task/Agent); --agents=false for the main thread only" default:"true"`
}

// SessionInfo describes a single session's start metadata.
type SessionInfo struct {
	ID              string     `json:"id"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	EndedAt         *time.Time `json:"endedAt,omitempty"`
	Model           string     `json:"model,omitempty"`
	ReasoningEffort string     `json:"reasoningEffort,omitempty"`
	Version         string     `json:"version,omitempty"`
	GitBranch       string     `json:"gitBranch,omitempty"`
	Provider        string     `json:"provider,omitempty"`
	CWD             string     `json:"cwd,omitempty"`
	ToolCalls       int        `json:"toolCalls"`
	Current         bool       `json:"current,omitempty"`
}

// SourceStats summarizes session history for a single agent source (claude or codex).
type SourceStats struct {
	SessionCount   int           `json:"sessionCount"`
	HistoryStart   *time.Time    `json:"historyStart,omitempty"`
	HistoryEnd     *time.Time    `json:"historyEnd,omitempty"`
	TotalToolCalls int           `json:"totalToolCalls"`
	Sessions       []SessionInfo `json:"sessions,omitempty"`
}

func (s SourceStats) IsEmpty() bool {
	return s.SessionCount == 0 && s.TotalToolCalls == 0 && s.HistoryStart == nil
}

// Pretty renders a single line summary for a source's stats.
func (s SourceStats) Pretty() api.Text {
	if s.IsEmpty() {
		return clicky.Text("(no sessions found)", "text-gray-500 italic")
	}
	t := api.Text{}.
		Append(fmt.Sprintf("%d", s.SessionCount), "font-bold").
		Append(" sessions", "text-gray-600").
		Append(" · ", "text-gray-400").
		Append(fmt.Sprintf("%d", s.TotalToolCalls), "font-bold").
		Append(" tool calls", "text-gray-600")
	if s.HistoryStart != nil && s.HistoryEnd != nil {
		t = t.Append(" · ", "text-gray-400").
			Append(api.Human(*s.HistoryStart, "text-gray-500")).
			Append(" → ", "text-gray-400").
			Append(api.Human(*s.HistoryEnd, "text-gray-500"))
	}
	return t
}

// Pretty renders a session start indicator: timestamp, model, version, branch.
// The detected current session shows its full (copyable) ID and a "current" badge;
// others show a short ID.
func (s SessionInfo) Pretty() api.Text {
	t := api.Text{}.Add(icons.Play).Space()
	if s.StartedAt != nil {
		t = t.Append(s.StartedAt.Format("2006-01-02 15:04"), "text-gray-700 font-medium")
	} else {
		t = t.Append("(no timestamp)", "text-gray-500 italic")
	}
	if s.Model != "" {
		t = t.Append("  ", "").
			Add(icons.AI).Space().
			Append(s.Model, "text-purple-600 font-medium")
	} else if s.Provider != "" {
		t = t.Append("  ", "").
			Append(s.Provider, "text-purple-600 font-medium")
	}
	if s.ReasoningEffort != "" {
		t = t.Append(" / ", "text-gray-400").
			Append(s.ReasoningEffort, "text-amber-600")
	}
	if s.Version != "" {
		t = t.Append("  v", "text-gray-500").
			Append(s.Version, "text-gray-600")
	}
	if s.GitBranch != "" {
		t = t.Append("  ", "").
			Add(icons.Git).Space().
			Append(s.GitBranch, "text-green-600")
	}
	if s.ToolCalls > 0 {
		t = t.Append("  ", "").
			Append(fmt.Sprintf("%d calls", s.ToolCalls), "text-gray-500")
	}
	if s.ID != "" {
		id := shortID(s.ID)
		if s.Current {
			id = s.ID
		}
		t = t.Append("  ", "").
			Append(id, "text-gray-400 italic")
	}
	if s.Current {
		t = t.Append("  ", "").
			Append("current", "text-green-600 font-bold")
	}
	return t
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

type InfoResult struct {
	CWD            string                  `json:"cwd"`
	CurrentSession *EnvironmentSessionInfo `json:"currentSession,omitempty"`
	ProjectRoot    string                  `json:"projectRoot"`
	ProjectName    string                  `json:"projectName"`
	MarkerFile     string                  `json:"markerFile"`
	ClaudeDir      string                  `json:"claudeDir,omitempty"`
	CodexDir       string                  `json:"codexDir,omitempty"`
	Claude         SourceStats             `json:"claude,omitempty"`
	Codex          SourceStats             `json:"codex,omitempty"`
	TotalSessions  int                     `json:"totalSessions"`
	TotalToolCalls int                     `json:"totalToolCalls"`

	showClaude bool
	showCodex  bool
}

func (r InfoResult) Pretty() api.Text {
	if r.CurrentSession != nil {
		return r.CurrentSession.Pretty(r.CWD)
	}
	t := api.Text{}.
		Add(icons.Folder).Space().
		Append(displayName(r), "font-bold text-blue-600")
	if r.MarkerFile != "" {
		t = t.Append("  ", "").
			Append(fmt.Sprintf("(%s)", r.MarkerFile), "text-gray-500 italic")
	}

	t = t.NewLine().Add(infoRow("CWD", r.CWD))
	if r.ProjectRoot != "" && r.ProjectRoot != r.CWD {
		t = t.NewLine().Add(infoRow("Root", r.ProjectRoot))
	}

	if r.showClaude {
		t = t.NewLine().NewLine().Add(renderSource(icons.AI, "Claude", r.ClaudeDir, r.Claude))
	}
	if r.showCodex {
		t = t.NewLine().NewLine().
			Add(renderSource(icons.Icon{Unicode: "🤖", Iconify: "mdi:robot", Style: "muted"}, "Codex", r.CodexDir, r.Codex))
	}

	if r.showClaude && r.showCodex {
		t = t.NewLine().NewLine().
			Add(icons.Info).Space().
			Append("Total: ", "text-gray-600").
			Append(fmt.Sprintf("%d sessions", r.TotalSessions), "font-bold").
			Append(" · ", "text-gray-400").
			Append(fmt.Sprintf("%d tool calls", r.TotalToolCalls), "font-bold")
	}

	return t
}

func renderSource(icon api.Textable, name, dir string, stats SourceStats) api.Text {
	t := api.Text{}.
		Add(icon).Space().
		Append(name, "font-bold text-blue-600").
		Append("  ", "").
		Add(stats.Pretty())
	if dir != "" {
		t = t.NewLine().
			Append("    ", "").
			Append(dir, "text-gray-500 italic")
	}
	for _, s := range stats.Sessions {
		t = t.NewLine().Append("    ", "").Add(s.Pretty())
	}
	return t
}

func displayName(r InfoResult) string {
	if r.ProjectName != "" {
		return r.ProjectName
	}
	return filepath.Base(r.CWD)
}

func infoRow(label, value string) api.Text {
	return api.Text{}.
		Append("  "+label+": ", "text-gray-500").
		Append(value, "text-gray-700")
}

func runInfoDiscovery(opts InfoOptions) (InfoResult, error) {
	path := opts.Path
	if path == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			return InfoResult{}, err
		}
	}

	// If neither flag is set, show both. If only one is set, hide the other.
	showClaude := opts.Claude || (!opts.Claude && !opts.Codex)
	showCodex := opts.Codex || (!opts.Claude && !opts.Codex)

	projectInfo := claude.FindProjectInfo(path)
	matchRoot := projectInfo.Root
	if matchRoot == "" {
		matchRoot = path
	}

	result := InfoResult{
		CWD:         path,
		ProjectRoot: projectInfo.Root,
		MarkerFile:  projectInfo.MarkerFile,
		showClaude:  showClaude,
		showCodex:   showCodex,
	}

	if projectInfo.Root != "" {
		result.ProjectName = filepath.Base(projectInfo.Root)
	}

	if showClaude {
		result.Claude = collectClaudeStats(path, opts.All, opts.Agents, &result)
	}
	if showCodex {
		result.Codex = collectCodexStats(matchRoot, opts.All, &result)
	}

	// The most-recent session for the current directory is the detected
	// "current" session. Marking it surfaces its full ID for use with
	// `captain history --session` and `captain changes --session-id`.
	if !opts.All {
		markCurrentSession(result.Claude.Sessions)
		markCurrentSession(result.Codex.Sessions)
	}

	result.TotalSessions = result.Claude.SessionCount + result.Codex.SessionCount
	result.TotalToolCalls = result.Claude.TotalToolCalls + result.Codex.TotalToolCalls

	return result, nil
}
