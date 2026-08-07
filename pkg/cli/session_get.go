package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
	rpchttp "github.com/flanksource/clicky/rpc/http"
)

type SessionGetResult struct {
	RootSessionID string           `json:"rootSessionId,omitempty"`
	Sessions      []SessionGetItem `json:"sessions"`
	Total         int              `json:"total"`
}

type SessionGetItem struct {
	CaptainID          string            `json:"captainId"`
	ParentSessionID    string            `json:"parentSessionId,omitempty"`
	RootSessionID      string            `json:"rootSessionId,omitempty"`
	ProviderSessionID  string            `json:"providerSessionId,omitempty"`
	Host               string            `json:"host,omitempty"`
	Aggregate          bool              `json:"aggregate,omitempty"`
	DetailAvailable    bool              `json:"detailAvailable"`
	Summary            SessionRecord     `json:"summary"`
	Detail             *session.Session  `json:"detail,omitempty"`
	ActiveRunID string            `json:"activeRunId,omitempty"`
	Chat        *ChatCapabilities `json:"chat,omitempty"`
	ChatState   *ChatStateFrame   `json:"chatState,omitempty"`
	notice      transcriptNotice
}

type sessionGetStore interface {
	sessionOverviewStore
	ListPromptRuns(context.Context, database.PromptRunFilter) ([]database.PromptRun, error)
}

// RunSessionGet returns every Captain session matching an exact Captain UUID
// or provider-session-id prefix. Transcript-less matches remain visible via
// their overview metadata; recorded transcripts and native prompt runs are
// projected into the same detail model and paged.
func RunSessionGet(ctx context.Context, opts SessionGetOptions) (SessionGetResult, error) {
	if strings.TrimSpace(opts.ID) == "" {
		return SessionGetResult{}, fmt.Errorf("id is required")
	}
	stopDatabase := rpchttp.Track(ctx, "database")
	db, err := captainDB(ctx)
	stopDatabase()
	if err != nil {
		return SessionGetResult{}, err
	}
	return runSessionGet(ctx, db, opts)
}

func runSessionGet(ctx context.Context, db sessionGetStore, opts SessionGetOptions) (SessionGetResult, error) {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		return SessionGetResult{}, fmt.Errorf("id is required")
	}
	stopLookup := rpchttp.Track(ctx, "lookup")
	overviews, err := resolveOverviewsByIdentity(ctx, db, id)
	stopLookup()
	if err != nil {
		return SessionGetResult{}, err
	}

	items := make([]SessionGetItem, 0, len(overviews))
	for i := range overviews {
		stopHydrate := rpchttp.Track(ctx, "hydrate")
		item, itemErr := buildSessionGetItem(ctx, db, overviews[i], opts)
		stopHydrate()
		if itemErr != nil {
			return SessionGetResult{}, itemErr
		}
		items = append(items, item)
	}
	rootID := ""
	if len(items) > 1 && items[0].ParentSessionID == "" {
		rootID = items[0].CaptainID
	}
	return SessionGetResult{RootSessionID: rootID, Sessions: items, Total: len(items)}, nil
}

func buildSessionGetItem(ctx context.Context, db sessionGetStore, overview database.SessionOverview, opts SessionGetOptions) (SessionGetItem, error) {
	item := SessionGetItem{
		CaptainID: overview.ID.String(), ProviderSessionID: stringOr(overview.ProviderSessionID, ""),
		Host: overview.HostID, Aggregate: stringOr(overview.AgentType, "") == "batch",
		Summary: recordFromOverview(overview),
	}
	if overview.ParentSessionID != nil {
		item.ParentSessionID = overview.ParentSessionID.String()
	}
	if overview.RootSessionID != nil {
		item.RootSessionID = overview.RootSessionID.String()
	}
	capabilities := sessionChatCapabilities(item.Summary)
	item.Chat = &capabilities
	active, ok := promptChats.getRun(item.CaptainID)
	if !ok && item.ProviderSessionID != "" {
		active, ok = promptChats.getSession(item.ProviderSessionID)
	}
	if ok {
		var activeCapabilities ChatCapabilities
		item.ActiveRunID, activeCapabilities, item.ChatState = active.projection()
		item.Chat = &activeCapabilities
	}
	detail, err := loadSessionDetail(ctx, db, overview)
	if err != nil {
		return SessionGetItem{}, err
	}
	if detail == nil {
		return item, nil
	}
	enrichSessionDetail(detail, item.Summary)
	item.DetailAvailable = true
	item.Summary.DetailAvailable = true
	item.Summary.Messages = max(item.Summary.Messages, len(detail.Messages))
	item.Summary.Provider = firstNonEmpty(item.Summary.Provider, detail.Provider)
	item.Summary.Backend = firstNonEmpty(item.Summary.Backend, detail.Backend)
	item.Summary.Model = firstNonEmpty(item.Summary.Model, detail.Model)
	item.Summary.ReasoningEffort = firstNonEmpty(item.Summary.ReasoningEffort, detail.ReasoningEffort)
	notice, err := applyTranscriptWindow(detail, opts, item.Summary.Messages)
	if err != nil {
		return SessionGetItem{}, fmt.Errorf("filter Captain session %s transcript: %w", overview.ID, err)
	}
	item.notice = notice
	item.Detail = detail
	return item, nil
}

