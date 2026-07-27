package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
	captaindb "github.com/flanksource/captain/pkg/database"
	captainsession "github.com/flanksource/captain/pkg/session"
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
	SessionID  string `json:"sessionId" pretty:"label=Session"`
	PlanID     string `json:"planId,omitempty" pretty:"label=Plan ID"`
	RevisionID string `json:"revisionId,omitempty"`
	Revision   int    `json:"revision,omitempty"`
	Source     string `json:"source,omitempty" pretty:"label=Source"`
	Path       string `json:"path,omitempty" pretty:"label=Plan"`
	OnDisk     bool   `json:"onDisk" pretty:"label=On Disk"`
	Slug       string `json:"slug,omitempty" pretty:"label=Slug"`
	Content    string `json:"content,omitempty"`

	pathOnly bool
}

func RunPlan(opts PlanOptions) (PlanResult, error) {
	ctx := context.Background()
	source, err := normalizeSessionSource(opts.Source)
	if err != nil {
		return PlanResult{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return PlanResult{}, err
	}
	db, err := freshenSessionDB(ctx)
	if err != nil {
		return PlanResult{}, err
	}

	id := strings.TrimSpace(opts.SessionID)
	if id != "" {
		persisted, ok, err := resolveNativePlan(ctx, db, id, source)
		if err != nil {
			return PlanResult{}, err
		}
		if ok {
			persisted.pathOnly = opts.PathOnly
			return *persisted, nil
		}
		overview, err := db.GetSessionOverviewByIdentity(ctx, id)
		if err != nil {
			return PlanResult{}, err
		}
		candidate := candidateFromOverview(*overview)
		if candidate.path == "" {
			return PlanResult{}, fmt.Errorf("session %q has no transcript recorded on this host", id)
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

	_, projectRoot, _ := resolveSessionScope(cwd, opts.All, "")
	plan, err := resolveLatestTranscriptPlan(ctx, db, latestTranscriptPlanQuery{
		Source: source, ProjectRoot: projectRoot,
	})
	if err != nil {
		return PlanResult{}, err
	}
	if plan == nil {
		return PlanResult{}, fmt.Errorf("no session with a plan found in %s", scopeLabel(cwd, opts.All))
	}
	plan.pathOnly = opts.PathOnly
	return *plan, nil
}

type latestTranscriptPlanQuery struct {
	Source      string
	ProjectRoot string
}

const latestTranscriptPlanPageLimit = 100

func resolveLatestTranscriptPlan(
	ctx context.Context,
	db sessionListStore,
	query latestTranscriptPlanQuery,
) (*PlanResult, error) {
	filter := captaindb.SessionListFilter{
		ProjectRoot: query.ProjectRoot,
		RootsOnly:   true,
		Limit:       latestTranscriptPlanPageLimit,
	}
	if query.Source != "all" {
		filter.Source = query.Source
	}
	seen := map[string]struct{}{}
	for {
		page, err := db.ListSessionSummaries(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("list Captain sessions while resolving latest plan: %w", err)
		}
		for i := range page.Rows {
			candidate := candidateFromOverview(overviewFromSummary(page.Rows[i]))
			if candidate.path == "" {
				continue
			}
			plan, err := resolveSessionPlan(candidate)
			if err == nil && plan != nil {
				return plan, nil
			}
		}
		if page.NextCursor == "" {
			return nil, nil
		}
		if _, ok := seen[page.NextCursor]; ok {
			return nil, fmt.Errorf("captain plan search pagination repeated cursor %q", page.NextCursor)
		}
		seen[page.NextCursor] = struct{}{}
		filter.Cursor = page.NextCursor
	}
}

// resolveNativePlan resolves persisted plan content without consulting the
// transcript or source plan path. Approved content wins; otherwise the latest
// immutable revision of the newest plan variant is returned.
func resolveNativePlan(ctx context.Context, db *captaindb.DB, identity, source string) (*PlanResult, bool, error) {
	sourceFilter := source
	if sourceFilter == "all" {
		sourceFilter = ""
	}
	session, err := db.GetSessionByIdentity(ctx, identity, sourceFilter, "", "")
	if err != nil {
		if errors.Is(err, captaindb.ErrSessionNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("resolve persisted Captain session %q: %w", identity, err)
	}
	plans, err := db.ListPlans(ctx, captaindb.PlanFilter{SourceSessionID: &session.ID})
	if err != nil {
		return nil, false, fmt.Errorf("list persisted plans for session %s: %w", session.ID, err)
	}
	var selected *captaindb.Plan
	for i := range plans {
		if plans[i].ApprovedRevision != nil {
			selected = &plans[i]
			break
		}
		if selected == nil && plans[i].LatestRevision != nil {
			selected = &plans[i]
		}
	}
	if selected == nil {
		return nil, false, nil
	}
	revision := selected.ApprovedRevision
	if revision == nil {
		revision = selected.LatestRevision
	}
	onDisk := false
	if selected.Path != "" {
		_, err := os.Stat(selected.Path)
		onDisk = err == nil
	}
	return &PlanResult{
		SessionID:  session.ID.String(),
		PlanID:     selected.ID.String(),
		RevisionID: revision.ID.String(),
		Revision:   revision.Revision,
		Source:     session.Source,
		Path:       selected.Path,
		OnDisk:     onDisk,
		Slug:       selected.Slug,
		Content:    revision.PlanMarkdown,
	}, true, nil
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
	plan := captainsession.CodexPlanFromToolUses(uses)
	if plan == nil || strings.TrimSpace(plan.Content) == "" {
		return nil, nil
	}
	return &PlanResult{
		SessionID: candidate.record.ID,
		Source:    "codex",
		Content:   plan.Content,
	}, nil
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
