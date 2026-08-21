package session

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/commons/logger"
	"github.com/google/uuid"
	"github.com/segmentio/encoding/json"
)

var codexLog = logger.GetLogger("session")

// BuildCodex builds the unified model for each given Codex session file.
// Unreadable files are logged at Warn and skipped.
func BuildCodex(files []string) []*Session {
	out := make([]*Session, 0, len(files))
	for _, f := range files {
		info, _ := history.ReadCodexSessionInfo(f)
		if info != nil && history.IsCodexAutoReviewModel(info.Model) {
			continue
		}
		uses, err := history.ExtractCodexToolUses(f)
		if err != nil {
			codexLog.Warnf("skipping unreadable codex session %s: %v", f, err)
			continue
		}
		if len(uses) == 0 {
			continue
		}
		sessionID := ""
		if info != nil {
			sessionID = strings.TrimSpace(info.ID)
		}
		if sessionID == "" {
			sessionID = codexSessionIDFromHistoryFile(f)
		}
		if sessionID != "" {
			if info == nil {
				info = &history.CodexSessionInfo{}
			}
			info.ID = sessionID
			for i := range uses {
				if strings.TrimSpace(uses[i].SessionID) == "" {
					uses[i].SessionID = sessionID
				}
			}
		}
		s := buildCodexSession(uses, info)
		s.HistoryFile = f
		if s.Root != nil {
			s.Root.HistoryFile = f
		}
		out = append(out, s)
	}
	return out
}

// codexSessionIDFromHistoryFile recovers the UUID Codex writes at the end of
// rollout filenames. It is a last-resort identity when an otherwise-readable
// transcript predates session_meta or contains a metadata shape newer than the
// local parser.
func codexSessionIDFromHistoryFile(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	const uuidLength = 36
	if len(name) < uuidLength {
		return ""
	}
	candidate := name[len(name)-uuidLength:]
	id, err := uuid.Parse(candidate)
	if err != nil {
		return ""
	}
	return id.String()
}

