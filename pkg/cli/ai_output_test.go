package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
)

type promptResultProvider struct{}

func (promptResultProvider) GetModel() string { return "claude-sonnet-4-6" }

func (promptResultProvider) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Anthropic, ai.ModeAPI) }

func (promptResultProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{
		Text:    "done",
		Model:   "claude-sonnet-4-6",
		Runtime: ai.RuntimeOf(ai.Anthropic, ai.ModeAPI),
		Usage:   ai.Usage{InputTokens: 12, OutputTokens: 7},
	}, nil
}

type structuredPromptResultProvider struct{}

func (structuredPromptResultProvider) GetModel() string { return "claude-sonnet-4-6" }

func (structuredPromptResultProvider) GetRuntime() ai.Runtime {
	return ai.RuntimeOf(ai.Anthropic, ai.ModeAPI)
}

func (structuredPromptResultProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{
		Text:           `{"answer":"42"}`,
		StructuredData: json.RawMessage(`{"answer":"42"}`),
		Model:          "claude-sonnet-4-6",
		Runtime:        ai.RuntimeOf(ai.Anthropic, ai.ModeAPI),
	}, nil
}

type promptResultStreamingProvider struct{}

func (promptResultStreamingProvider) GetModel() string { return "gpt-5-codex" }

func (promptResultStreamingProvider) GetRuntime() ai.Runtime {
	return ai.RuntimeOf(ai.OpenAI, ai.ModeCLI)
}

func (promptResultStreamingProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return nil, nil
}

func (promptResultStreamingProvider) ExecuteStream(_ context.Context, _ ai.Request) (<-chan ai.Event, error) {
	events := make(chan ai.Event, 3)
	events <- ai.Event{Kind: ai.EventSystem, SessionID: "stream-session-1", Model: "gpt-5-codex"}
	events <- ai.Event{Kind: ai.EventText, Text: "streamed", Model: "gpt-5-codex"}
	events <- ai.Event{Kind: ai.EventResult, Model: "gpt-5-codex", Usage: &ai.Usage{InputTokens: 21, OutputTokens: 9}, CostUSD: 0.02}
	close(events)
	return events, nil
}

type structuredResultStreamingProvider struct {
	text string
}

func (structuredResultStreamingProvider) GetModel() string { return "gpt-5-codex" }

func (structuredResultStreamingProvider) GetRuntime() ai.Runtime {
	return ai.RuntimeOf(ai.OpenAI, ai.ModeCLI)
}

func (structuredResultStreamingProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return nil, nil
}

func (p structuredResultStreamingProvider) ExecuteStream(_ context.Context, _ ai.Request) (<-chan ai.Event, error) {
	events := make(chan ai.Event, 2)
	if p.text != "" {
		events <- ai.Event{Kind: ai.EventText, Text: p.text}
	}
	events <- ai.Event{Kind: ai.EventResult, StructuredData: json.RawMessage(`{"answer":"42"}`)}
	close(events)
	return events, nil
}