func loadSessionDetail(ctx context.Context, db sessionGetStore, overview database.SessionOverview) (*session.Session, error) {
	if overview.MessageCount > 0 {
		captainDB, ok := db.(*database.DB)
		if !ok {
			return nil, fmt.Errorf("captain session %s has database messages but its store cannot load the canonical aggregate", overview.ID)
		}
		store, err := aichat.NewDatabaseThreadStore(captainDB)
		if err != nil {
			return nil, err
		}
		detail, err := store.GetSession(ctx, overview.ID.String())
		if err != nil {
			return nil, fmt.Errorf("load canonical Captain session %s: %w", overview.ID, err)
		}
		runs, err := db.ListPromptRuns(ctx, database.PromptRunFilter{SessionID: &overview.ID})
		if err != nil {
			return nil, fmt.Errorf("list prompt runs for Captain session %s: %w", overview.ID, err)
		}
		if len(runs) > 0 {
			if err := attachPromptRunData(detail, runs[0]); err != nil {
				return nil, fmt.Errorf("attach prompt run %s to Captain session %s: %w", runs[0].ID, overview.ID, err)
			}
		}
		return detail, nil
	}
	path := stringOr(overview.HistoryFile, stringOr(overview.Path, ""))
	var detail *session.Session
	if path != "" {
		stopParse := rpchttp.Track(ctx, "parse")
		parsed, err := buildSessionModel(candidateFromOverview(overview))
		stopParse()
		if err != nil {
			return nil, fmt.Errorf("parse Captain session %s: %w", overview.ID, err)
		}
		detail = parsed
		detail.ID = overview.ID.String()
		detail.ProviderSessionID = stringOr(overview.ProviderSessionID, "")
		detail.Revision = overview.StateVersion
	}
	stopPromptRuns := rpchttp.Track(ctx, "prompt_runs")
	runs, err := db.ListPromptRuns(ctx, database.PromptRunFilter{SessionID: &overview.ID})
	stopPromptRuns()
	if err != nil {
		return nil, fmt.Errorf("list prompt runs for Captain session %s: %w", overview.ID, err)
	}
	if len(runs) > 0 {
		if detail == nil {
			if detail, err = sessionFromPromptRun(overview, runs[0]); err != nil {
				return nil, err
			}
		} else if err := attachPromptRunData(detail, runs[0]); err != nil {
			return nil, fmt.Errorf("attach prompt run %s to Captain session %s: %w", runs[0].ID, overview.ID, err)
		}
	}
	if detail == nil {
		return nil, nil
	}
	// The transcript and prompt-run branches build the aggregate from the source
	// that produced the session, so neither carries the stored projection the
	// database branch gets from GetSession.
	if err := applyOverviewProjection(ctx, db, overview, detail); err != nil {
		return nil, fmt.Errorf("project Captain session %s overview: %w", overview.ID, err)
	}
	return detail, nil
}

// applyOverviewProjection adapts the store interface; the projection itself is
// shared with the database branch.
func applyOverviewProjection(
	ctx context.Context,
	db sessionGetStore,
	overview database.SessionOverview,
	detail *session.Session,
) error {
	store, ok := db.(aichat.OverviewProjectionStore)
	if !ok {
		return nil
	}
	return aichat.ApplyOverviewProjection(ctx, store, overview, detail)
}

