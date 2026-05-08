package ai

import (
	"net/http"
	"time"
)

type Request struct {
	SystemPrompt       string
	AppendSystemPrompt string            // claude --append-system-prompt
	Prompt             string
	MaxTokens          int
	Temperature        float64
	StructuredOutput   any               // nil = text mode, non-nil = JSON schema target
	Metadata           map[string]string // arbitrary caller metadata

	// Per-request CLI knobs honoured by ExecuteStream-capable providers
	// (currently claude_cli). Zero values are equivalent to "let the
	// provider/CLI use its default" so existing buffered Execute callers
	// stay byte-identical.
	SessionID       string // resume an existing session (claude --session-id)
	PermissionMode  string // claude --permission-mode (e.g. "acceptEdits")
	StrictMCP       bool   // claude --strict-mcp-config
	Verbose         bool   // claude --verbose (required for stream-json)
	MaxTurns        int    // claude --max-turns (0 = omit, let CLI default)
	ReasoningEffort string // codex -c model_reasoning_effort=... ("low" | "medium" | "high"); other providers ignore

	// Safety / sandbox knobs. Zero values mean "use provider/CLI default".
	// Each provider translates what it understands; unknowns are ignored or
	// surfaced as a config error.
	Edit            bool     // shorthand: acceptEdits + curated Read/Edit/Write/Glob/Grep allowlist
	AllowedTools    []string // claude --allowedTools / codex: not supported
	DisallowedTools []string // claude --disallowedTools / codex: not supported
	NoMCP           bool     // claude --strict-mcp-config + empty inline / codex -c mcp_servers={}
	NoHooks         bool     // claude: requires --bare or --setting-sources / codex --ignore-rules
	NoSkills        bool     // claude --disable-slash-commands / codex --ignore-rules (best effort)
	SkillDirs       []string // claude --plugin-dir (repeatable)
	NoUser          bool     // claude --setting-sources without "user" / codex --ignore-user-config
	NoProject       bool     // claude --setting-sources without "project,local" / codex --ignore-rules
	NoMemory        bool     // claude: requires --bare / codex --ephemeral
	Bare            bool     // claude --bare / codex composite (--ignore-user-config --ignore-rules --ephemeral)
}

type Response struct {
	Text           string
	StructuredData any
	Model          string
	Backend        Backend
	Usage          Usage
	Duration       time.Duration
	CacheHit       bool
	Raw            any
}

type Usage struct {
	InputTokens      int
	OutputTokens     int
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
}

func (u Usage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens + u.ReasoningTokens + u.CacheReadTokens + u.CacheWriteTokens
}

type EventKind string

const (
	EventText     EventKind = "text"
	EventThinking EventKind = "thinking"
	EventToolUse  EventKind = "tool_use"
	EventResult   EventKind = "result"
	EventError    EventKind = "error"
	EventSystem   EventKind = "system"
)

type Event struct {
	Kind      EventKind
	Text      string
	Tool      string         // when Kind == EventToolUse
	Input     map[string]any // when Kind == EventToolUse
	Usage     *Usage         // when Kind == EventResult
	CostUSD   float64        // when Kind == EventResult
	Success   bool           // when Kind == EventResult
	SessionID string         // when Kind == EventSystem
	Model     string
	Error     string // when Kind == EventError

	// Raw carries the backend-native event (e.g. claude.HistoryEntry for the
	// claude_cli stream) so renderers can use the rich pretty-printers in
	// pkg/claude/tools instead of reformatting from Tool/Input.
	Raw any
}

type Cost struct {
	Model        string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	InputCost    float64
	OutputCost   float64
}

func (c Cost) Total() float64 { return c.InputCost + c.OutputCost }

func (c Cost) Add(other Cost) Cost {
	return Cost{
		Model:        c.Model,
		InputTokens:  c.InputTokens + other.InputTokens,
		OutputTokens: c.OutputTokens + other.OutputTokens,
		TotalTokens:  c.TotalTokens + other.TotalTokens,
		InputCost:    c.InputCost + other.InputCost,
		OutputCost:   c.OutputCost + other.OutputCost,
	}
}

type Costs []Cost

func (c Costs) Sum() Cost {
	var total Cost
	for _, cost := range c {
		total = total.Add(cost)
	}
	return total
}

func (c Costs) ByModel() map[string]Cost {
	m := make(map[string]Cost)
	for _, cost := range c {
		m[cost.Model] = m[cost.Model].Add(cost)
	}
	return m
}

type Config struct {
	Model         string
	Backend       Backend // empty = infer from model
	APIKey        string  // empty = env lookup
	APIURL        string
	HTTPClient    *http.Client // nil = default client
	MaxTokens     int
	Temperature   float64
	CacheDBPath   string
	CacheTTL      time.Duration
	NoCache       bool
	MaxConcurrent int
	Debug         bool
	SessionID     string
	ProjectName   string
	BudgetUSD     float64 // 0 = no budget
}