func TestRunBuffered_JSONIncludesFullInputSpec(t *testing.T) {
	req := ai.Request{
		Model:  api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAPI, Effort: api.EffortMedium},
		Prompt: api.Prompt{System: "be precise", User: "summarize"},
		Budget: api.Budget{MaxTokens: 2048},
		Setup:  &shell.Setup{Cwd: "/repo"},
		Permissions: api.Permissions{
			Presets: []api.Preset{api.PresetEdit},
			Tools:   api.Tools{"Read": api.ToolPolicyAllow},
		},
		SessionID: "resume-1",
	}

	got, err := runBuffered(context.Background(), promptResultProvider{}, req)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := got.(AIPromptResult)
	if !ok {
		t.Fatalf("runBuffered returned %T, want AIPromptResult", got)
	}
	if result.Input.Prompt.User != "summarize" || result.Input.Model.Name != "claude-sonnet-4-6" {
		t.Fatalf("result input = %+v, want original request", result.Input)
	}
	if result.InputTokens != 12 {
		t.Fatalf("InputTokens = %d, want 12", result.InputTokens)
	}
	if result.Model != "claude-sonnet-4-6" || result.Provider != "anthropic" || result.Mode != "api" || result.Dir != "/repo" || result.SessionID != "resume-1" {
		t.Fatalf("resolved fields = model %q provider %q mode %q dir %q session %q", result.Model, result.Provider, result.Mode, result.Dir, result.SessionID)
	}

	var encoded map[string]any
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatal(err)
	}
	input, ok := encoded["input"].(map[string]any)
	if !ok {
		t.Fatalf("json input = %T, want object: %s", encoded["input"], data)
	}
	prompt, ok := input["prompt"].(map[string]any)
	if !ok || prompt["user"] != "summarize" || prompt["system"] != "be precise" {
		t.Fatalf("json input.prompt = %#v, want rendered prompt", input["prompt"])
	}
	// input is the authored spec, so it carries the model and effort; the resolved
	// runtime is run history and is published on the result itself.
	if input["model"] != "claude-sonnet-4-6" || input["effort"] != "medium" {
		t.Fatalf("json input model fields = %#v", input)
	}
	if encoded["provider"] != "anthropic" || encoded["mode"] != "api" {
		t.Fatalf("json runtime = %#v/%#v, want the resolved pair", encoded["provider"], encoded["mode"])
	}
	setup, ok := input["setup"].(map[string]any)
	if !ok || setup["cwd"] != "/repo" {
		t.Fatalf("json input.setup = %#v, want cwd /repo", input["setup"])
	}
	if input["sessionId"] != "resume-1" || encoded["sessionId"] != "resume-1" || encoded["dir"] != "/repo" {
		t.Fatalf("json session/dir fields = top %#v input %#v", encoded, input)
	}
	if got := encoded["inputTokens"]; got != float64(12) {
		t.Fatalf("json inputTokens = %#v, want 12", got)
	}
}

func TestRunBuffered_PreservesStructuredOutput(t *testing.T) {
	got, err := runBuffered(context.Background(), structuredPromptResultProvider{}, ai.Request{})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := got.(AIPromptResult)
	if !ok {
		t.Fatalf("runBuffered returned %T, want AIPromptResult", got)
	}
	if result.Text != `{"answer":"42"}` {
		t.Fatalf("Text = %q, want JSON transcript text", result.Text)
	}
	if result.StructuredOutput["answer"] != "42" {
		t.Fatalf("StructuredOutput = %#v, want decoded answer", result.StructuredOutput)
	}
}

func TestRunStreaming_JSONIncludesFullInputSpec(t *testing.T) {
	req := ai.Request{
		Model:       api.Model{Name: "gpt-5-codex", Mode: api.ModeCLI, Effort: api.EffortHigh},
		Prompt:      api.Prompt{User: "fix tests"},
		Setup:       &shell.Setup{Cwd: "/repo"},
		Permissions: api.Permissions{MCP: api.MCP{Disabled: true}},
	}

	got, err := runStreaming(context.Background(), promptResultStreamingProvider{}, req)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := got.(AIPromptResult)
	if !ok {
		t.Fatalf("runStreaming returned %T, want AIPromptResult", got)
	}
	if result.Input.Prompt.User != "fix tests" || result.Input.Model.Mode != api.ModeCLI {
		t.Fatalf("result input = %+v, want original request", result.Input)
	}
	if result.Dir != "/repo" || result.SessionID != "stream-session-1" || result.Input.SessionID != "stream-session-1" {
		t.Fatalf("dir/session = dir %q session %q input session %q", result.Dir, result.SessionID, result.Input.SessionID)
	}
	if result.InputTokens != 21 || result.Output != 9 || result.CostUSD != 0.02 {
		t.Fatalf("usage/cost = input %d output %d cost %f", result.InputTokens, result.Output, result.CostUSD)
	}
}

func TestRunStreaming_StructuredResultIsReturnedOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{name: "result only"},
		{name: "replaces prior text", text: "discarded narrative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runStreaming(context.Background(), structuredResultStreamingProvider{text: tc.text}, ai.Request{})
			if err != nil {
				t.Fatal(err)
			}
			result, ok := got.(AIPromptResult)
			if !ok {
				t.Fatalf("runStreaming returned %T, want AIPromptResult", got)
			}
			if result.Text != `{"answer":"42"}` {
				t.Fatalf("Text = %q, want authoritative structured JSON once", result.Text)
			}
			if result.StructuredOutput["answer"] != "42" {
				t.Fatalf("StructuredOutput = %#v, want decoded answer", result.StructuredOutput)
			}
		})
	}
}