// buildCodexSession assembles one Session from a Codex session's tool uses and
// metadata sidecar.
func buildCodexSession(uses []history.ToolUse, info *history.CodexSessionInfo) *Session {
	s := &Session{Source: "codex"}
	if info != nil {
		s.CWD = info.CWD
	}
	root := &Agent{IsRoot: true}

	var read, written []string
	costs := api.Costs{}
	turns := newCodexTurnBuilder()
	agents := map[string]*Agent{}
	var latestContext *Context
	var cumulative codexCumulative

	for _, u := range uses {
		if s.ID == "" && u.SessionID != "" {
			s.ID = u.SessionID
		}
		if s.CWD == "" {
			s.CWD = u.CWD
		}
		if s.Model == "" {
			s.Model = u.Model
		}
		if u.Timestamp != nil {
			extendRange(s, *u.Timestamp)
		}
		if tools.IsEventToolName(u.Tool) || u.Tool == "ApiError" {
			ev := codexUseToEvent(u)
			if u.Tool == "MemoryCitation" {
				ev.Scope = "session"
				s.Events = append(s.Events, ev)
			} else if ev.Scope == "turn" {
				turns.addEvent(u, ev)
			} else {
				s.Events = append(s.Events, ev)
			}
			mergeCodexCapabilities(&s.Capabilities, u)
			if u.Tool == "TokenCount" {
				// Prefer codex's own running total over the record's
				// self-reported delta: the deltas do not add up to it.
				cost := codexCostFromUse(u)
				if delta, ok := cumulative.delta(u); ok {
					cost = codexCostFromUsage(u.Model, delta)
				}
				if cost.TotalTokens != 0 {
					costs = append(costs, cost)
					turns.addUsage(u, cost)
				}
				if ctx := codexContextFromUse(u); ctx != nil {
					latestContext = ctx
				}
			}
			continue
		}
		mergeCodexCapabilities(&s.Capabilities, u)
		if u.Tool == "Agent" && u.AgentID != "" {
			if agents[u.AgentID] == nil {
				agents[u.AgentID] = &Agent{
					ID:   u.AgentID,
					Type: u.AgentType,
					Desc: u.AgentDesc,
				}
			}
		}
		collectCodexPaths(u, &read, &written)
		msg := codexUseToMessage(u)
		turns.addMessage(u, msg.ID)
		s.Messages = append(s.Messages, msg)
	}

	if info != nil {
		if s.ID == "" {
			s.ID = info.ID
		}
		if s.CWD == "" {
			s.CWD = info.CWD
		}
		s.Provider = info.ModelProvider
		s.Version = info.CLIVersion
		s.Git.Branch = info.GitBranch
		s.Git.Commit = info.GitCommit
		if s.Model == "" {
			s.Model = info.Model
		}
		if info.StartedAt != nil {
			extendRange(s, *info.StartedAt)
		}
	}
	if s.Project == "" && s.CWD != "" {
		s.Project = filepath.Base(claude.FindProjectRoot(s.CWD))
	}

	root.ID = s.ID
	root.Cost = costs.Sum()
	root.Usage = usageFromCost(root.Cost)
	s.Root = root
	s.Agents = []*Agent{root}
	for _, agent := range sortedCodexAgents(agents) {
		agent.ParentID = root.ID
		root.Children = append(root.Children, agent)
		s.Agents = append(s.Agents, agent)
	}
	s.Files = ChangedFiles{
		Read:    sortedUnique(relativizeAll(read, s.CWD)),
		Written: sortedUnique(relativizeAll(written, s.CWD)),
	}
	s.Todos = latestTodos(uses, func(tu history.ToolUse) (string, map[string]any) {
		return tu.Tool, tu.Input
	})
	s.Plan = CodexPlanFromToolUses(uses)
	s.Cost = costs.Sum()
	s.Usage = usageFromCost(s.Cost)
	s.ToolCosts = collapseByModel(costs)
	s.Context = latestContext
	s.Turns = turns.finish()
	s.Capabilities.Tools = sortedStrings(s.Capabilities.Tools)
	s.Capabilities.PendingMCPServers = sortedStrings(s.Capabilities.PendingMCPServers)
	s.Capabilities.Agents = sortedStrings(s.Capabilities.Agents)
	s.Capabilities.Skills = sortedStrings(s.Capabilities.Skills)
	applySessionIdentity(s)
	return s
}

// CodexPlanFromToolUses renders the latest Codex update_plan/TodoWrite state as
// a canonical inline Plan. Codex revises plans in-place rather than writing plan
// files, so the final TodoWrite payload is the durable plan content.
func CodexPlanFromToolUses(uses []history.ToolUse) *Plan {
	var latest []any
	var ts *time.Time
	var taggedContent string
	var taggedEvents []PlanEvent
	for _, use := range uses {
		if use.Tool == "Plan" {
			content, _ := use.Input["content"].(string)
			if content = strings.TrimSpace(content); content != "" {
				taggedContent = content
				taggedEvents = append(taggedEvents, PlanEvent{Kind: PlanWrite, Timestamp: use.Timestamp})
			}
			continue
		}
		if use.Tool != "TodoWrite" {
			continue
		}
		todos, ok := use.Input["todos"].([]any)
		if !ok || len(todos) == 0 {
			continue
		}
		latest = todos
		ts = use.Timestamp
	}
	if taggedContent != "" {
		return &Plan{
			Content:  taggedContent,
			Explicit: true,
			Events:   taggedEvents,
		}
	}
	if len(latest) == 0 {
		return nil
	}
	content := renderCodexPlan(latest)
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return &Plan{
		Content:  content,
		Explicit: true,
		Events:   []PlanEvent{{Kind: PlanWrite, Timestamp: ts}},
	}
}

