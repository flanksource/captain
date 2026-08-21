package cli

import (
	"context"
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
		plan, err := resolveIdentityPlan(ctx, db, id, source)
		if err != nil {
			return PlanResult{}, err
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
	plans, _ := db.(planStore)
	seen := map[string]struct{}{}
	for {
		page, err := db.ListSessionSummaries(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("list Captain sessions while resolving latest plan: %w", err)
		}
		for i := range page.Rows {
			overview := overviewFromSummary(page.Rows[i])
			if plans != nil {
				plan, ok, err := resolveNativePlan(ctx, plans, overview)
				if err != nil {
					return nil, err
				}
				if ok {
					return plan, nil
				}
			}
			candidate := candidateFromOverview(overview)
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

// planStore reads persisted plan revisions independently of transcript storage.
type planStore interface {
	ListPlans(context.Context, captaindb.PlanFilter) ([]captaindb.Plan, error)
}

// planIdentityStore resolves an identity to every matching session overview and
// reads the plans recorded against a Captain session UUID.
type planIdentityStore interface {
	sessionOverviewStore
	planStore
}

// resolveIdentityPlan resolves a Captain UUID or provider session ID to a plan.
// The identity lookup is plural because the same provider session ID may exist
// once per source (captain_sessions is unique on source+host+provider id): a
// gavel orchestration row carries no transcript while the provider row it
// parents carries the real one. Persisted plans win over transcript recovery,
// and transcript recovery uses the first match that actually has a transcript.
func resolveIdentityPlan(ctx context.Context, db planIdentityStore, identity, source string) (*PlanResult, error) {
	overviews, err := resolveOverviewsByIdentity(ctx, db, identity)
	if err != nil {
		return nil, err
	}
	if source != "all" {
		filtered := make([]captaindb.SessionOverview, 0, len(overviews))
		for i := range overviews {
			if overviews[i].Source == source {
				filtered = append(filtered, overviews[i])
			}
		}
		overviews = filtered
	}
	if len(overviews) == 0 {
		return nil, fmt.Errorf("%w: %s", captaindb.ErrSessionNotFound, identity)
	}
	for i := range overviews {
		persisted, ok, err := resolveNativePlan(ctx, db, overviews[i])
		if err != nil {
			return nil, err
		}
		if ok {
			return persisted, nil
		}
	}
	for i := range overviews {
		candidate := candidateFromOverview(overviews[i])
		if candidate.path == "" {
			continue
		}
		plan, err := resolveSessionPlan(candidate)
		if err != nil {
			return nil, err
		}
		if plan != nil {
			return plan, nil
		}
		return nil, fmt.Errorf("session %q has no plan", identity)
	}
	return nil, fmt.Errorf("session %q has no transcript recorded on this host", identity)
}

// resolveNativePlan resolves persisted plan content for one Captain session
// without consulting the transcript or source plan path. Approved content wins;
// otherwise the latest immutable revision of the newest plan variant is returned.
func resolveNativePlan(
	ctx context.Context,
	db planStore,
	session captaindb.SessionOverview,
) (*PlanResult, bool, error) {
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
