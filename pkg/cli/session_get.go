package cli

import (
	"context"
	"fmt"
	"strings"

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
	CaptainID         string            `json:"captainId"`
	ParentSessionID   string            `json:"parentSessionId,omitempty"`
	RootSessionID     string            `json:"rootSessionId,omitempty"`
	ProviderSessionID string            `json:"providerSessionId,omitempty"`
	Host              string            `json:"host,omitempty"`
	DetailAvailable   bool              `json:"detailAvailable"`
	Summary           SessionRecord     `json:"summary"`
	Detail            *session.Session  `json:"detail,omitempty"`
	ActiveRunID       string            `json:"activeRunId,omitempty"`
	Chat              *ChatCapabilities `json:"chat,omitempty"`
	ChatState         *ChatStateFrame   `json:"chatState,omitempty"`
}

// RunSessionGet returns every Captain session matching an exact Captain UUID
// or provider-session-id prefix. Transcript-less matches remain visible via
// their overview metadata; recorded transcripts are parsed and paged.
func RunSessionGet(ctx context.Context, opts SessionGetOptions) (SessionGetResult, error) {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		return SessionGetResult{}, fmt.Errorf("id is required")
	}
	db, err := captainDB(ctx)
	if err != nil {
		return SessionGetResult{}, err
	}
	overviews, err := resolveOverviewsByAnyID(ctx, db, id)
	if err != nil {
		return SessionGetResult{}, err
	}

	defer rpchttp.Track(ctx, "parse")()
	items := make([]SessionGetItem, 0, len(overviews))
	for i := range overviews {
		overview := overviews[i]
		path := stringOr(overview.HistoryFile, stringOr(overview.Path, ""))
		item := SessionGetItem{
			CaptainID:         overview.ID.String(),
			ProviderSessionID: stringOr(overview.ProviderSessionID, ""),
			Host:              overview.HostID,
			DetailAvailable:   path != "",
			Summary:           recordFromOverview(overview),
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
		if path != "" {
			detail, buildErr := buildSessionModel(candidateFromOverview(overview))
			if buildErr != nil {
				return SessionGetResult{}, fmt.Errorf("parse Captain session %s: %w", overview.ID, buildErr)
			}
			attachPromptRun(ctx, db, overview.ID, detail)
			enrichSessionDetail(detail, item.Summary)
			pageSessionMessages(detail, opts)
			item.Detail = detail
		}
		items = append(items, item)
	}
	rootID := ""
	if len(items) > 1 && items[0].ParentSessionID == "" {
		rootID = items[0].CaptainID
	}
	return SessionGetResult{RootSessionID: rootID, Sessions: items, Total: len(items)}, nil
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

func (r SessionGetResult) Pretty() clickyapi.Text {
	list := clicky.List()
	list.Unstyled = true
	list.MaxInline = 1
	for i := range r.Sessions {
		list.Items = append(list.Items, sessionGetListItem{text: r.Sessions[i].Pretty()})
	}
	return clickyapi.Text{}.Add(list)
}

func (r SessionGetResult) Tree() clickyapi.TreeNode {
	byParent := map[string][]SessionGetItem{}
	for i := range r.Sessions {
		byParent[r.Sessions[i].ParentSessionID] = append(byParent[r.Sessions[i].ParentSessionID], r.Sessions[i])
	}
	children := make([]clickyapi.TreeNode, 0, len(byParent[""]))
	for _, item := range byParent[""] {
		children = append(children, sessionGetTreeNode{item: item, byParent: byParent})
	}
	return &clickyapi.ConcreteBranchNode{Children: children}
}

type sessionGetTreeNode struct {
	item     SessionGetItem
	byParent map[string][]SessionGetItem
}

func (n sessionGetTreeNode) Pretty() clickyapi.Text { return n.item.Pretty() }

func (n sessionGetTreeNode) GetChildren() []clickyapi.TreeNode {
	items := n.byParent[n.item.CaptainID]
	children := make([]clickyapi.TreeNode, len(items))
	for i := range items {
		children[i] = sessionGetTreeNode{item: items[i], byParent: n.byParent}
	}
	return children
}

func (i SessionGetItem) Pretty() clickyapi.Text {
	text := clickyapi.Text{}.
		Append("Captain ", "text-gray-500").
		Append(i.CaptainID, "font-bold text-blue-600")
	if i.Summary.Source != "" {
		text = text.Append("  ", "").Append(strings.ToUpper(i.Summary.Source), "text-gray-500")
	}
	for _, metadata := range []struct{ label, value string }{
		{label: "Provider session", value: i.ProviderSessionID},
		{label: "Host", value: i.Host},
		{label: "Project", value: i.Summary.Project},
		{label: "CWD", value: i.Summary.CWD},
	} {
		if strings.TrimSpace(metadata.value) != "" {
			text = text.NewLine().
				Append("  "+metadata.label+": ", "text-gray-500").
				Append(metadata.value, "text-muted")
		}
	}
	if i.Detail != nil {
		return text.NewLine().NewLine().Add(i.Detail.Pretty())
	}
	return text.NewLine().Append("  Transcript: unavailable", "text-amber-600")
}

type sessionGetListItem struct {
	text clickyapi.Text
}

func (i sessionGetListItem) String() string   { return i.text.String() + "\n" }
func (i sessionGetListItem) ANSI() string     { return i.text.ANSI() }
func (i sessionGetListItem) HTML() string     { return i.text.HTML() }
func (i sessionGetListItem) Markdown() string { return i.text.Markdown() }

// pageSessionMessages windows the message stream: the last Tail messages, or
// an Offset/Limit slice from the start.
func pageSessionMessages(s *session.Session, opts SessionGetOptions) {
	if opts.Tail > 0 {
		if len(s.Messages) > opts.Tail {
			s.Messages = s.Messages[len(s.Messages)-opts.Tail:]
		}
		return
	}
	if opts.Offset <= 0 && opts.Limit <= 0 {
		return
	}
	offset := max(opts.Offset, 0)
	if offset >= len(s.Messages) {
		s.Messages = nil
		return
	}
	s.Messages = s.Messages[offset:]
	if opts.Limit > 0 && len(s.Messages) > opts.Limit {
		s.Messages = s.Messages[:opts.Limit]
	}
}