func sessionFromPromptRun(overview database.SessionOverview, run database.PromptRun) (*session.Session, error) {
	resolved := run.Runtime.Resolved
	requested := run.Runtime.Requested
	detail := &session.Session{
		ID:                overview.ID.String(),
		ProviderSessionID: stringOr(overview.ProviderSessionID, ""),
		Revision:          overview.StateVersion,
		Source:            overview.Source,
		Project:           stringOr(overview.Project, ""),
		CWD:               stringOr(overview.CWD, ""),
		Slug:              stringOr(overview.Slug, ""),
		Title:             stringOr(overview.Title, ""),
		InitialPrompt:     stringOr(overview.InitialPrompt, run.PromptMarkdown),
		Version:           stringOr(overview.CLIVersion, ""),
		Provider:          firstNonEmpty(overview.Provider, resolved.Provider, requested.Provider),
		Backend:           firstNonEmpty(stringOr(overview.Backend, ""), resolved.Backend, requested.Backend),
		Model:             firstNonEmpty(stringOr(overview.Model, ""), resolved.Model, requested.Model),
		ReasoningEffort:   firstNonEmpty(stringOr(overview.Effort, ""), resolved.Effort, requested.Effort),
		StartedAt:         firstTime(overview.StartedAt, run.StartedAt, &run.QueuedAt),
		EndedAt:           firstTime(overview.EndedAt, run.FinishedAt),
	}
	if run.PromptMarkdown != "" {
		detail.Messages = append(detail.Messages, promptRunMessage(run, "user", run.PromptMarkdown))
	}
	resultText, err := promptRunResultText(run)
	if err != nil {
		return nil, err
	}
	if resultText != "" {
		detail.Messages = append(detail.Messages, promptRunMessage(run, "assistant", resultText))
	}
	if run.Error != "" {
		detail.Events = append(detail.Events, session.Event{
			Type: "error", Scope: "prompt_run", Timestamp: run.FinishedAt, UUID: run.ID.String(),
			Data: map[string]any{"message": run.Error, "state": run.State},
		})
	}
	if err := attachPromptRunData(detail, run); err != nil {
		return nil, fmt.Errorf("encode prompt run %s: %w", run.ID, err)
	}
	return detail, nil
}

func promptRunMessage(run database.PromptRun, role, text string) session.Message {
	return session.Message{
		ID: run.ID.String() + "-" + role, Role: role,
		Parts: []session.Part{{Type: session.PartText, Text: text}},
	}
}

func promptRunResultText(run database.PromptRun) (string, error) {
	if run.ResultText != "" {
		return run.ResultText, nil
	}
	if len(run.ResultJSON) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(run.ResultJSON)
	if err != nil {
		return "", fmt.Errorf("encode prompt run %s result: %w", run.ID, err)
	}
	return string(raw), nil
}

func attachPromptRunData(detail *session.Session, run database.PromptRun) error {
	if len(run.RenderedSpec) > 0 {
		raw, err := json.Marshal(run.RenderedSpec)
		if err != nil {
			return err
		}
		detail.Prompt = raw
	}
	detail.StructuredOutput = promptRunStructuredOutput(run)
	return nil
}

func promptRunStructuredOutput(run database.PromptRun) map[string]any {
	if run.ResultJSON != nil {
		return run.ResultJSON
	}
	if !promptRunDeclaresOutputSchema(run.RenderedSpec) || !json.Valid([]byte(run.ResultText)) {
		return nil
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(run.ResultText), &output); err != nil {
		return nil
	}
	return output
}

func promptRunDeclaresOutputSchema(rendered map[string]any) bool {
	schema, ok := rendered["outputSchema"].(map[string]any)
	return ok && len(schema) > 0
}

func firstTime(values ...*time.Time) *time.Time {
	for _, value := range values {
		if value != nil && !value.IsZero() {
			return value
		}
	}
	return nil
}

func sessionChatCapabilities(summary SessionRecord) ChatCapabilities {
	capabilities := chatCapabilitiesForBackend(summary.Backend)
	if summary.Source == "claude" || summary.Source == "codex" {
		capabilities.Resume = true
	}
	return capabilities
}

