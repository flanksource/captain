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
	err     error
	closed  bool
	lastReq Request
}

func (m *mockProvider) GetModel() string    { return m.model }
func (m *mockProvider) GetBackend() Backend { return BackendAnthropic }
func (m *mockProvider) Close() error        { m.closed = true; return nil }
func (m *mockProvider) Execute(_ context.Context, req Request) (*Response, error) {
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	return &Response{
		Text:    m.text + ":" + req.Prompt.User,
		Model:   m.model,
		Backend: BackendAnthropic,
		Usage:   Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

func TestAgent_ExecutePromptAccruesCost(t *testing.T) {
	a := NewAgentWithProvider(&mockProvider{model: "test-model", text: "out"}, Config{Model: api.Model{Name: "test-model"}})

	resp, err := a.ExecutePrompt(context.Background(), PromptRequest{Name: "p1", Prompt: "hi"})
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

func TestAgent_ExecutePromptForwardsSchemaJSON(t *testing.T) {
	mp := &mockProvider{model: "m", text: "out"}
	a := NewAgentWithProvider(mp, Config{Model: api.Model{Name: "m"}})

	schema := json.RawMessage(`{"type":"object","required":["pass"]}`)
	_, err := a.ExecutePrompt(context.Background(), PromptRequest{Name: "p", Prompt: "hi", SchemaJSON: schema})
	require.NoError(t, err)
	assert.JSONEq(t, string(schema), string(mp.lastReq.Prompt.SchemaJSON),
		"PromptRequest.SchemaJSON must be forwarded to the provider request")
}

func TestAgent_ExecutePromptError(t *testing.T) {
	a := NewAgentWithProvider(&mockProvider{model: "m", err: fmt.Errorf("boom")}, Config{Model: api.Model{Name: "m"}})
	resp, err := a.ExecutePrompt(context.Background(), PromptRequest{Name: "p", Prompt: "x"})
	require.Error(t, err)
	assert.False(t, resp.IsOK())
	assert.Contains(t, resp.Error, "boom")
	assert.Empty(t, a.GetCosts(), "failed calls accrue no cost")
}

func TestAgent_ExecuteBatchKeyedByName(t *testing.T) {
	a := NewAgentWithProvider(&mockProvider{model: "m", text: "r"}, Config{Model: api.Model{Name: "m"}, MaxConcurrent: 2})
	reqs := []PromptRequest{
		{Name: "a", Prompt: "1"},
		{Name: "b", Prompt: "2"},
		{Name: "c", Prompt: "3"},
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
