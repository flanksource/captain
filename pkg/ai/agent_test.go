package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	model   string
	text    string
	cost    float64
	calls   int
	err     error
	closed  bool
	lastReq Request
	outcome *TerminalOutcome
}

func (m *mockProvider) GetModel() string    { return m.model }
func (m *mockProvider) GetRuntime() Runtime { return RuntimeOf(Anthropic, ModeAPI) }
func (m *mockProvider) Close() error        { m.closed = true; return nil }
func (m *mockProvider) Execute(_ context.Context, req Request) (*Response, error) {
	m.lastReq = req
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return &Response{
		Text:            m.text + ":" + req.Prompt.User,
		Model:           m.model,
		Runtime:         RuntimeOf(Anthropic, ModeAPI),
		Usage:           Usage{InputTokens: 10, OutputTokens: 5},
		CostUSD:         m.cost,
		TerminalOutcome: m.outcome,
	}, nil
}

func TestAgent_ExecutePromptCarriesTerminalOutcome(t *testing.T) {
	outcome := &TerminalOutcome{Kind: TerminalOutcomePlan, Plan: &TerminalPlan{Content: "1. Inspect"}}
	a := NewAgentWithProvider(&mockProvider{model: "m", outcome: outcome}, Config{Model: api.Model{Name: "m"}})

	resp, err := a.ExecutePrompt(context.Background(), PromptRequest{Name: "plan", Spec: api.Spec{Prompt: api.Prompt{User: "plan this"}}})
	require.NoError(t, err)
	assert.Same(t, outcome, resp.TerminalOutcome)
}

func TestAgent_ExecutePromptAccruesCost(t *testing.T) {
	a := NewAgentWithProvider(&mockProvider{model: "test-model", text: "out"}, Config{Model: api.Model{Name: "test-model"}})

	resp, err := a.ExecutePrompt(context.Background(), PromptRequest{Name: "p1", Spec: api.Spec{Prompt: api.Prompt{User: "hi"}}})
	require.NoError(t, err)
	assert.Equal(t, "out:hi", resp.Result)
	assert.Equal(t, "test-model", resp.Model)
	require.Len(t, resp.Costs, 1)
	assert.Equal(t, 10, resp.Costs[0].InputTokens)
	assert.Equal(t, 15, resp.Costs[0].TotalTokens)

	costs := a.GetCosts()
	require.Len(t, costs, 1)
	assert.Equal(t, "test-model", costs[0].Model)
}

func TestAgent_ExecutePromptEnforcesBudget(t *testing.T) {
	mp := &mockProvider{model: "test-model", text: "out", cost: 0.08}
	a := NewAgentWithProvider(mp, Config{
		Model:  api.Model{Name: "test-model"},
		Budget: api.Budget{Cost: 0.10},
	})

	// First two calls fit under the $0.10 budget (spend 0 → 0.08 → 0.16).
	_, err := a.ExecutePrompt(context.Background(), PromptRequest{Name: "p1", Spec: api.Spec{Prompt: api.Prompt{User: "a"}}})
	require.NoError(t, err)
	_, err = a.ExecutePrompt(context.Background(), PromptRequest{Name: "p2", Spec: api.Spec{Prompt: api.Prompt{User: "b"}}})
	require.NoError(t, err, "second call still under budget at pre-flight (spent 0.08)")

	// Third call trips the pre-flight (spent 0.16 ≥ 0.10) and must not execute.
	callsBefore := mp.calls
	resp, err := a.ExecutePrompt(context.Background(), PromptRequest{Name: "p3", Spec: api.Spec{Prompt: api.Prompt{User: "c"}}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBudgetExceeded)
	assert.Equal(t, callsBefore, mp.calls, "provider must not be invoked once budget is exceeded")
	assert.Equal(t, err.Error(), resp.Error)
	assert.InDelta(t, 0.16, a.TotalCost(), 1e-9, "TotalCost prefers provider-reported cost")
}

func TestAgent_ExecutePromptForwardsSchemaJSON(t *testing.T) {
	mp := &mockProvider{model: "m", text: "out"}
	a := NewAgentWithProvider(mp, Config{Model: api.Model{Name: "m"}})

	schema := json.RawMessage(`{"type":"object","required":["pass"]}`)
	_, err := a.ExecutePrompt(context.Background(), PromptRequest{Name: "p", Spec: api.Spec{Prompt: api.Prompt{User: "hi", SchemaJSON: schema}}})
	require.NoError(t, err)
	assert.JSONEq(t, string(schema), string(mp.lastReq.Prompt.SchemaJSON),
		"PromptRequest.Spec.Prompt.SchemaJSON must be forwarded to the provider request")
}

func TestAgent_ExecutePromptError(t *testing.T) {
	a := NewAgentWithProvider(&mockProvider{model: "m", err: fmt.Errorf("boom")}, Config{Model: api.Model{Name: "m"}})
	resp, err := a.ExecutePrompt(context.Background(), PromptRequest{Name: "p", Spec: api.Spec{Prompt: api.Prompt{User: "x"}}})
	require.Error(t, err)
	assert.False(t, resp.IsOK())
	assert.Contains(t, resp.Error, "boom")
	assert.Empty(t, a.GetCosts(), "failed calls accrue no cost")
}

func TestAgent_ExecuteBatchKeyedByName(t *testing.T) {
	a := NewAgentWithProvider(&mockProvider{model: "m", text: "r"}, Config{Model: api.Model{Name: "m"}, MaxConcurrent: 2})
	reqs := []PromptRequest{
		{Name: "a", Spec: api.Spec{Prompt: api.Prompt{User: "1"}}},
		{Name: "b", Spec: api.Spec{Prompt: api.Prompt{User: "2"}}},
		{Name: "c", Spec: api.Spec{Prompt: api.Prompt{User: "3"}}},
	}
	got, err := a.ExecuteBatch(context.Background(), reqs)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "r:1", got["a"].Result)
	assert.Equal(t, "r:2", got["b"].Result)
	assert.Equal(t, "r:3", got["c"].Result)
	assert.Len(t, a.GetCosts(), 3)
}

func TestAgent_Close(t *testing.T) {
	mp := &mockProvider{model: "m"}
	a := NewAgentWithProvider(mp, Config{Model: api.Model{Name: "m"}})
	require.NoError(t, a.Close())
	assert.True(t, mp.closed)
}