func enrichSessionDetail(detail *session.Session, summary SessionRecord) {
	if detail.Provider == "" {
		detail.Provider = summary.Provider
	}
	if detail.Model == "" {
		detail.Model = summary.Model
	}
	if detail.Backend == "" {
		detail.Backend = summary.Backend
	}
	if detail.ExecutionMode == "" {
		detail.ExecutionMode = api.Backend(detail.Backend).Mode()
	}
	if detail.ReasoningEffort == "" {
		detail.ReasoningEffort = summary.ReasoningEffort
	}
	if summary.Live != nil {
		detail.Live = &session.LiveProcess{
			PID: summary.Live.PID, Status: summary.Live.Status, Active: summary.Live.Active,
			CPUPercent: summary.Live.CPUPercent, MemoryPercent: summary.Live.MemoryPercent,
			StartedAt: summary.Live.StartedAt, CWD: summary.Live.CWD, Command: summary.Live.Command,
		}
	}
	for i := range detail.Turns {
		if detail.Turns[i].Backend == "" {
			detail.Turns[i].Backend = summary.Backend
		}
		if detail.Turns[i].ReasoningEffort == "" {
			detail.Turns[i].ReasoningEffort = summary.ReasoningEffort
		}
	}
}

// Pretty renders the flat list form. NOTE: terminal and HTML output do not go
// through here — clicky's TryTypedValue matches TreeMixin before Pretty, so
// Tree below wins for any formatter-driven render. Both delegate to
// SessionGetItem.Pretty, which is where per-session layout changes belong.
func (r SessionGetResult) Pretty() clickyapi.Text {
	list := clicky.List()
	list.Unstyled = true
	list.MaxInline = 1
	for i := range r.Sessions {
		list.Items = append(list.Items, sessionGetListItem{text: r.Sessions[i].Pretty()})
	}
	return clickyapi.Text{}.Add(list)
}

// Tree roots the forest at every session whose parent is absent from the
// result set, not only at sessions with no parent at all. Resolving a provider
// session ID that exists under several sources (the schema allows one row per
// source) returns a mid-thread slice whose parents all live outside the slice,
// and anchoring roots at "" alone rendered that slice as an empty forest.
func (r SessionGetResult) Tree() clickyapi.TreeNode {
	byParent := map[string][]SessionGetItem{}
	present := make(map[string]struct{}, len(r.Sessions))
	for i := range r.Sessions {
		byParent[r.Sessions[i].ParentSessionID] = append(byParent[r.Sessions[i].ParentSessionID], r.Sessions[i])
		present[r.Sessions[i].CaptainID] = struct{}{}
	}
	children := make([]clickyapi.TreeNode, 0, len(r.Sessions))
	rendered := map[string]struct{}{}
	addRoot := func(item SessionGetItem) {
		node := sessionGetTreeNode{
			item: item, byParent: byParent, seen: map[string]struct{}{item.CaptainID: {}},
		}
		markRendered(node, rendered)
		children = append(children, node)
	}
	for i := range r.Sessions {
		if _, parented := present[r.Sessions[i].ParentSessionID]; !parented {
			addRoot(r.Sessions[i])
		}
	}
	// Every session must appear once. Only a parent cycle can leave one
	// unreachable from the roots above, so promote whatever is left over rather
	// than dropping it from the render.
	for i := range r.Sessions {
		if _, shown := rendered[r.Sessions[i].CaptainID]; !shown {
			addRoot(r.Sessions[i])
		}
	}
	return &clickyapi.ConcreteBranchNode{Children: children}
}

func markRendered(node sessionGetTreeNode, rendered map[string]struct{}) {
	rendered[node.item.CaptainID] = struct{}{}
	for _, child := range node.GetChildren() {
		markRendered(child.(sessionGetTreeNode), rendered)
	}
}

type sessionGetTreeNode struct {
	item     SessionGetItem
	byParent map[string][]SessionGetItem
	// seen carries the ancestors already rendered on this branch so a cyclic
	// parent reference cannot recurse forever now that roots are derived.
	seen map[string]struct{}
}

func (n sessionGetTreeNode) Pretty() clickyapi.Text { return n.item.Pretty() }

