package cmux

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/ai/pricing"
)

// High-level agent states surfaced to the dashboard, derived from the last
// meaningful event in the session log. Mirrors the TS deriveSessionState mapping.
const (
	sessionStateThinking  = "thinking"
	sessionStateWorking   = "working"
	sessionStateAsk       = "ask"
	sessionStateCompleted = "completed"
	// sessionStateError marks a turn that ended on an API/network error rather than
	// a normal completion, so the dashboard surfaces the failure instead of a stale
	// "completed". See history.EventError.
	sessionStateError = "error"
)

// isAskTool reports whether a tool pauses the turn awaiting the user (Claude
// emits no terminal stop reason for these), so the session is "asking", not
// "working", until they respond.
func isAskTool(tool string) bool {
	switch tool {
	case "AskUserQuestion", "ExitPlanMode":
		return true
	default:
		return false
	}
}

// sessionStateFromLine maps the last event of one session-log line to the agent
// state it represents, plus the failure reason when that event is an API/network
// error. Non-conversational lines (tool results, bookkeeping) yield no event and
// return ("", "", false) so the caller keeps the prior state.
func sessionStateFromLine(line []byte) (state, errMsg string, ok bool) {
	events, err := history.ParseSessionEvents(line)
	if err != nil || len(events) == 0 {
		return "", "", false
	}
	last := events[len(events)-1]
	switch last.Kind {
	case history.EventThinking:
		return sessionStateThinking, "", true
	case history.EventToolUse:
		if isAskTool(last.ToolUse.Tool) {
			return sessionStateAsk, "", true
		}
		return sessionStateWorking, "", true
	case history.EventError:
		return sessionStateError, sessionErrorText(last), true
	case history.EventTurnEnd:
		return sessionStateCompleted, "", true
	case history.EventAssistantText:
		return sessionStateWorking, "", true
	default:
		return "", "", false
	}
}

// sessionErrorText renders the one-line reason for an API/network error event:
// the synthetic "API Error: …" message Claude Code records, falling back to its
// classification and HTTP status when the message text is absent.
func sessionErrorText(ev history.SessionEvent) string {
	if ev.Text != "" {
		return ev.Text
	}
	if ev.ErrorStatus > 0 {
		return fmt.Sprintf("API error %d (%s)", ev.ErrorStatus, ev.ErrorType)
	}
	if ev.ErrorType != "" {
		return "API error: " + ev.ErrorType
	}
	return "API error"
}

const (
	// sessionStatsTTL bounds how long a cold (disk-derived) stats entry is reused
	// before it is recomputed, even when the log's mtime has not changed.
	sessionStatsTTL = 5 * time.Second
	// sessionStatsMaxLive caps the live map; finished sessions are evicted
	// oldest-first beyond it so a long-lived dashboard does not grow unbounded.
	sessionStatsMaxLive = 128
	sessionStatsMaxLine = 10 * 1024 * 1024
)

// SessionStats is the rolled-up activity of one agent session: its identity
// (agent/model/effort), elapsed time, token usage and derived cost. It backs the
// dashboard's session timer in both the detail pane and the sidebar. Token totals
// are summed across every assistant turn in the session log; cost is derived from
// those totals via captain's pricing registry (best-effort — zero when the model
// is absent from the registry, mirroring the ai-fix context-window lookup).
type SessionStats struct {
	SessionID           string    `json:"sessionId,omitempty"`
	Agent               string    `json:"agent,omitempty"`
	Model               string    `json:"model,omitempty"`
	Effort              string    `json:"effort,omitempty"`
	StartedAt           time.Time `json:"startedAt,omitempty"`
	UpdatedAt           time.Time `json:"updatedAt,omitempty"`
	DurationMs          int64     `json:"durationMs"`
	InputTokens         int       `json:"inputTokens"`
	OutputTokens        int       `json:"outputTokens"`
	CacheReadTokens     int       `json:"cacheReadTokens"`
	CacheCreationTokens int       `json:"cacheCreationTokens"`
	TotalTokens         int       `json:"totalTokens"`
	// ContextTokens is the live context-window occupancy: the most recent
	// assistant turn's input + cache-read + cache-creation tokens (which a
	// compaction resets), as opposed to TotalTokens summing every turn. This is
	// what the dashboard surfaces as the token figure.
	ContextTokens int `json:"contextTokens"`
	// ContextWindow is the model's total context-window size (tokens), looked up
	// from captain's pricing registry, so the dashboard can render ContextTokens
	// as a fraction of capacity. Zero when the model is absent from the registry.
	ContextWindow int `json:"contextWindow"`
	Turns         int `json:"turns"`
	// Compactions counts the context compactions seen so far (Claude's
	// `compact_boundary` markers); each one shrinks ContextTokens.
	Compactions int     `json:"compactions"`
	CostUSD     float64 `json:"costUsd"`
	InProgress  bool    `json:"inProgress"`
	Found       bool    `json:"found"`
	// State is the high-level agent state from the most recent session-log event
	// (thinking / working / ask / completed / error); empty before the first event.
	State string `json:"state,omitempty"`
	// Error is the API/network failure reason when State == "error" — the synthetic
	// "API Error: …" message Claude Code records when a request fails after retries.
	Error string `json:"error,omitempty"`
}

