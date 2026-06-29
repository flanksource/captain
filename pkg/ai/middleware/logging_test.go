package middleware

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
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
func (s stubProvider) GetBackend() ai.Backend { return ai.BackendAnthropic }

func TestSchemaInJSON(t *testing.T) {
	if got := schemaInJSON(nil); got != "" {
		t.Fatalf("text-mode (nil target): want empty, got %q", got)
	}

	got := schemaInJSON(&sampleOut{})
	for _, want := range []string{`"type":"object"`, `"description":"conventional type"`, `"required":["type"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("schema-in %q missing %q", got, want)
		}
	}

	if got := schemaInJSON("not-a-struct"); !strings.Contains(got, "schema-in error") {
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
		Prompt:           "rendered input prompt",
		Source:           "ai-commit-message.prompt",
		StructuredOutput: &sampleOut{},
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