func renderCodexPlan(steps []any) string {
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
		text = strings.TrimSpace(text)
		if text == "" {
			continue
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

// relativizeAll makes paths relative to cwd for consistency with the claude
// model, which stores project-relative paths.
func relativizeAll(paths []string, cwd string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = claude.RelativePath(p, cwd)
	}
	return out
}

// codexUseToMessage maps a Codex tool use to a canonical message: "Assistant"
// and "Reasoning" become text/reasoning turns, everything else a tool part with
// the inline response as its output.
//
// SourceLine is the row's identity for incremental ingest, so it is carried here
// rather than left to the caller: keying Codex messages on their ordinal in the
// parsed slice made every re-parse renumber the file, and the high-water mark
// then dropped the renumbered rows for good.
func codexUseToMessage(u history.ToolUse) Message {
	message := codexUseToMessageBody(u)
	message.SourceLine = u.SourceLine
	message.Provisional = u.Provisional
	return message
}

func codexUseToMessageBody(u history.ToolUse) Message {
	id := codexMessageID(u)
	agentID := u.SessionID
	prov := &Provenance{
		Source:          "codex",
		CWD:             u.CWD,
		Model:           u.Model,
		ReasoningEffort: u.ReasoningEffort,
		SessionID:       u.SessionID,
		AgentID:         agentID,
		Timestamp:       u.Timestamp,
	}
	switch u.Tool {
	case "System":
		return Message{ID: id, Role: "system", Parts: []Part{{Type: PartText, Text: codexText(u)}}, TurnID: u.TurnID, Provenance: prov, AgentID: agentID}
	case "User":
		return Message{ID: id, Role: "user", Parts: []Part{{Type: PartText, Text: codexText(u)}}, TurnID: u.TurnID, Provenance: prov, AgentID: agentID}
	case "Assistant":
		return Message{ID: id, Role: "assistant", Parts: []Part{{Type: PartText, Text: codexText(u)}}, TurnID: u.TurnID, Provenance: prov, AgentID: agentID}
	case "Reasoning":
		return Message{ID: id, Role: "assistant", Parts: []Part{{Type: PartReasoning, Text: codexText(u)}}, TurnID: u.TurnID, Provenance: prov, AgentID: agentID}
	default:
		part := Part{
			Type:       PartTool,
			ToolName:   u.Tool,
			ToolCallID: u.ToolUseID,
			State:      ToolStateInputAvailable,
			Input:      marshalInput(u.Input),
		}
		if u.Response != "" {
			if out, err := json.Marshal(u.Response); err == nil {
				part.Output = out
			}
			part.State = ToolStateOutputAvailable
		}
		return Message{ID: id, Role: "assistant", Parts: []Part{part}, TurnID: u.TurnID, Provenance: prov, AgentID: agentID}
	}
}

func codexUseToEvent(u history.ToolUse) Event {
	typ, _ := u.Input["event"].(string)
	if typ == "" {
		typ = "event"
	}
	data := make(map[string]any, len(u.Input))
	for k, v := range u.Input {
		if k == "event" {
			continue
		}
		data[k] = v
	}
	scope := "session"
	if u.TurnID != "" {
		scope = "turn"
	}
	return Event{
		Type:      typ,
		Scope:     scope,
		TurnID:    u.TurnID,
		Timestamp: u.Timestamp,
		Data:      data,
	}
}

func codexMessageID(u history.ToolUse) string {
	if u.ToolUseID != "" {
		return u.ToolUseID
	}
	if u.Timestamp != nil {
		return fmt.Sprintf("codex-%s-%s-%d", u.TurnID, u.Tool, u.Timestamp.UnixNano())
	}
	return fmt.Sprintf("codex-%s-%s", u.TurnID, u.Tool)
}

func codexText(u history.ToolUse) string {
	if t, ok := u.Input["text"].(string); ok {
		return t
	}
	return ""
}

func marshalInput(input map[string]any) json.RawMessage {
	if len(input) == 0 {
		return nil
	}
	if b, err := json.Marshal(input); err == nil {
		return b
	}
	return nil
}

// collectCodexPaths appends read/write paths for file-touching Codex tools.
// Which files a tool touched is decided once, in history.ToolFootprint, so a
// Codex session and a Claude session cannot disagree about the same field.
func collectCodexPaths(u history.ToolUse, read, written *[]string) {
	footprint := history.ToolFootprint(u)
	appendAbsolute(read, footprint.Read, u.CWD)
	appendAbsolute(written, footprint.Written, u.CWD)
}

// appendAbsolute anchors patch-derived paths the same way the parsed ApplyPatch
// case does. A patch embedded in a script yields the paths verbatim, so without
// this a relative path would get a different session path identity than the same
// file named by an already-normalized patch.
func appendAbsolute(dst *[]string, paths []string, cwd string) {
	for _, p := range paths {
		*dst = append(*dst, claude.AbsolutePath(p, cwd, ""))
	}
}

type codexTurnBuilder struct {
	order []string
	byID  map[string]*Turn
}

func newCodexTurnBuilder() *codexTurnBuilder {
	return &codexTurnBuilder{byID: map[string]*Turn{}}
}

func (b *codexTurnBuilder) ensure(id string, ts *time.Time) *Turn {
	if id == "" {
		return nil
	}
	if turn := b.byID[id]; turn != nil {
		if turn.StartedAt == nil && ts != nil {
			turn.StartedAt = cloneTime(ts)
		}
		return turn
	}
	turn := &Turn{
		ID:    id,
		Index: len(b.order) + 1,
	}
	if ts != nil {
		turn.StartedAt = cloneTime(ts)
	}
	b.byID[id] = turn
	b.order = append(b.order, id)
	return turn
}

func (b *codexTurnBuilder) addMessage(u history.ToolUse, messageID string) {
	turn := b.ensure(u.TurnID, u.Timestamp)
	if turn == nil {
		return
	}
	turn.MessageIDs = appendUnique(turn.MessageIDs, messageID)
	observeCodexTurnRuntime(turn, u)
}

func (b *codexTurnBuilder) addEvent(u history.ToolUse, ev Event) {
	turn := b.ensure(u.TurnID, u.Timestamp)
	if turn == nil {
		return
	}
	if u.Tool == "TaskStarted" && u.Timestamp != nil {
		turn.StartedAt = cloneTime(u.Timestamp)
	}
	if u.Tool == "TaskComplete" && u.Timestamp != nil {
		turn.EndedAt = cloneTime(u.Timestamp)
	}
	turn.Events = append(turn.Events, ev)
	observeCodexTurnRuntime(turn, u)
}

func (b *codexTurnBuilder) addUsage(u history.ToolUse, cost api.Cost) {
	turn := b.ensure(u.TurnID, u.Timestamp)
	if turn == nil {
		return
	}
	turn.Cost = turn.Cost.Add(cost)
	turn.Usage = usageFromCost(turn.Cost)
	if ctx := codexContextFromUse(u); ctx != nil {
		turn.Context = ctx
	}
	observeCodexTurnRuntime(turn, u)
}

func observeCodexTurnRuntime(turn *Turn, u history.ToolUse) {
	if u.Model != "" {
		turn.Model = u.Model
	}
	if u.ReasoningEffort != "" {
		turn.ReasoningEffort = u.ReasoningEffort
	}
}

func (b *codexTurnBuilder) finish() []Turn {
	turns := make([]Turn, 0, len(b.order))
	for _, id := range b.order {
		turn := b.byID[id]
		if turn == nil {
			continue
		}
		if turn.EndedAt == nil {
			for i := len(turn.Events) - 1; i >= 0; i-- {
				if turn.Events[i].Timestamp != nil {
					turn.EndedAt = cloneTime(turn.Events[i].Timestamp)
					break
				}
			}
		}
		turns = append(turns, *turn)
	}
	return turns
}

func codexCostFromUse(u history.ToolUse) api.Cost {
	usage := api.Usage{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		ReasoningTokens: u.ReasoningTokens,
		CacheReadTokens: u.CacheReadTokens,
	}
	cost := codexCostFromUsage(u.Model, usage)
	// A record that reports only a total keeps it rather than recomputing zero.
	if cost.TotalTokens == 0 && u.TotalTokens != 0 {
		cost.TotalTokens = u.TotalTokens
	}
	return cost
}

// codexCostFromUsage prices one codex usage record. Shared by the cumulative
// (result-derived) path and the per-event fallback so both price identically.
func codexCostFromUsage(model string, usage api.Usage) api.Cost {
	if usage == (api.Usage{}) {
		return api.Cost{Model: model}
	}
	cost := api.Cost{
		Model:           model,
		InputTokens:     usage.InputTokens,
		OutputTokens:    usage.OutputTokens,
		ReasoningTokens: usage.ReasoningTokens,
		CacheReadTokens: usage.CacheReadTokens,
		TotalTokens:     usage.TotalTokens(),
	}
	// Codex runs OpenAI models: price via the registry under the openai/ key.
	// The old claude.PricingFor path mispriced every gpt-*/o* model at Claude
	// Sonnet rates (finding C1). A registry miss leaves the bucket costs zero
	// rather than inventing a wrong number.
	//
	// OpenAI list-prices reasoning at the output rate. Keep the two costs
	// separate so persistence retains the same bucket split as the token counts.
	for _, id := range []string{"openai/" + model, model} {
		if res, err := pricing.CalculateCost(id, usage.InputTokens, usage.OutputTokens, usage.ReasoningTokens, usage.CacheReadTokens, 0); err == nil {
			cost.InputCost = res.InputCost
			cost.OutputCost = res.OutputCost
			cost.ReasoningCost = res.ReasoningCost
			cost.CacheReadCost = res.CacheReadCost
			break
		}
	}
	return cost
}

// codexCumulative turns codex's running session totals into per-record deltas.
//
// The cumulative figure is the provider's own result; taking successive
// differences keeps every per-turn and per-model split summing exactly back to
// it. Summing each record's self-reported delta instead drifts — a real
// 238-event session sums to 29,469,753 against a reported 29,236,689.
type codexCumulative struct {
	prev api.Usage
	seen bool
}

// delta returns this record's share of the session total, and whether a
// cumulative figure was available at all. A counter that moves backwards means
// the session restarted its accounting, so the cumulative is taken whole.
func (c *codexCumulative) delta(u history.ToolUse) (api.Usage, bool) {
	if u.CumulativeUsage == nil {
		return api.Usage{}, false
	}
	current := *u.CumulativeUsage
	delta := api.Usage{
		InputTokens:     current.InputTokens - c.prev.InputTokens,
		OutputTokens:    current.OutputTokens - c.prev.OutputTokens,
		ReasoningTokens: current.ReasoningTokens - c.prev.ReasoningTokens,
		CacheReadTokens: current.CacheReadTokens - c.prev.CacheReadTokens,
	}
	if c.seen && (delta.InputTokens < 0 || delta.OutputTokens < 0 ||
		delta.ReasoningTokens < 0 || delta.CacheReadTokens < 0) {
		delta = current
	}
	c.prev = current
	c.seen = true
	return delta, true
}

func codexContextFromUse(u history.ToolUse) *Context {
	if u.ContextWindow == 0 {
		return nil
	}
	used := u.InputTokens + u.CacheReadTokens
	return &Context{
		UsedTokens:   used,
		WindowTokens: u.ContextWindow,
		FreePercent:  freeContextPercent(used, u.ContextWindow),
	}
}

func mergeCodexCapabilities(c *Capabilities, u history.ToolUse) {
	switch u.Tool {
	case "DeferredToolsDelta":
		c.Tools = append(c.Tools, stringsFromAny(u.Input["addedNames"])...)
		c.PendingMCPServers = append(c.PendingMCPServers, stringsFromAny(u.Input["pendingMcpServers"])...)
	case "Agent":
		if u.AgentType != "" {
			c.Agents = append(c.Agents, u.AgentType)
		}
	case "SkillListing":
		c.Skills = append(c.Skills, stringsFromAny(u.Input["names"])...)
	}
}

func sortedCodexAgents(agents map[string]*Agent) []*Agent {
	out := make([]*Agent, 0, len(agents))
	for _, agent := range agents {
		out = append(out, agent)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}