// sessionLogLine is the subset of a Claude session-log entry needed for stats:
// the per-request token usage and model carried on each assistant turn, plus the
// `compact_boundary` marker that resets the context window.
type sessionLogLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	// CompactMetadata is present only on `compact_boundary` system entries; its
	// postTokens is the context size that survived the compaction.
	CompactMetadata struct {
		PostTokens int `json:"postTokens"`
	} `json:"compactMetadata"`
}

func (l sessionLogLine) hasUsage() bool {
	u := l.Message.Usage
	return u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0
}

// isCompaction reports whether this line is a context-compaction boundary, which
// shrinks the context window and counts toward SessionStats.Compactions.
func (l sessionLogLine) isCompaction() bool {
	return l.Type == "system" && l.Subtype == "compact_boundary"
}

// applyUsage folds one assistant entry's usage into the running totals and snaps
// the live context window to this turn's prompt size (input + cache).
func (s *SessionStats) applyUsage(l sessionLogLine) {
	u := l.Message.Usage
	s.InputTokens += u.InputTokens
	s.OutputTokens += u.OutputTokens
	s.CacheReadTokens += u.CacheReadInputTokens
	s.CacheCreationTokens += u.CacheCreationInputTokens
	s.ContextTokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	s.Turns++
	if l.Message.Model != "" {
		s.Model = l.Message.Model
	}
}

// applyCompaction records a compaction boundary: it bumps the count and snaps the
// context window down to the post-compaction size the metadata reports.
func (s *SessionStats) applyCompaction(l sessionLogLine) {
	s.Compactions++
	if l.CompactMetadata.PostTokens > 0 {
		s.ContextTokens = l.CompactMetadata.PostTokens
	}
}

// finalize derives the totals, elapsed duration, and cost from the accumulated
// token counts and timestamps. The model is fixed within a session, so summing
// per-turn cost equals computing cost once from the totals.
func (s *SessionStats) finalize() {
	s.TotalTokens = s.InputTokens + s.OutputTokens + s.CacheReadTokens + s.CacheCreationTokens
	if !s.StartedAt.IsZero() && !s.UpdatedAt.IsZero() && s.UpdatedAt.After(s.StartedAt) {
		s.DurationMs = s.UpdatedAt.Sub(s.StartedAt).Milliseconds()
	}
	s.CostUSD = sessionCost(s.Model, s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheCreationTokens)
	if info, ok := modelInfo(s.Model); ok {
		s.ContextWindow = info.ContextWindow
	}
}

// executingRecency bounds how recently a cold (no live tailer) session must have
// advanced to still count as executing again. A run killed mid-turn leaves an
// active state but its log stops growing, so past this window the session settles
// back to its real (e.g. failed) status instead of appearing to run forever.
const executingRecency = 60 * time.Second

// Executing reports whether the session is doing work right now: a live tailer is
// feeding it (InProgress), or — for a session gavel is not tailing — its on-disk
// log is in an active state (thinking/working/ask) and advanced within
// executingRecency. It lets a todo whose recorded session starts running again (a
// resume, or a continued cmux REPL) surface as in-progress instead of keeping a
// stale terminal status. now is passed in so callers can test without a clock.
func (s SessionStats) Executing(now time.Time) bool {
	if !s.Found {
		return false
	}
	if s.InProgress {
		return true
	}
	switch s.State {
	case sessionStateThinking, sessionStateWorking, sessionStateAsk:
		return !s.UpdatedAt.IsZero() && now.Sub(s.UpdatedAt) < executingRecency
	}
	return false
}

// modelVersionRe matches a Claude session-log model id whose version uses hyphens
// (e.g. "claude-opus-4-8" or "claude-sonnet-4-5-20250929") so it can be rewritten
// to the dotted form the pricing registry is keyed by ("claude-opus-4.8"). The
// optional trailing date suffix is dropped.
var modelVersionRe = regexp.MustCompile(`^(claude-[a-z]+)-(\d+)-(\d+)(?:-\d{6,})?$`)

// normalizeModelID rewrites a bare Claude session-log model id to the dotted
// version the pricing registry uses; ids that don't match are returned unchanged.
func normalizeModelID(model string) string {
	if m := modelVersionRe.FindStringSubmatch(model); m != nil {
		return m[1] + "-" + m[2] + "." + m[3]
	}
	return model
}

