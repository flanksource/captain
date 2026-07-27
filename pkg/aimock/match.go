// ABOUTME: Matcher predicates and the ordered first-unconsumed-match-wins rule store.
// ABOUTME: Consumption is what lets turn N of an agent loop receive reply N.

package aimock

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
)

// Unlimited marks a rule that never exhausts, for a reply that should serve
// every matching request (a catch-all, or a side-call like title generation).
const Unlimited = -1

// Match is the predicate half of a rule. Every field that is set must hold for
// the rule to fire; an empty Match matches everything.
type Match struct {
	PromptContains string            `json:"prompt_contains,omitempty" yaml:"prompt_contains,omitempty"`
	PromptRegex    string            `json:"prompt_regex,omitempty" yaml:"prompt_regex,omitempty"`
	SystemContains string            `json:"system_contains,omitempty" yaml:"system_contains,omitempty"`
	Model          string            `json:"model,omitempty" yaml:"model,omitempty"`
	ToolResultFor  string            `json:"tool_result_for,omitempty" yaml:"tool_result_for,omitempty"`
	Header         map[string]string `json:"header,omitempty" yaml:"header,omitempty"`

	compiled *regexp.Regexp
}

// Validate compiles the regex matcher so a malformed pattern fails at scenario
// load rather than on the first request.
func (m *Match) Validate() error {
	if m.PromptRegex == "" {
		return nil
	}
	re, err := regexp.Compile(m.PromptRegex)
	if err != nil {
		return fmt.Errorf("prompt_regex %q: %w", m.PromptRegex, err)
	}
	m.compiled = re
	return nil
}

// Matches reports whether every set predicate holds for req.
func (m *Match) Matches(req Request) bool {
	if m.PromptContains != "" && !strings.Contains(req.LastUserText(), m.PromptContains) {
		return false
	}
	if m.compiled != nil && !m.compiled.MatchString(req.LastUserText()) {
		return false
	}
	if m.SystemContains != "" && !strings.Contains(req.System, m.SystemContains) {
		return false
	}
	if m.Model != "" && !modelMatches(m.Model, req.Model) {
		return false
	}
	if m.ToolResultFor != "" && !containsFold(req.ToolResultNames(), m.ToolResultFor) {
		return false
	}
	for key, want := range m.Header {
		if req.Headers[strings.ToLower(key)] != want {
			return false
		}
	}
	return true
}

// Describe renders the matcher for the miss diagnostic. Only set fields appear.
func (m *Match) Describe() string {
	var parts []string
	for _, kv := range [][2]string{
		{"prompt_contains", m.PromptContains},
		{"prompt_regex", m.PromptRegex},
		{"system_contains", m.SystemContains},
		{"model", m.Model},
		{"tool_result_for", m.ToolResultFor},
	} {
		if kv[1] != "" {
			parts = append(parts, fmt.Sprintf("%s=%q", kv[0], kv[1]))
		}
	}
	for key, want := range m.Header {
		parts = append(parts, fmt.Sprintf("header[%s]=%q", key, want))
	}
	if len(parts) == 0 {
		return "<matches anything>"
	}
	return strings.Join(parts, " ")
}

// modelMatches compares a glob pattern against the requested model. A bare name
// also matches any provider-prefixed form, so `sonnet` matches
// `claude-sonnet-5` the way the CLIs normalize model names.
func modelMatches(pattern, model string) bool {
	if pattern == model {
		return true
	}
	if ok, err := path.Match(pattern, model); err == nil && ok {
		return true
	}
	return strings.Contains(model, pattern)
}

func containsFold(haystack []string, needle string) bool {
	for _, item := range haystack {
		if strings.EqualFold(item, needle) {
			return true
		}
	}
	return false
}

// Rule pairs a predicate with a protocol-specific response. Repeat bounds how
// many times it may fire: 0 means the default of 1, Unlimited never exhausts.
type Rule[T any] struct {
	Match   Match `json:"match,omitempty" yaml:"match,omitempty"`
	Respond T     `json:"respond" yaml:"respond"`
	Repeat  int   `json:"repeat,omitempty" yaml:"repeat,omitempty"`
}

// Rules is an ordered, concurrency-safe rule set with first-unconsumed-match-wins
// selection. Exhausted rules are skipped, so successive requests in an agent
// loop walk forward through the scenario instead of re-firing rule one.
type Rules[T any] struct {
	mu       sync.Mutex
	rules    []Rule[T]
	used     []int
	fallback *T
}

// NewRules builds a rule set. fallback, when non-nil, answers any request that
// no rule claims; without it a miss is an error (see ErrNoMatch).
func NewRules[T any](rules []Rule[T], fallback *T) *Rules[T] {
	return &Rules[T]{rules: rules, used: make([]int, len(rules)), fallback: fallback}
}

// ErrNoMatch reports a request that matched no unconsumed rule and had no
// fallback. Its message names the request and every remaining matcher, so the
// diagnostic says what was asked for and what was on offer.
type ErrNoMatch struct {
	Request   Request
	Remaining []string
}

func (e *ErrNoMatch) Error() string {
	remaining := "none (every rule consumed)"
	if len(e.Remaining) > 0 {
		remaining = "\n  - " + strings.Join(e.Remaining, "\n  - ")
	}
	return fmt.Sprintf("aimock: no scenario rule matched this request\n  model: %s\n  last user message: %s\n  unconsumed rules: %s",
		e.Request.Model, truncate(e.Request.LastUserText(), 500), remaining)
}

// Next returns the response for req, consuming the rule that fired. A miss
// returns *ErrNoMatch unless a fallback was configured.
func (r *Rules[T]) Next(req Request) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range r.rules {
		if r.exhausted(i) || !r.rules[i].Match.Matches(req) {
			continue
		}
		r.used[i]++
		return r.rules[i].Respond, nil
	}
	if r.fallback != nil {
		return *r.fallback, nil
	}
	var zero T
	return zero, &ErrNoMatch{Request: req, Remaining: r.remainingLocked()}
}

// Remaining describes every rule that can still fire — the assertion surface
// for "did the scenario play out fully?".
func (r *Rules[T]) Remaining() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.remainingLocked()
}

func (r *Rules[T]) remainingLocked() []string {
	var out []string
	for i := range r.rules {
		if !r.exhausted(i) {
			out = append(out, fmt.Sprintf("[%d] %s", i, r.rules[i].Match.Describe()))
		}
	}
	return out
}

func (r *Rules[T]) exhausted(i int) bool {
	limit := r.rules[i].Repeat
	if limit == Unlimited {
		return false
	}
	if limit == 0 {
		limit = 1
	}
	return r.used[i] >= limit
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
