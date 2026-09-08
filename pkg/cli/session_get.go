package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
	sessionquery "github.com/flanksource/captain/pkg/session/query"
	sessiontranscript "github.com/flanksource/captain/pkg/session/transcript"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons/logger"
)

type SessionGetResult struct {
	RootSessionID string           `json:"rootSessionId,omitempty"`
	Sessions      []SessionGetItem `json:"sessions"`
	Total         int              `json:"total"`
}

type SessionGetItem struct {
	CaptainID         string `json:"captainId"`
	ParentSessionID   string `json:"parentSessionId,omitempty"`
	RootSessionID     string `json:"rootSessionId,omitempty"`
	ProviderSessionID string `json:"providerSessionId,omitempty"`
	Host              string `json:"host,omitempty"`
	Aggregate         bool   `json:"aggregate,omitempty"`
	DetailAvailable   bool   `json:"detailAvailable"`
	// DetailSource names the source of each facet of Detail. DetailAvailable
	// alone could not tell a parsed transcript from a single synthesised prompt
	// message, which is how an un-ingested session read as an empty one.
	DetailSource map[string]string `json:"detailSource,omitempty"`
	Summary      SessionRecord     `json:"summary"`
	Detail       *session.Session  `json:"detail,omitempty"`
	// Browsers are the live agent-browser sessions this agent session launched.
	Browsers    []BrowserSession  `json:"browsers,omitempty"`
	ActiveRunID string            `json:"activeRunId,omitempty"`
	Chat        *ChatCapabilities `json:"chat,omitempty"`
	ChatState   *ChatStateFrame   `json:"chatState,omitempty"`
	notice      transcriptNotice
}

type sessionGetStore interface {
	sessionOverviewStore
	sessionquery.Store
	sessionquery.TranscriptStore
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
	stopBrowsers := rpchttp.Track(ctx, "browsers")
	attachBrowserSessions(ctx, items)
	stopBrowsers()

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
	detail, provenance, err := loadSessionDetail(ctx, db, overview)
	if err != nil {
		return SessionGetItem{}, err
	}
	if detail == nil {
		return item, nil
	}
	enrichSessionDetail(detail, item.Summary)
	// Provenance travels with the detail so an unread session is distinguishable
	// from an empty one: messages sourced from "prompt-run" mean the transcript
	// has not been ingested, not that nothing happened.
	item.DetailSource = provenance.Facets()
	item.DetailAvailable = true
	item.Summary.DetailAvailable = true
	item.Summary.Messages = max(item.Summary.Messages, len(detail.Messages))
	item.Summary.Provider = firstNonEmpty(item.Summary.Provider, detail.Provider)
	item.Summary.ModelMode = firstNonEmpty(item.Summary.ModelMode, string(detail.ModelMode))
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

// loadSessionDetail composes one session aggregate from every source that holds
// facts about it.
//
// It used to be three mutually exclusive producers chosen by incidental database
// state — stored messages, then a recorded transcript path, then a prompt run —
// so which facts a caller received depended on which branch ran, and nothing in
// the response said which one did. Approvals were read in only one of the three,
// which is why a run suspended on a tool approval reported none at all.
//
// Every contributor now runs; load.Precedence decides contested facets. A source
// that fails is reported but does not fail the read: a transcript that will not
// parse must not hide the approval that is blocking the run.
func loadSessionDetail(
	ctx context.Context,
	db sessionGetStore,
	overview database.SessionOverview,
) (*session.Session, load.Provenance, error) {
	stored, err := storedSource(ctx, db, overview)
	if err != nil {
		return nil, load.Provenance{}, err
	}

	stopCompose := rpchttp.Track(ctx, "compose")
	transcript, err := parsedTranscript(ctx, db, overview)
	if err != nil {
		stopCompose()
		return nil, load.Provenance{}, err
	}
	result, _, err := sessionquery.ComposeSession(ctx, db, overview, sessionquery.ComposeOptions{
		Stored: stored, Transcript: transcript,
	})
	stopCompose()
	if err != nil {
		return nil, load.Provenance{}, err
	}
	// A session nothing could say anything about stays metadata-only, rather
	// than being reported as an empty one.
	if result.Provenance.Of(load.FacetMessages) == load.SourceNone &&
		result.Provenance.Of(load.FacetPrompt) == load.SourceNone {
		return nil, result.Provenance, nil
	}
	return result.Session, result.Provenance, nil
}

// parsedTranscript parses the transcript of the row that actually holds one,
// which for a Gavel run is a sibling rather than the session asked for: the
// admission root is a provider-identity bridge and carries no log.
//
// A transcript that cannot be resolved or parsed is not an error — it is the
// normal state of a run whose log has not been ingested yet — so this reports
// nothing and lets the other sources answer.
func parsedTranscript(ctx context.Context, db sessionGetStore, overview database.SessionOverview) (*session.Session, error) {
	candidate, ok, err := sessionquery.TranscriptCandidate(ctx, db, overview)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	stopParse := rpchttp.Track(ctx, "parse")
	parsed, err := sessiontranscript.Parse(candidate)
	stopParse()
	if err != nil {
		logger.Debugf("captain session %s: no usable transcript: %v", overview.ID, err)
		return nil, nil
	}
	parsed.ID = overview.ID.String()
	parsed.ProviderSessionID = stringOr(overview.ProviderSessionID, "")
	parsed.Revision = overview.StateVersion
	return parsed, nil
}

// storedSource loads the aggregate the database holds, when it holds one.
func storedSource(ctx context.Context, db sessionGetStore, overview database.SessionOverview) (*session.Session, error) {
	if overview.MessageCount == 0 {
		return nil, nil
	}
	captainDB, ok := db.(*database.DB)
	if !ok {
		return nil, fmt.Errorf("captain session %s has database messages but its store cannot load the canonical aggregate", overview.ID)
	}
	store, err := sessionquery.New(captainDB)
	if err != nil {
		return nil, err
	}
	stored, err := store.StoredAggregate(ctx, overview)
	if err != nil {
		return nil, fmt.Errorf("load canonical Captain session %s: %w", overview.ID, err)
	}
	return stored, nil
}

func sessionChatCapabilities(summary SessionRecord) ChatCapabilities {
	capabilities := chatCapabilitiesFor(summary.Provider, summary.ModelMode)
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
	if detail.ModelMode == "" {
		detail.ModelMode = api.RuntimeMode(summary.ModelMode)
	}
	if detail.ExecutionMode == "" {
		detail.ExecutionMode = detail.ModelMode
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
		if detail.Turns[i].Mode == "" {
			detail.Turns[i].Mode = summary.ModelMode
			detail.Turns[i].ModelProvider = summary.Provider
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