// modelIDCandidates lists the registry keys to try for a session-log model id:
// the bare id, the OpenRouter "anthropic/" prefix, and the dotted-version
// normalization that reconciles log ids ("claude-opus-4-8") with registry keys
// ("anthropic/claude-opus-4.8"). Shared by the cost and context-window lookups.
func modelIDCandidates(model string) []string {
	norm := normalizeModelID(model)
	ids := []string{model, "anthropic/" + model}
	if norm != model {
		ids = append(ids, norm, "anthropic/"+norm)
	}
	return ids
}

// modelInfo resolves a session-log model id to its pricing registry entry,
// returning false when no candidate id matches.
func modelInfo(model string) (pricing.ModelInfo, bool) {
	if model == "" {
		return pricing.ModelInfo{}, false
	}
	for _, id := range modelIDCandidates(model) {
		if info, ok := pricing.GetModelInfo(id); ok {
			return info, true
		}
	}
	return pricing.ModelInfo{}, false
}

// sessionCost prices the session's tokens via captain's pricing registry. Claude
// session logs report bare hyphenated model ids (e.g. "claude-opus-4-8") while the
// registry is keyed by dotted OpenRouter ids ("anthropic/claude-opus-4.8"), so
// every candidate form is tried. An unknown model yields zero cost rather than
// failing — pricing is optional enrichment, not a correctness invariant.
func sessionCost(model string, in, out, cacheRead, cacheWrite int) float64 {
	if model == "" {
		return 0
	}
	for _, id := range modelIDCandidates(model) {
		if res, err := pricing.CalculateCost(id, in, out, 0, cacheRead, cacheWrite); err == nil {
			return res.TotalCost
		}
	}
	return 0
}

func parseSessionTime(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t
	}
	return time.Time{}
}

// computeSessionStats reads a complete session log from disk and rolls up its
// token usage, model, and first/last timestamps. Used for cold (non-in-progress)
// sessions the live tailers never observed.
func computeSessionStats(path string) (SessionStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionStats{}, err
	}
	defer func() { _ = f.Close() }()

	stats := SessionStats{Found: true, Agent: "claude"}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), sessionStatsMaxLine)
	var first, last time.Time
	for scanner.Scan() {
		if state, errMsg, ok := sessionStateFromLine(scanner.Bytes()); ok {
			stats.State = state
			stats.Error = errMsg
		}
		var entry sessionLogLine
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if ts := parseSessionTime(entry.Timestamp); !ts.IsZero() {
			if first.IsZero() || ts.Before(first) {
				first = ts
			}
			if ts.After(last) {
				last = ts
			}
		}
		switch {
		case entry.isCompaction():
			stats.applyCompaction(entry)
		case entry.Type == "assistant" && entry.hasUsage():
			stats.applyUsage(entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return SessionStats{}, err
	}
	stats.StartedAt = first
	stats.UpdatedAt = last
	stats.finalize()
	return stats, nil
}

// SessionAccumulator is the live, in-progress stats for one running session,
// fed a line at a time by the cmux tailer. It is registered in the cache so the
// dashboard reads live totals without re-reading the growing log on every poll.
type SessionAccumulator struct {
	mu    sync.Mutex
	stats SessionStats
}

// AddLine folds one raw session-log line into the running stats: its state (from
// the line's last event) and, for assistant turns, its token usage. Safe to use
// as the tailer's onLine hook; it never retains the slice.
func (a *SessionAccumulator) AddLine(line []byte) {
	state, errMsg, hasState := sessionStateFromLine(line)

	var entry sessionLogLine
	parsed := json.Unmarshal(line, &entry) == nil
	isCompaction := parsed && entry.isCompaction()
	hasUsage := parsed && entry.Type == "assistant" && entry.hasUsage()
	if !hasState && !hasUsage && !isCompaction {
		return
	}
	a.mu.Lock()
	if hasState {
		a.stats.State = state
		a.stats.Error = errMsg
	}
	if isCompaction {
		a.stats.applyCompaction(entry)
		a.stats.UpdatedAt = time.Now()
	}
	if hasUsage {
		a.stats.applyUsage(entry)
		a.stats.UpdatedAt = time.Now()
	}
	a.mu.Unlock()
}

// Finish marks the session no longer in progress, freezing its elapsed duration.
func (a *SessionAccumulator) Finish() {
	a.mu.Lock()
	a.stats.InProgress = false
	if a.stats.UpdatedAt.IsZero() {
		a.stats.UpdatedAt = time.Now()
	}
	a.mu.Unlock()
}

// snapshot returns a finalized copy. While in progress the elapsed clock runs to
// now so the dashboard timer keeps ticking between polls.
func (a *SessionAccumulator) snapshot() SessionStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.stats
	s.Found = true
	if s.InProgress {
		s.UpdatedAt = time.Now()
	}
	s.finalize()
	return s
}

