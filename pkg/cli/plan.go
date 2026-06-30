package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

type PlanOptions struct {
	SessionID string `flag:"session-id" args:"true" help:"Session ID (exact or prefix) to resolve the plan for; defaults to the most recent session with a plan in the current directory" short:"s"`
	Source    string `flag:"source" help:"Restrict source: all, claude, codex" default:"all"`
	All       bool   `flag:"all" help:"Search all projects, not just the current directory" short:"a"`
	PathOnly  bool   `flag:"path" help:"Print only the plan file path"`
}

func (PlanOptions) GetName() string { return "plan [session-id]" }

// PlanResult is the exit-plan-mode plan for a single session.
type PlanResult struct {
	SessionID string `json:"sessionId" pretty:"label=Session"`
	Source    string `json:"source,omitempty" pretty:"label=Source"`
	Path      string `json:"path,omitempty" pretty:"label=Plan"`
	OnDisk    bool   `json:"onDisk" pretty:"label=On Disk"`
	Slug      string `json:"slug,omitempty" pretty:"label=Slug"`
	Content   string `json:"content,omitempty"`

	pathOnly bool
}

func RunPlan(opts PlanOptions) (PlanResult, error) {
	source, err := normalizeSessionSource(opts.Source)
	if err != nil {
		return PlanResult{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return PlanResult{}, err
	}

	id := strings.TrimSpace(opts.SessionID)
	if id != "" {
		// A session id can name a session in any project, so search everywhere.
		candidates, err := discoverSessionCandidates("", true, source)
		if err != nil {
			return PlanResult{}, err
		}
		candidate, ok := matchPlanCandidate(candidates, id)
		if !ok {
			return PlanResult{}, fmt.Errorf("session %q not found", id)
		}
		plan, err := resolveSessionPlan(candidate)
		if err != nil {
			return PlanResult{}, err
		}
		if plan == nil {
			return PlanResult{}, fmt.Errorf("session %q has no plan", id)
		}
		plan.pathOnly = opts.PathOnly
		return *plan, nil
	}

	candidates, err := discoverSessionCandidates(cwd, opts.All, source)
	if err != nil {
		return PlanResult{}, err
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return sessionSortTime(candidates[i].record).After(sessionSortTime(candidates[j].record))
	})
	for _, candidate := range candidates {
		plan, err := resolveSessionPlan(candidate)
		if err != nil || plan == nil {
			continue
		}
		plan.pathOnly = opts.PathOnly
		return *plan, nil
	}
	return PlanResult{}, fmt.Errorf("no session with a plan found in %s", scopeLabel(cwd, opts.All))
}

// matchPlanCandidate finds the candidate whose record key or id matches id
// exactly, or whose id has id as a prefix (so the short ids printed elsewhere work).
func matchPlanCandidate(candidates []sessionCandidate, id string) (sessionCandidate, bool) {
	for _, c := range candidates {
		if c.record.Key == id || c.record.ID == id || (c.record.ID != "" && strings.HasPrefix(c.record.ID, id)) {
			return c, true
		}
	}
	return sessionCandidate{}, false
}

// resolveSessionPlan reads a session transcript and recovers its plan. It returns
// nil (no error) when the session has no plan.
func resolveSessionPlan(candidate sessionCandidate) (*PlanResult, error) {
	switch candidate.record.Source {
	case "claude":
		return resolveClaudePlan(candidate)
	case "codex":
		return resolveCodexPlan(candidate)
	default:
		return nil, fmt.Errorf("unknown session source %q", candidate.record.Source)
	}
}

func resolveClaudePlan(candidate sessionCandidate) (*PlanResult, error) {
	entries, err := claude.ReadHistoryFile(candidate.path)
	if err != nil {
		return nil, err
	}
	sp := claude.PlanFromEntries(entries)
	if sp == nil {
		return nil, nil
	}

	content := sp.Content
	onDisk := false
	if data, err := os.ReadFile(sp.Path); err == nil {
		onDisk = true
		// The on-disk file is canonical: the agent may revise it after exiting
		// plan mode, so it supersedes the inline copy captured in the transcript.
		if s := string(data); strings.TrimSpace(s) != "" {
			content = s
		}
	}

	// A slug alone (no ExitPlanMode call, write, or plan-mode attachment) is just
	// the session title — only treat it as a plan if the file actually exists.
	if !sp.Explicit && !onDisk {
		return nil, nil
	}

	return &PlanResult{
		SessionID: candidate.record.ID,
		Source:    "claude",
		Path:      sp.Path,
		OnDisk:    onDisk,
		Slug:      sp.Slug,
		Content:   content,
	}, nil
}

func resolveCodexPlan(candidate sessionCandidate) (*PlanResult, error) {
	uses, err := history.ExtractCodexToolUses(candidate.path)
	if err != nil {
		return nil, err
	}
	content := latestCodexPlan(uses)
	if content == "" {
		return nil, nil
	}
	return &PlanResult{
		SessionID: candidate.record.ID,
		Source:    "codex",
		Content:   content,
	}, nil
}

// latestCodexPlan renders the most recent Codex update_plan checklist as markdown.
// Codex keeps its plan inline (no file), revising it via update_plan; the last
// revision is the final plan.
func latestCodexPlan(uses []history.ToolUse) string {
	var steps []any
	for _, use := range uses {
		if use.Tool != "TodoWrite" {
			continue
		}
		if todos, ok := use.Input["todos"].([]any); ok && len(todos) > 0 {
			steps = todos
		}
	}
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		text, _ := step["step"].(string)
		if text == "" {
			text, _ = step["content"].(string)
		}
		status, _ := step["status"].(string)
		mark := " "
		suffix := ""
		switch status {
		case "completed", "done":
			mark = "x"
		case "in_progress":
			suffix = " _(in progress)_"
		}
		fmt.Fprintf(&b, "- [%s] %s%s\n", mark, text, suffix)
	}
	return strings.TrimRight(b.String(), "\n")
}

// shortenHomePath rewrites a leading home directory to ~ for display.
func shortenHomePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func (r PlanResult) Pretty() api.Text {
	if r.pathOnly {
		return clicky.Text(r.Path, "")
	}

	text := clicky.Text("").Add(icons.Icon{Unicode: "📋", Iconify: "codicon:checklist", Style: "muted"}).Space().
		Append(r.SessionID, "text-gray-700 font-medium")
	if r.Source != "" {
		text = text.Append("  ", "").Append(r.Source, "text-purple-600")
	}
	if r.Path != "" {
		text = text.NewLine().Append("Plan: ", "font-bold").Append(shortenHomePath(r.Path), "text-cyan-400")
		if r.OnDisk {
			text = text.Append(" ✓ on disk", "text-green-500")
		} else {
			text = text.Append(" (not on disk)", "text-gray-500")
		}
	} else {
		text = text.NewLine().Append("Plan: ", "font-bold").Append("(inline)", "text-gray-500")
	}
	if r.Content != "" {
		text = text.NewLine().Add(clicky.CodeBlock("markdown", r.Content))
	}
	return text
}