func (n sessionGetTreeNode) GetChildren() []clickyapi.TreeNode {
	items := n.byParent[n.item.CaptainID]
	children := make([]clickyapi.TreeNode, 0, len(items))
	for i := range items {
		if _, cycle := n.seen[items[i].CaptainID]; cycle {
			continue
		}
		seen := make(map[string]struct{}, len(n.seen)+1)
		for id := range n.seen {
			seen[id] = struct{}{}
		}
		seen[items[i].CaptainID] = struct{}{}
		children = append(children, sessionGetTreeNode{item: items[i], byParent: n.byParent, seen: seen})
	}
	return children
}

func (i SessionGetItem) Pretty() clickyapi.Text {
	text := clickyapi.Text{}.
		Append("Captain ", "text-muted").
		Append(i.CaptainID, "font-bold text-blue-600")
	if strings.TrimSpace(i.Host) != "" {
		text = text.Append("  "+i.Host, "text-muted")
	}
	// The detail body opens with its own header and Summary rows carrying
	// source, project, cwd and the provider session id, so repeating them here
	// would duplicate four of the first eight lines of output.
	if i.Detail != nil {
		return text.NewLine().NewLine().Add(i.Detail.Pretty()).Add(i.hiddenRowsNotice())
	}
	if i.Summary.Source != "" {
		text = text.Append("  ", "").Append(strings.ToUpper(i.Summary.Source), "text-muted")
	}
	for _, metadata := range []struct{ label, value string }{
		{label: "Provider session", value: i.ProviderSessionID},
		{label: "Project", value: i.Summary.Project},
		{label: "CWD", value: i.Summary.CWD},
	} {
		if strings.TrimSpace(metadata.value) != "" {
			text = text.NewLine().
				Append("  "+metadata.label+": ", "text-muted").
				Append(metadata.value, "")
		}
	}
	if i.Aggregate {
		return text.NewLine().Append("  Aggregate session; child results are shown below", "text-muted")
	}
	return text.NewLine().Append("  Transcript: unavailable", "text-amber-600")
}

// hiddenRowsNotice reports messages dropped between the session's full count
// and the rendered transcript so a bounded view never reads as the whole
// session. See transcriptNotice for how the causes are attributed.
func (i SessionGetItem) hiddenRowsNotice() clickyapi.Text {
	if i.Detail == nil {
		return clickyapi.Text{}
	}
	return i.notice.text(len(i.Detail.Messages))
}

type sessionGetListItem struct {
	text clickyapi.Text
}

func (i sessionGetListItem) String() string   { return i.text.String() + "\n" }
func (i sessionGetListItem) ANSI() string     { return i.text.ANSI() }
func (i sessionGetListItem) HTML() string     { return i.text.HTML() }
func (i sessionGetListItem) Markdown() string { return i.text.Markdown() }

// pageSessionTranscript windows both transcript collections: the last Tail
// rows, or an Offset/Limit slice from the start. Events are windowed alongside
// messages because provider state rows (titles, skill listings, prompt
// checkpoints) grow with the session and would otherwise ignore the window
// entirely — a --tail 10 could still emit hundreds of event lines.
func pageSessionTranscript(s *session.Session, opts SessionGetOptions) {
	full := session.TranscriptWindow{
		Messages: len(s.Messages), Events: len(s.Events),
		ToolCalls: session.CountToolParts(s.Messages),
	}
	s.Messages = pageTranscriptRows(s.Messages, opts)
	s.Events = pageTranscriptRows(s.Events, opts)
	// Recorded only when rows were actually dropped, so summaries of a complete
	// transcript stay free of window annotations.
	if len(s.Messages) != full.Messages || len(s.Events) != full.Events {
		if s.Window == nil {
			s.Window = &full
		}
	}
}

func pageTranscriptRows[T any](rows []T, opts SessionGetOptions) []T {
	if opts.Tail > 0 {
		if len(rows) > opts.Tail {
			return rows[len(rows)-opts.Tail:]
		}
		return rows
	}
	if opts.Offset <= 0 && opts.Limit <= 0 {
		return rows
	}
	offset := max(opts.Offset, 0)
	if offset >= len(rows) {
		return nil
	}
	rows = rows[offset:]
	if opts.Limit > 0 && len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
	}
	return rows
}
