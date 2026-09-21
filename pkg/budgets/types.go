// Package budgets owns Captain's multidimensional budget rule catalog,
// attribution, and settled-spend queries.
package budgets

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/timberio/go-datemath"
)

// RuleMatch selects requests by host dimensions and resolved model identity.
type RuleMatch struct {
	Dimensions map[string]string `json:"dimensions,omitempty" yaml:"dimensions,omitempty"`
	Models     []string          `json:"models,omitempty" yaml:"models,omitempty"`
}

// Rule is a catalog budget rule with its source identity.
type Rule struct {
	ID        string     `json:"id" yaml:"-"`
	Key       string     `json:"key" yaml:"-"`
	Source    SourceInfo `json:"source" yaml:"-"`
	Name      string     `json:"name" yaml:"name"`
	Match     RuleMatch  `json:"match,omitempty" yaml:"match,omitempty"`
	GroupBy   []string   `json:"groupBy,omitempty" yaml:"groupBy,omitempty"`
	Amount    float64    `json:"amount" yaml:"amount"`
	Window    string     `json:"window" yaml:"window"`
	UpdatedAt time.Time  `json:"updatedAt" yaml:"-"`
}

// RuleInput is the one-rule-per-file YAML shape and catalog write contract.
type RuleInput struct {
	Name    string    `json:"name" yaml:"name"`
	Match   RuleMatch `json:"match,omitempty" yaml:"match,omitempty"`
	GroupBy []string  `json:"groupBy,omitempty" yaml:"groupBy,omitempty"`
	Amount  float64   `json:"amount" yaml:"amount"`
	Window  string    `json:"window" yaml:"window"`
}

var (
	ErrNotFound  = errors.New("budget rule not found")
	ErrAmbiguous = errors.New("budget rule name is ambiguous")
	ErrNameTaken = errors.New("budget rule name is already taken")
	ErrReadOnly  = errors.New("budget source is read-only")
	ErrInvalid   = errors.New("invalid budget rule")
)

func normalizeInput(input RuleInput) (RuleInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Window = strings.TrimSpace(input.Window)
	if input.Name == "" || input.Amount <= 0 || input.Window == "" {
		return RuleInput{}, fmt.Errorf("%w: name, positive amount, and window are required", ErrInvalid)
	}
	if _, err := windowStart(input.Window, time.Now().UTC()); err != nil {
		return RuleInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	dimensions := make(map[string]string, len(input.Match.Dimensions))
	for key, pattern := range input.Match.Dimensions {
		key, pattern = strings.TrimSpace(key), strings.TrimSpace(pattern)
		if key == "" || pattern == "" {
			return RuleInput{}, fmt.Errorf("%w: dimension keys and patterns cannot be blank", ErrInvalid)
		}
		dimensions[key] = pattern
	}
	input.Match.Dimensions = dimensions
	input.Match.Models = cleanList(input.Match.Models)
	groupBy := make([]string, 0, len(input.GroupBy))
	for _, key := range input.GroupBy {
		key = strings.TrimSpace(key)
		if key == "" {
			return RuleInput{}, fmt.Errorf("%w: groupBy keys cannot be blank", ErrInvalid)
		}
		groupBy = append(groupBy, key)
	}
	input.GroupBy = groupBy
	seen := map[string]bool{}
	for _, key := range input.GroupBy {
		if seen[key] {
			return RuleInput{}, fmt.Errorf("%w: groupBy repeats %q", ErrInvalid, key)
		}
		seen[key] = true
	}
	return input, nil
}

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func windowStart(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "now") {
		return time.Time{}, fmt.Errorf("budget window %q must be relative to now", raw)
	}
	start, err := datemath.ParseAndEvaluate(raw, datemath.WithNow(now.UTC()), datemath.WithLocation(time.UTC))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid budget window %q: %w", raw, err)
	}
	start = start.UTC()
	if start.After(now.UTC()) {
		return time.Time{}, fmt.Errorf("budget window %q starts after now", raw)
	}
	return start, nil
}

func ruleMatches(rule Rule, dimensions map[string]string, model api.Model) bool {
	for key, pattern := range rule.Match.Dimensions {
		value, ok := dimensions[key]
		if !ok || !(api.MatchPatterns{pattern}).Matches(value) {
			return false
		}
	}
	return modelMatches(rule.Match.Models, model)
}

func modelMatches(patterns []string, model api.Model) bool {
	if len(patterns) == 0 {
		return true
	}
	positive, matched := false, false
	for _, expression := range patterns {
		for _, pattern := range strings.Split(expression, ",") {
			pattern = strings.TrimSpace(pattern)
			negative := strings.HasPrefix(pattern, "!")
			if negative {
				pattern = strings.TrimSpace(strings.TrimPrefix(pattern, "!"))
			} else {
				positive = true
			}
			if selectorMatches(pattern, model) {
				if negative {
					return false
				}
				matched = true
			}
		}
	}
	return matched || !positive
}

func selectorMatches(pattern string, model api.Model) bool {
	if api.ModelSelectorMatches(pattern, model) {
		return true
	}
	identity := api.RuntimeIdentityOf(model)
	items := []string{
		model.Name, model.ID, identity.Provider + "/" + identity.Model,
		string(identity.Mode) + ":" + identity.Model,
		string(identity.Mode) + ":" + identity.Provider + "/" + identity.Model,
	}
	for _, item := range items {
		if item != "" && (api.MatchPatterns{pattern}).Matches(item) {
			return true
		}
	}
	return false
}