func (a *SessionAccumulator) finished() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.stats.InProgress
}

// lastActivity returns the wall-clock time of the most recent token-usage or
// compaction line folded in, without the snapshot's in-progress now() override,
// so the stall watchdog can tell whether the session log is actually advancing.
func (a *SessionAccumulator) lastActivity() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats.UpdatedAt
}

// state returns the high-level agent state from the most recent session-log event
// (thinking / working / ask / …), used by the stall watchdog to suppress nudges
// while the turn is paused awaiting the user.
func (a *SessionAccumulator) state() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats.State
}

// SetState overrides the live agent state. The cmux stall watchdog uses it to
// surface a tool-permission dialog as "ask" (awaiting input): claude renders the
// approval prompt only on the terminal, so no session-log event marks it and the
// state would otherwise stay "working" while the run is actually paused on a human.
func (a *SessionAccumulator) SetState(state string) {
	a.mu.Lock()
	a.stats.State = state
	a.mu.Unlock()
}

func (a *SessionAccumulator) startedAt() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats.StartedAt
}

// SessionStatsCache holds the live in-progress sessions (pushed by the tailers)
// and a cold cache of disk-derived stats for sessions no tailer is watching.
type SessionStatsCache struct {
	mu   sync.Mutex
	live map[string]*SessionAccumulator
	cold map[string]coldStatsEntry
}

type coldStatsEntry struct {
	stats      SessionStats
	mtime      time.Time
	computedAt time.Time
}

func NewSessionStatsCache() *SessionStatsCache {
	return &SessionStatsCache{
		live: map[string]*SessionAccumulator{},
		cold: map[string]coldStatsEntry{},
	}
}

// Begin registers a live session and returns the accumulator the tailer feeds.
// agent/model/effort are the run's configured values; model is later refined to
// the concrete id reported in the log as turns arrive.
func (c *SessionStatsCache) Begin(sessionID, agent, model, effort string, start time.Time) *SessionAccumulator {
	acc := &SessionAccumulator{stats: SessionStats{
		SessionID:  sessionID,
		Agent:      agent,
		Model:      model,
		Effort:     effort,
		StartedAt:  start,
		UpdatedAt:  start,
		InProgress: true,
	}}
	c.mu.Lock()
	c.live[sessionID] = acc
	c.evictLiveLocked()
	c.mu.Unlock()
	return acc
}

// Get returns the stats for a session: the live accumulator when one is present,
// otherwise a cold read of the on-disk log cached by mtime + TTL. A missing log
// (session never produced output) is the normal "not found" state, returned as a
// zero SessionStats with Found=false rather than an error.
func (c *SessionStatsCache) Get(sessionID, path string) (SessionStats, error) {
	c.mu.Lock()
	acc := c.live[sessionID]
	c.mu.Unlock()
	if acc != nil {
		return acc.snapshot(), nil
	}
	return c.coldStats(sessionID, path)
}

func (c *SessionStatsCache) coldStats(sessionID, path string) (SessionStats, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SessionStats{}, nil
		}
		return SessionStats{}, err
	}

	c.mu.Lock()
	if entry, ok := c.cold[path]; ok && entry.mtime.Equal(info.ModTime()) && time.Since(entry.computedAt) < sessionStatsTTL {
		c.mu.Unlock()
		return entry.stats, nil
	}
	c.mu.Unlock()

	stats, err := computeSessionStats(path)
	if err != nil {
		return SessionStats{}, err
	}
	stats.SessionID = sessionID

	c.mu.Lock()
	c.cold[path] = coldStatsEntry{stats: stats, mtime: info.ModTime(), computedAt: time.Now()}
	c.mu.Unlock()
	return stats, nil
}

// evictLiveLocked drops the oldest finished sessions once the live map exceeds
// the cap; in-progress sessions are never evicted. Caller holds c.mu.
func (c *SessionStatsCache) evictLiveLocked() {
	if len(c.live) <= sessionStatsMaxLive {
		return
	}
	type aged struct {
		id    string
		start time.Time
	}
	finished := make([]aged, 0, len(c.live))
	for id, acc := range c.live {
		if acc.finished() {
			finished = append(finished, aged{id: id, start: acc.startedAt()})
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].start.Before(finished[j].start) })
	for _, f := range finished {
		if len(c.live) <= sessionStatsMaxLive {
			break
		}
		delete(c.live, f.id)
	}
}

// defaultSessionStats is the process-wide cache: the cmux executor (running
// in-process under the dashboard) feeds live sessions into it and the dashboard's
// stats endpoint reads from it.
var defaultSessionStats = NewSessionStatsCache()

// GlobalSessionStats returns the process-wide session stats cache.
func GlobalSessionStats() *SessionStatsCache { return defaultSessionStats }
