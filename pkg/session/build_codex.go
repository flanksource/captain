package session

import (
	"errors"
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
		s, err := BuildCodexFile(f)
		if err != nil {
			codexLog.Warnf("skipping unreadable codex session %s: %v", f, err)
			continue
		}
		out = append(out, s)
	}
	return out
}

// ErrCodexAutoReview marks a rollout written by Codex's automatic review
// model: not a user session, skipped everywhere.
var ErrCodexAutoReview = errors.New("codex auto-review session")

// ErrCodexEmpty marks a readable rollout with nothing to build a session from.
var ErrCodexEmpty = errors.New("codex rollout has no tool uses")

// BuildCodexFile builds one session from a rollout file, or reports why the
// file yields none so an ingest failure names its cause.
func BuildCodexFile(f string) (*Session, error) {
	info, _ := history.ReadCodexSessionInfo(f)
	if info != nil && history.IsCodexAutoReviewModel(info.Model) {
		return nil, ErrCodexAutoReview
	}
	uses, err := history.ExtractCodexToolUses(f)
	if err != nil {
		return nil, err
	}
	if len(uses) == 0 {
		return nil, ErrCodexEmpty
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
	return s, nil
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

// CodexAccumulator keeps the aggregate state needed by the monitor without
// retaining the transcript's historical messages and events. Add accepts only
// settled rows; Project overlays the parser's current EOF snapshot without
// committing provisional data to this checkpoint.
type CodexAccumulator struct {
	session       Session
	collect       bool
	read          map[string]struct{}
	written       map[string]struct{}
	costByModel   map[string]api.Cost
	turns         *codexTurnBuilder
	agents        map[string]*Agent
	cumulative    codexCumulative
	plan          codexPlanState
	freshMessages []Message
	rootType      string
	rootDesc      string
}

// NewCodexAccumulator creates the compact accumulator used for live monitor
// updates. A separate full collector powers BuildCodex through the same core.
func NewCodexAccumulator(historyFile string) *CodexAccumulator {
	return newCodexAccumulator(historyFile, false)
}

func newCodexAccumulator(historyFile string, collect bool) *CodexAccumulator {
	s := Session{
		ID:          codexSessionIDFromHistoryFile(historyFile),
		Source:      "codex",
		HistoryFile: historyFile,
	}
	return &CodexAccumulator{
		session: s, collect: collect,
		read: map[string]struct{}{}, written: map[string]struct{}{},
		costByModel: map[string]api.Cost{},
		turns:       newCodexTurnBuilder(collect),
		agents:      map[string]*Agent{},
	}
}

// Add advances aggregate state with rows the parser has declared final.
func (a *CodexAccumulator) Add(info *history.CodexSessionInfo, uses []history.ToolUse) {
	a.applyInfo(info)
	for _, use := range uses {
		if use.SessionID == "" {
			use.SessionID = a.session.ID
		}
		a.observe(use)
	}
}

func (a *CodexAccumulator) applyInfo(info *history.CodexSessionInfo) {
	if info == nil {
		return
	}
	if id := strings.TrimSpace(info.ID); id != "" {
		a.session.ID = id
	}
	if a.session.CWD == "" {
		a.session.CWD = info.CWD
	}
	a.session.Provider = info.ModelProvider
	a.session.Version = info.CLIVersion
	a.session.ForkedFrom = info.ParentThreadID
	a.rootType = info.ThreadSource
	a.rootDesc = strings.TrimSpace(info.AgentNickname + " " + info.AgentPath)
	a.session.Git.Branch = info.GitBranch
	a.session.Git.Commit = info.GitCommit
	if a.session.Model == "" {
		a.session.Model = info.Model
	}
	if info.StartedAt != nil {
		extendRange(&a.session, *info.StartedAt)
	}
}

func (a *CodexAccumulator) observe(use history.ToolUse) {
	s := &a.session
	if s.ID == "" {
		s.ID = use.SessionID
	}
	if s.CWD == "" {
		s.CWD = use.CWD
	}
	if s.Model == "" {
		s.Model = use.Model
	}
	if use.Timestamp != nil {
		extendRange(s, *use.Timestamp)
	}
	a.plan.observe(use)
	if items := todosFromCodexUse(use); len(items) > 0 {
		s.Todos = items
	}

	if tools.IsEventToolName(use.Tool) || use.Tool == "ApiError" {
		ev := codexUseToEvent(use)
		if use.Tool == "MemoryCitation" {
			ev.Scope = "session"
			if a.collect {
				s.Events = append(s.Events, ev)
			}
		} else if ev.Scope == "turn" {
			a.turns.addEvent(use, ev)
		} else if a.collect {
			s.Events = append(s.Events, ev)
		}
		if a.collect {
			mergeCodexCapabilities(&s.Capabilities, use)
		}
		if use.Tool == "TokenCount" {
			a.observeUsage(use)
		}
		return
	}
	if a.collect {
		mergeCodexCapabilities(&s.Capabilities, use)
		if use.Tool == "Agent" && use.AgentID != "" && a.agents[use.AgentID] == nil {
			a.agents[use.AgentID] = &Agent{ID: use.AgentID, Type: use.AgentType, Desc: use.AgentDesc}
		}
	}
	a.collectPaths(use)
	message := codexUseToMessage(use)
	a.turns.addMessage(use, message.ID)
	if a.collect {
		s.Messages = append(s.Messages, message)
	} else {
		a.freshMessages = append(a.freshMessages, message)
	}
	observeCodexIdentity(s, message)
}

func (a *CodexAccumulator) observeUsage(use history.ToolUse) {
	cost := codexCostFromUse(use)
	if delta, ok := a.cumulative.delta(use); ok {
		cost = codexCostFromUsage(use.Model, delta)
	}
	if cost.TotalTokens != 0 {
		a.session.Cost = a.session.Cost.Add(cost)
		modelCost := a.costByModel[cost.Model]
		modelCost.Model = cost.Model
		a.costByModel[cost.Model] = modelCost.Add(cost)
		a.turns.addUsage(use, cost)
	}
	if context := codexContextFromUse(use); context != nil {
		a.session.Context = context
	}
}

func (a *CodexAccumulator) collectPaths(use history.ToolUse) {
	footprint := history.ToolFootprint(use)
	for _, path := range footprint.Read {
		a.addPath(a.read, &a.session.Files.Read, path, use.CWD)
	}
	for _, path := range footprint.Written {
		a.addPath(a.written, &a.session.Files.Written, path, use.CWD)
	}
}

func (a *CodexAccumulator) addPath(seen map[string]struct{}, files *[]string, path, cwd string) {
	absolute := claude.AbsolutePath(path, cwd, "")
	if absolute == "" {
		return
	}
	if _, ok := seen[absolute]; ok {
		return
	}
	seen[absolute] = struct{}{}
	*files = insertSortedString(*files, claude.RelativePath(absolute, a.session.CWD))
}

func insertSortedString(values []string, value string) []string {
	if value == "" {
		return values
	}
	index := sort.SearchStrings(values, value)
	if index < len(values) && values[index] == value {
		return values
	}
	values = append(values, "")
	copy(values[index+1:], values[index:])
	values[index] = value
	return values
}

// Project builds the next database projection from committed aggregates plus
// the parser's non-destructive EOF snapshot. Only messages and turns touched by
// this pass are returned; session metadata remains the full current projection.
func (a *CodexAccumulator) Project(provisional []history.ToolUse) *Session {
	s := a.baseSession()
	s.Messages = append([]Message(nil), a.freshMessages...)
	a.freshMessages = nil

	plan := a.plan
	plan.taggedEvents = append([]PlanEvent(nil), plan.taggedEvents...)
	var extraRead, extraWritten []string
	for _, use := range provisional {
		if use.SessionID == "" {
			use.SessionID = s.ID
		}
		if use.Timestamp != nil {
			extendRange(&s, *use.Timestamp)
		}
		plan.observe(use)
		if items := todosFromCodexUse(use); len(items) > 0 {
			s.Todos = items
		}
		if tools.IsEventToolName(use.Tool) || use.Tool == "ApiError" {
			continue
		}
		footprint := history.ToolFootprint(use)
		appendAbsolute(&extraRead, footprint.Read, use.CWD)
		appendAbsolute(&extraWritten, footprint.Written, use.CWD)
		message := codexUseToMessage(use)
		s.Messages = append(s.Messages, message)
		observeCodexIdentity(&s, message)
	}
	s.Plan = plan.value()
	s.Files = a.changedFiles(extraRead, extraWritten)
	s.Turns = a.turns.takeDirty(provisional)
	return &s
}

func (a *CodexAccumulator) baseSession() Session {
	s := a.session
	if s.Project == "" && s.CWD != "" {
		s.Project = filepath.Base(claude.FindProjectRoot(s.CWD))
	}
	s.Files = a.changedFiles(nil, nil)
	s.Plan = a.plan.value()
	s.Usage = usageFromCost(s.Cost)
	s.ToolCosts = codexCostsByModel(a.costByModel)
	s.Root = &Agent{
		ID: s.ID, IsRoot: true, HistoryFile: s.HistoryFile,
		Type: a.rootType, Desc: a.rootDesc, Cost: s.Cost, Usage: s.Usage,
	}
	return s
}

func (a *CodexAccumulator) changedFiles(extraRead, extraWritten []string) ChangedFiles {
	if len(extraRead) == 0 && len(extraWritten) == 0 {
		return a.session.Files
	}
	files := ChangedFiles{
		Read:    append([]string(nil), a.session.Files.Read...),
		Written: append([]string(nil), a.session.Files.Written...),
	}
	for _, path := range extraRead {
		files.Read = insertSortedString(files.Read, claude.RelativePath(path, a.session.CWD))
	}
	for _, path := range extraWritten {
		files.Written = insertSortedString(files.Written, claude.RelativePath(path, a.session.CWD))
	}
	return files
}

func (a *CodexAccumulator) fullSession() *Session {
	s := a.baseSession()
	s.Turns = a.turns.finish()
	s.Capabilities.Tools = sortedStrings(s.Capabilities.Tools)
	s.Capabilities.PendingMCPServers = sortedStrings(s.Capabilities.PendingMCPServers)
	s.Capabilities.Agents = sortedStrings(s.Capabilities.Agents)
	s.Capabilities.Skills = sortedStrings(s.Capabilities.Skills)

	root := s.Root
	s.Root = root
	s.Agents = []*Agent{root}
	for _, agent := range sortedCodexAgents(a.agents) {
		agent.ParentID = root.ID
		root.Children = append(root.Children, agent)
		s.Agents = append(s.Agents, agent)
	}
	applySessionIdentity(&s)
	return &s
}

func codexCostsByModel(byModel map[string]api.Cost) api.Costs {
	models := make([]string, 0, len(byModel))
	for model := range byModel {
		models = append(models, model)
	}
	sort.Strings(models)
	costs := make(api.Costs, 0, len(models))
	for _, model := range models {
		costs = append(costs, byModel[model])
	}
	return costs
}

func todosFromCodexUse(use history.ToolUse) []tools.TodoItem {
	if _, ok := todoToolNames[use.Tool]; !ok {
		return nil
	}
	return todosFromInput(use.Input)
}

func observeCodexIdentity(s *Session, message Message) {
	if s.InitialPrompt != "" || message.Role != "user" {
		return
	}
	for _, part := range message.Parts {
		if part.Type != PartText || strings.TrimSpace(part.Text) == "" {
			continue
		}
		s.InitialPrompt = strings.TrimSpace(part.Text)
		s.Title = DeriveTitle(s.InitialPrompt)
		return
	}
}

// buildCodexSession assembles one Session from an already parsed rollout. It is
// retained as the package seam used by focused normalizer tests.
func buildCodexSession(uses []history.ToolUse, info *history.CodexSessionInfo) *Session {
	accumulator := newCodexAccumulator("", true)
	accumulator.Add(info, uses)
	return accumulator.fullSession()
}

// CodexPlanFromToolUses renders the latest Codex update_plan/TodoWrite state as
// a canonical inline Plan. Codex revises plans in-place rather than writing plan
// files, so the final TodoWrite payload is the durable plan content.
func CodexPlanFromToolUses(uses []history.ToolUse) *Plan {
	var state codexPlanState
	for _, use := range uses {
		state.observe(use)
	}
	return state.value()
}

type codexPlanState struct {
	latest        []any
	latestAt      *time.Time
	taggedContent string
	taggedEvents  []PlanEvent
}

func (s *codexPlanState) observe(use history.ToolUse) {
	if use.Tool == "Plan" {
		content, _ := use.Input["content"].(string)
		if content = strings.TrimSpace(content); content != "" {
			s.taggedContent = content
			s.taggedEvents = append(s.taggedEvents, PlanEvent{Kind: PlanWrite, Timestamp: use.Timestamp})
		}
		return
	}
	if use.Tool != "TodoWrite" {
		return
	}
	todos, ok := use.Input["todos"].([]any)
	if !ok || len(todos) == 0 {
		return
	}
	s.latest = todos
	s.latestAt = use.Timestamp
}

func (s codexPlanState) value() *Plan {
	if s.taggedContent != "" {
		return &Plan{
			Content:  s.taggedContent,
			Explicit: true,
			Events:   append([]PlanEvent(nil), s.taggedEvents...),
		}
	}
	if len(s.latest) == 0 {
		return nil
	}
	content := renderCodexPlan(s.latest)
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return &Plan{
		Content:  content,
		Explicit: true,
		Events:   []PlanEvent{{Kind: PlanWrite, Timestamp: s.latestAt}},
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
	order         []string
	byID          map[string]*Turn
	dirty         map[string]struct{}
	lastEventAt   map[string]*time.Time
	explicitEnded map[string]bool
	collect       bool
}

func newCodexTurnBuilder(collect bool) *codexTurnBuilder {
	return &codexTurnBuilder{
		byID: map[string]*Turn{}, dirty: map[string]struct{}{},
		lastEventAt: map[string]*time.Time{}, explicitEnded: map[string]bool{},
		collect: collect,
	}
}

func (b *codexTurnBuilder) ensure(id string, ts *time.Time) *Turn {
	if id == "" {
		return nil
	}
	if turn := b.byID[id]; turn != nil {
		if turn.StartedAt == nil && ts != nil {
			turn.StartedAt = cloneTime(ts)
			b.dirty[id] = struct{}{}
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
	b.dirty[id] = struct{}{}
	return turn
}

func (b *codexTurnBuilder) addMessage(u history.ToolUse, messageID string) {
	turn := b.ensure(u.TurnID, u.Timestamp)
	if turn == nil {
		return
	}
	if b.collect {
		turn.MessageIDs = appendUnique(turn.MessageIDs, messageID)
	}
	observeCodexTurnRuntime(turn, u)
	b.dirty[turn.ID] = struct{}{}
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
		b.explicitEnded[turn.ID] = true
	}
	if u.Timestamp != nil {
		b.lastEventAt[turn.ID] = cloneTime(u.Timestamp)
	}
	if b.collect {
		turn.Events = append(turn.Events, ev)
	}
	observeCodexTurnRuntime(turn, u)
	b.dirty[turn.ID] = struct{}{}
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
	b.dirty[turn.ID] = struct{}{}
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
		turn, ok := b.view(id)
		if !ok {
			continue
		}
		turns = append(turns, turn)
	}
	return turns
}

func (b *codexTurnBuilder) view(id string) (Turn, bool) {
	stored := b.byID[id]
	if stored == nil {
		return Turn{}, false
	}
	turn := *stored
	if !b.explicitEnded[id] && b.lastEventAt[id] != nil {
		turn.EndedAt = cloneTime(b.lastEventAt[id])
	}
	return turn, true
}

// takeDirty returns committed turns changed by this batch plus transient copies
// touched by the current EOF snapshot, then clears the committed dirty set.
func (b *codexTurnBuilder) takeDirty(provisional []history.ToolUse) []Turn {
	projected := map[string]Turn{}
	for id := range b.dirty {
		if turn, ok := b.view(id); ok {
			projected[id] = turn
		}
	}
	b.dirty = map[string]struct{}{}

	nextIndex := len(b.order) + 1
	for _, use := range provisional {
		id := use.TurnID
		if id == "" {
			continue
		}
		turn, ok := projected[id]
		if !ok {
			if committed, exists := b.view(id); exists {
				turn = committed
			} else {
				turn = Turn{ID: id, Index: nextIndex}
				nextIndex++
			}
		}
		if turn.StartedAt == nil && use.Timestamp != nil {
			turn.StartedAt = cloneTime(use.Timestamp)
		}
		observeCodexTurnRuntime(&turn, use)
		if (tools.IsEventToolName(use.Tool) || use.Tool == "ApiError") && use.Timestamp != nil &&
			use.Tool != "MemoryCitation" && !b.explicitEnded[id] {
			turn.EndedAt = cloneTime(use.Timestamp)
		}
		projected[id] = turn
	}

	turns := make([]Turn, 0, len(projected))
	for _, turn := range projected {
		turns = append(turns, turn)
	}
	sort.Slice(turns, func(i, j int) bool { return turns[i].Index < turns[j].Index })
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
