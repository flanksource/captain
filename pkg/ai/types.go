package ai

import (
	"net/http"
	"time"
)

type Request struct {
	SystemPrompt     string
	Prompt           string
	MaxTokens        int
	Temperature      float64
	StructuredOutput any               // nil = text mode, non-nil = JSON schema target
	Metadata         map[string]string // arbitrary caller metadata
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
	Backend       Backend       // empty = infer from model
	APIKey        string        // empty = env lookup
	APIURL        string
	HTTPClient    *http.Client  // nil = default client
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
