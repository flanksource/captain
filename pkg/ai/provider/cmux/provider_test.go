package cmux

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
)

func TestWithSchemaPrompt(t *testing.T) {
	// A raw JSON schema is appended to the prompt and the native fields cleared,
	// so the cmux run is a plain text turn that still asks for JSON.
	req := ai.Request{Prompt: api.Prompt{
		User:       "review the diff",
		SchemaJSON: []byte(`{"type":"object","required":["pass"]}`),
	}}
	got, schema, err := withSchemaPrompt(req)
	if err != nil {
		t.Fatalf("withSchemaPrompt: %v", err)
	}
	if got.Prompt.Schema != nil || got.Prompt.SchemaJSON != nil {
		t.Errorf("native schema fields must be cleared, got Schema=%v SchemaJSON=%s", got.Prompt.Schema, got.Prompt.SchemaJSON)
	}
	if string(schema) != string(req.Prompt.SchemaJSON) {
		t.Errorf("preserved schema = %s, want %s", schema, req.Prompt.SchemaJSON)
	}
	if !strings.Contains(got.Prompt.User, "review the diff") {
		t.Errorf("original prompt lost: %q", got.Prompt.User)
	}
	if !strings.Contains(got.Prompt.User, `"required":["pass"]`) {
		t.Errorf("schema not appended to prompt: %q", got.Prompt.User)
	}

	// A text-mode request is returned unchanged.
	plain := ai.Request{Prompt: api.Prompt{User: "hi"}}
	got, schema, err = withSchemaPrompt(plain)
	if err != nil {
		t.Fatalf("withSchemaPrompt(text): %v", err)
	}
	if got.Prompt.User != "hi" {
		t.Errorf("text prompt altered: %q", got.Prompt.User)
	}
	if len(schema) != 0 {
		t.Errorf("text prompt preserved unexpected schema: %s", schema)
	}
}

func TestValidatedStructuredData(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["pass"],"properties":{"pass":{"type":"boolean"}}}`)

	got, err := validatedStructuredData(schema, "Result:\n```json\n{\"pass\":true}\n```", nil)
	if err != nil {
		t.Fatalf("validatedStructuredData(valid) error = %v", err)
	}
	if string(got) != `{"pass":true}` {
		t.Fatalf("validatedStructuredData(valid) = %s", got)
	}

	if _, err := validatedStructuredData(schema, `{"pass":"yes"}`, nil); !errors.Is(err, ai.ErrSchemaValidation) {
		t.Fatalf("validatedStructuredData(invalid) error = %v, want ErrSchemaValidation", err)
	}

	outcome := &ai.TerminalOutcome{Kind: ai.TerminalOutcomePlan, Plan: &ai.TerminalPlan{Content: "1. Inspect"}}
	got, err = validatedStructuredData(schema, "not a schema envelope", outcome)
	if err != nil || got != nil {
		t.Fatalf("native terminal outcome must bypass schema extraction, got (%s, %v)", got, err)
	}
}

func TestNewDerivesAgentAndBackend(t *testing.T) {
	cases := []struct {
		name        string
		backend     api.Backend
		model       string
		wantModel   string
		wantAgent   string
		wantBackend api.Backend
	}{
		{"claude cmux", api.BackendClaudeCmux, "opus", "claude-opus-5", "claude", api.BackendClaudeCmux},
		{"codex cmux", api.BackendCodexCmux, "gpt-5", "gpt-5", "codex", api.BackendCodexCmux},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(ai.Config{Model: api.Model{Name: tc.model, Backend: tc.backend}})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if p.agent != tc.wantAgent {
				t.Fatalf("agent = %q, want %q", p.agent, tc.wantAgent)
			}
			if p.GetModel() != tc.wantModel {
				t.Fatalf("GetModel() = %q, want %q", p.GetModel(), tc.wantModel)
			}
			if p.GetBackend() != tc.wantBackend {
				t.Fatalf("GetBackend() = %q, want %q", p.GetBackend(), tc.wantBackend)
			}
		})
	}
}

func TestNewRejectsUnsupportedBackend(t *testing.T) {
	if _, err := New(ai.Config{Model: api.Model{Name: "claude", Backend: api.BackendClaudeAgent}}); err == nil {
		t.Fatal("New() error = nil, want an error for a non-cmux backend")
	}
}

func TestExecuteStreamRequiresPrompt(t *testing.T) {
	p, err := New(ai.Config{Model: api.Model{Name: "claude", Backend: api.BackendClaudeCmux}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := p.ExecuteStream(context.Background(), ai.Request{Prompt: api.Prompt{User: ""}}); err == nil {
		t.Fatal("ExecuteStream() error = nil, want a required-prompt error")
	}
}

func TestUsageFromStats(t *testing.T) {
	stats := SessionStats{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5, CacheCreationTokens: 7}
	got := usageFromStats(stats)
	want := ai.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5, CacheWriteTokens: 7}
	if got != want {
		t.Fatalf("usageFromStats() = %+v, want %+v", got, want)
	}
}

func TestExecuteFailsLoudWhenCmuxUnavailable(t *testing.T) {
	// A runner that fails `ping` makes the whole run fail before any cmux surface is
	// created; Execute must surface that as a CLI execution failure, never success.
	p, err := New(ai.Config{Model: api.Model{Name: "claude", Backend: api.BackendClaudeCmux}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	r := newTestRun(runConfig{}, func(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
		return "", errors.New("cmux not running")
	})
	if _, _, err := p.execute(context.Background(), ai.Request{Prompt: api.Prompt{User: "hi"}, Setup: &shell.Setup{Cwd: t.TempDir()}}, r); err == nil {
		t.Fatal("execute() error = nil, want a failure when cmux ping fails")
	}
}
