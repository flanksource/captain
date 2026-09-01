package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/logger"
)

// sampleOut is a structured-output target: Type is required (no omitempty) and
// carries a description, Note is optional — enough to exercise schema generation.
type sampleOut struct {
	Type string `json:"type" description:"conventional type"`
	Note string `json:"note,omitempty"`
}

type stubProvider struct{ resp *ai.Response }

func (s stubProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return s.resp, nil
}
func (s stubProvider) GetModel() string       { return "test-model" }
func (s stubProvider) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Anthropic, ai.ModeAPI) }

type streamingStubProvider struct{ stubProvider }

func (s streamingStubProvider) ExecuteStream(context.Context, ai.Request) (<-chan ai.Event, error) {
	events := make(chan ai.Event)
	close(events)
	return events, nil
}

func TestRuntimeLogIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mode   api.RuntimeMode
		model  string
		effort api.Effort
		want   string
	}{
		{"agent without effort", api.ModeAgent, "gpt-5.6-luna", api.EffortNone, "agent:gpt-5.6-luna"},
		{"agent with effort", api.ModeAgent, "gpt-5.6-sol", api.EffortMax, "agent:gpt-5.6-sol:max"},
		{"cli", api.ModeCLI, "claude-sonnet-5", api.EffortHigh, "cli:claude-sonnet-5:high"},
		{"cmux", api.ModeCmux, "gpt-5.6-terra", api.EffortUltra, "cmux:gpt-5.6-terra:ultra"},
		{"api", api.ModeAPI, "gpt-5.6", api.EffortLow, "api:gpt-5.6:low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ai.LogIdentity(tt.mode, tt.model, tt.effort); got != tt.want {
				t.Fatalf("ai.LogIdentity() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoggingProvider_UsesEffortQualifiedRuntimeIdentity(t *testing.T) {
	prev := logger.GetOutput()
	t.Cleanup(func() { logger.SetOutput(prev) })
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.GetLogger("ai").SetLogLevel("info")

	p, err := WithLogging()(stubProvider{resp: &ai.Response{Model: "gpt-5.6-sol"}})
	if err != nil {
		t.Fatalf("WithLogging: %v", err)
	}
	if _, err := p.Execute(context.Background(), ai.Request{
		Model:  api.Model{Name: "gpt-5.6-sol", Mode: api.ModeAgent, Effort: api.EffortMax},
		Prompt: api.Prompt{Source: "commit-grouping.prompt"},
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := ansi.ReplaceAllString(buf.String(), "")
	for _, want := range []string{
		"✨ agent:gpt-5.6-sol:max [commit-grouping.prompt]",
		"✓ agent:gpt-5.6-sol:max",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("logging middleware output missing %q\n--- output ---\n%s", want, got)
		}
	}
}

func TestLoggingProvider_StreamOmitsEmptyEffort(t *testing.T) {
	prev := logger.GetOutput()
	t.Cleanup(func() { logger.SetOutput(prev) })
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.GetLogger("ai").SetLogLevel("info")

	p, err := WithLogging()(streamingStubProvider{stubProvider{resp: &ai.Response{}}})
	if err != nil {
		t.Fatalf("WithLogging: %v", err)
	}
	streamer := p.(ai.StreamingProvider)
	if _, err := streamer.ExecuteStream(context.Background(), ai.Request{
		Model:  api.Model{Name: "gpt-5.6-luna", Mode: api.ModeAgent},
		Prompt: api.Prompt{Source: "commit-grouping.prompt"},
	}); err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}

	got := ansi.ReplaceAllString(buf.String(), "")
	want := "✨ agent:gpt-5.6-luna (stream) [commit-grouping.prompt]"
	if !strings.Contains(got, want) {
		t.Errorf("stream logging output missing %q\n--- output ---\n%s", want, got)
	}
	if strings.Contains(got, "luna:") {
		t.Errorf("stream logging unexpectedly added an effort suffix\n--- output ---\n%s", got)
	}
}

func TestSchemaInJSON(t *testing.T) {
	if got := schemaInJSON(api.Prompt{}); got != "" {
		t.Fatalf("text-mode (no schema): want empty, got %q", got)
	}

	got := schemaInJSON(api.Prompt{Schema: &sampleOut{}})
	for _, want := range []string{`"type":"object"`, `"description":"conventional type"`, `"required":["type"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("reflected schema-in %q missing %q", got, want)
		}
	}

	// A pre-built SchemaJSON is printed verbatim, preserving vocabulary the reflected
	// path can't express (e.g. maxItems) — the commit-grouping cap must be visible under -v.
	if got := schemaInJSON(api.Prompt{SchemaJSON: json.RawMessage(`{"type":"object","properties":{"groups":{"type":"array","maxItems":2}}}`)}); !strings.Contains(got, `"maxItems":2`) {
		t.Errorf("pre-built SchemaJSON should print verbatim, got %q", got)
	}

	if got := schemaInJSON(api.Prompt{Schema: "not-a-struct"}); !strings.Contains(got, "schema-in error") {
		t.Errorf("non-struct target: want inline error marker, got %q", got)
	}
}

func TestStructuredOutJSON(t *testing.T) {
	if got := structuredOutJSON(nil); got != "" {
		t.Fatalf("no structured data: want empty, got %q", got)
	}
	if got := structuredOutJSON(sampleOut{Type: "feat"}); !strings.Contains(got, `"type": "feat"`) {
		t.Errorf("schema-out %q missing rendered field", got)
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// TestLoggingProvider_EmitsSourceAndSchemas drives a request with a Source and a
// structured-output target through the logging middleware at trace level and
// asserts the captured output carries the prompt source, the rendered input, the
// schema-in, and the structured schema-out — the four things `gavel commit -vvvv`
// previously could not show.
func TestLoggingProvider_EmitsSourceAndSchemas(t *testing.T) {
	prev := logger.GetOutput()
	t.Cleanup(func() { logger.SetOutput(prev) })
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.GetLogger("ai").SetLogLevel("trace")

	p, err := WithLogging()(stubProvider{resp: &ai.Response{
		Text:           `{"type":"feat"}`,
		StructuredData: sampleOut{Type: "feat"},
		Model:          "test-model",
	}})
	if err != nil {
		t.Fatalf("WithLogging: %v", err)
	}

	if _, err := p.Execute(context.Background(), ai.Request{
		Prompt: api.Prompt{
			User:   "rendered input prompt",
			Source: "ai-commit-message.prompt",
			Schema: &sampleOut{},
		},
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := ansi.ReplaceAllString(buf.String(), "")
	for _, want := range []string{
		"ai-commit-message.prompt", // prompt source
		"rendered input prompt",    // rendered input
		"schema-in",                // schema-in label
		`"type":"object"`,          // schema-in body
		"schema-out",               // schema-out label
		`"type": "feat"`,           // structured schema-out body
	} {
		if !strings.Contains(got, want) {
			t.Errorf("logging middleware output missing %q\n--- output ---\n%s", want, got)
		}
	}
}

// TestLoggingProvider_EmitsSchemaJSON drives a request whose schema is a pre-built
// SchemaJSON (no Go target) — the commit-grouping path — through the logging
// middleware at debug level and asserts the schema-in with its maxItems cap appears.
// Before this, schema-in only rendered the reflected Go-target schema, so
// `gavel commit -G -v` could not show the grouping cap.
func TestLoggingProvider_EmitsSchemaJSON(t *testing.T) {
	prev := logger.GetOutput()
	t.Cleanup(func() { logger.SetOutput(prev) })
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.GetLogger("ai").SetLogLevel("debug")

	p, err := WithLogging()(stubProvider{resp: &ai.Response{Text: `{"groups":[]}`, Model: "test-model"}})
	if err != nil {
		t.Fatalf("WithLogging: %v", err)
	}

	if _, err := p.Execute(context.Background(), ai.Request{Prompt: api.Prompt{
		User:       "group the files",
		Source:     "commit-grouping.prompt",
		SchemaJSON: json.RawMessage(`{"type":"object","properties":{"groups":{"type":"array","maxItems":2}}}`),
	}}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := ansi.ReplaceAllString(buf.String(), "")
	for _, want := range []string{"schema-in", `"maxItems":2`} {
		if !strings.Contains(got, want) {
			t.Errorf("debug output missing %q\n--- output ---\n%s", want, got)
		}
	}
}
