package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
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

// isolateSavedAI redirects captainconfig.Path() to an empty file inside
// t.TempDir() so loadSavedAI() returns zero defaults rather than leaking
// the developer's real ~/.captain.yaml into table-test expectations.
func isolateSavedAI(t *testing.T) {
	t.Helper()
	seedSavedAI(t, "")
}

// seedSavedAI redirects captainconfig.Path() to a temp file containing yaml.
func seedSavedAI(t *testing.T, yaml string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".captain.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatalf("seed captain config: %v", err)
	}
	captainconfig.SetPathForTesting(p)
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
}

func TestAIPromptOptions_ToRequest_Defaults(t *testing.T) {
	isolateSavedAI(t)
	opts := defaultPromptOptions(t)
	req, err := opts.ToRequest()
	if err != nil {
		t.Fatalf("ToRequest: %v", err)
	}

	if req.Prompt.User != "hello" {
		t.Errorf("Prompt = %q, want hello", req.Prompt.User)
	}
	if req.Budget.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096 built-in default", req.Budget.MaxTokens)
	}
	for name, got := range map[string]bool{
		"NoMCP": req.Permissions.MCP.Disabled, "NoHooks": req.Memory.SkipHooks, "NoSkills": req.Memory.SkipSkills,
		"NoUser": req.Memory.SkipUser, "NoProject": req.Memory.SkipProject, "NoMemory": req.Memory.SkipMemory,
		"Bare": req.Memory.Bare, "Edit": req.Permissions.HasPreset(api.PresetEdit),
	} {
		if got {
			t.Errorf("%s = true; default-shape options must not flip any No*/Bare/Edit", name)
		}
	}
}

// TestAIPromptOptions_ToRequest_NegativeFlags verifies each --no-* flag sets the
// matching No* field on the request.
func TestAIPromptOptions_ToRequest_NegativeFlags(t *testing.T) {
	isolateSavedAI(t)
	cases := []struct {
		name   string
		mutate func(*AIPromptOptions)
		get    func(ai.Request) bool
	}{
		{"no-mcp", func(o *AIPromptOptions) { o.NoMCP = true }, func(r ai.Request) bool { return r.Permissions.MCP.Disabled }},
		{"no-hooks", func(o *AIPromptOptions) { o.NoHooks = true }, func(r ai.Request) bool { return r.Memory.SkipHooks }},
		{"no-skills", func(o *AIPromptOptions) { o.NoSkills = true }, func(r ai.Request) bool { return r.Memory.SkipSkills }},
		{"no-user", func(o *AIPromptOptions) { o.NoUser = true }, func(r ai.Request) bool { return r.Memory.SkipUser }},
		{"no-project", func(o *AIPromptOptions) { o.NoProject = true }, func(r ai.Request) bool { return r.Memory.SkipProject }},
		{"no-memory", func(o *AIPromptOptions) { o.NoMemory = true }, func(r ai.Request) bool { return r.Memory.SkipMemory }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := defaultPromptOptions(t)
			tc.mutate(&opts)
			req, err := opts.ToRequest()
			if err != nil {
				t.Fatalf("ToRequest: %v", err)
			}
			if !tc.get(req) {
				t.Errorf("flag --%s did not set the mapped request field", tc.name)
			}
		})
	}
}

func TestAIPromptOptions_ToRequest_PassesScalars(t *testing.T) {
	isolateSavedAI(t)
	opts := defaultPromptOptions(t)
	opts.System = "be careful"
	opts.AppendSystem = "also be brief"
	opts.PermissionMode = "acceptEdits"
	opts.Edit = true
	opts.AllowedTools = []string{"Read", "Bash"}
	opts.DisallowedTools = []string{"Write"}
	opts.SkillDirs = []string{"/skills/a", "/skills/b"}
	opts.Bare = true
	opts.MaxTokens = 1024
	opts.Temperature = "0.5"
	opts.Effort = "high"
	opts.MaxTurns = 7
	opts.Resume = "sess-123"

	req, err := opts.ToRequest()
	if err != nil {
		t.Fatalf("ToRequest: %v", err)
	}
	if req.Prompt.System != "be careful" {
		t.Errorf("SystemPrompt = %q", req.Prompt.System)
	}
	if req.Prompt.AppendSystem != "also be brief" {
		t.Errorf("AppendSystemPrompt = %q", req.Prompt.AppendSystem)
	}
	if req.Permissions.Mode != api.PermissionAcceptEdits {
		t.Errorf("Permissions.Mode = %q, want acceptEdits", req.Permissions.Mode)
	}
	if !req.Permissions.HasPreset(api.PresetEdit) {
		t.Error("Edit preset not propagated")
	}
	if !reflect.DeepEqual(req.Permissions.Tools.AllowList(), []string{"Bash", "Read"}) {
		t.Errorf("AllowedTools = %v", req.Permissions.Tools.AllowList())
	}
	if !reflect.DeepEqual(req.Permissions.Tools.DenyList(), []string{"Write"}) {
		t.Errorf("DisallowedTools = %v", req.Permissions.Tools.DenyList())
	}
	if !reflect.DeepEqual(req.Memory.Skills, []string{"/skills/a", "/skills/b"}) {
		t.Errorf("SkillDirs = %v", req.Memory.Skills)
	}
	if !req.Memory.Bare {
		t.Error("Bare not propagated")
	}
	if req.Budget.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d", req.Budget.MaxTokens)
	}
	if temp, ok := req.Temp(); !ok || temp != 0.5 {
		t.Errorf("Temperature = %v (set=%v)", temp, ok)
	}
	if req.Effort != api.EffortHigh {
		t.Errorf("ReasoningEffort = %q", req.Effort)
	}
	if req.Budget.MaxTurns != 7 {
		t.Errorf("MaxTurns = %d", req.Budget.MaxTurns)
	}
	if req.SessionID != "sess-123" {
		t.Errorf("SessionID = %q (from --resume)", req.SessionID)
	}
}

// TestAIRuntimeOptions_ToRequest_ValidationErrors verifies malformed input fails
// loudly instead of being silently coerced to a zero value.
func TestAIRuntimeOptions_ToRequest_ValidationErrors(t *testing.T) {
	isolateSavedAI(t)
	cases := []struct {
		name   string
		mutate func(*AIPromptOptions)
		want   string
	}{
		{"bad temperature", func(o *AIPromptOptions) { o.Temperature = "hot" }, "temperature"},
		{"temperature above max", func(o *AIPromptOptions) { o.Temperature = "3" }, "0.0-2.0"},
		{"temperature below min", func(o *AIPromptOptions) { o.Temperature = "-1" }, "0.0-2.0"},
		{"bad permission-mode", func(o *AIPromptOptions) { o.PermissionMode = "yolo" }, "permission-mode"},
		{"bad effort", func(o *AIPromptOptions) { o.Effort = "extreme" }, "effort"},
		{"max-turns above max", func(o *AIPromptOptions) { o.MaxTurns = 200 }, "max-turns"},
		{"max-turns below min", func(o *AIPromptOptions) { o.MaxTurns = -1 }, "max-turns"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := defaultPromptOptions(t)
			tc.mutate(&opts)
			if _, err := opts.ToRequest(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestAIProviderOptions_ToConfig_ValidationErrors(t *testing.T) {
	isolateSavedAI(t)
	if _, err := (AIProviderOptions{ModelFlags: aiflags.ModelFlags{Model: "claude-x", Mode: "nope"}}).ToConfig(); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("expected invalid mode error, got %v", err)
	}
	if _, err := (AIProviderOptions{ModelFlags: aiflags.ModelFlags{Model: "claude-x"}, Budget: "free"}).ToConfig(); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected invalid budget error, got %v", err)
	}
}

func TestAIProviderOptions_ToConfig_Sandbox(t *testing.T) {
	tests := []struct {
		name, saved, wantName, wantErr string
		flags                          aiflags.ModelFlags
		// A sandbox forces cli mode, so only the provider varies per candidate.
		providers []*api.ModelProvider
	}{
		{name: "selects CLI without changing model", flags: aiflags.ModelFlags{Model: "claude-sonnet-5"}, wantName: "claude-sonnet-5", providers: []*api.ModelProvider{api.Anthropic}},
		{name: "rejects explicit API mode", flags: aiflags.ModelFlags{Model: "claude-sonnet-5", Mode: "api"}, wantErr: "requires cli mode"},
		{name: "overrides the saved agent mode", saved: "ai:\n  providers:\n    anthropic:\n      model: opus\n      mode: agent\n", providers: []*api.ModelProvider{api.Anthropic}},
		{name: "resolves fallbacks in CLI mode", flags: aiflags.ModelFlags{Model: "claude-sonnet-5,gpt-5.5", Fallback: []string{"gemini-3.5-flash"}}, providers: []*api.ModelProvider{api.Anthropic, api.OpenAI, api.Google}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.saved == "" {
				isolateSavedAI(t)
			} else {
				seedSavedAI(t, tt.saved)
			}
			cfg, err := (AIProviderOptions{ModelFlags: tt.flags, Sandbox: "docker"}).ToConfig()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			candidates := cfg.Model.Candidates()
			if cfg.SandboxSelection == nil || cfg.SandboxSelection.Kind != api.SandboxDocker || len(candidates) != len(tt.providers) {
				t.Fatalf("sandbox/candidates = %v/%v, want docker/%v", cfg.SandboxSelection, candidates, tt.providers)
			}
			for i, provider := range tt.providers {
				if candidates[i].Provider != provider || candidates[i].Mode != registry.ModeCLI {
					t.Fatalf("candidate[%d] runtime = %v %s, want %s cli", i, candidates[i].Provider, candidates[i].Mode, provider.Name)
				}
			}
			if tt.wantName != "" && cfg.Model.Name != tt.wantName {
				t.Fatalf("model name = %q, want %q", cfg.Model.Name, tt.wantName)
			}
		})
	}
}

// --api-url is the only way to point a captain run at a `captain ai mock`
// endpoint for the runtimes that read Config.APIURL — the genkit ones, and
// openai cli, which ignores OPENAI_BASE_URL when a ChatGPT credential is stored.
func TestAIProviderOptions_ToConfig_CarriesAPIURL(t *testing.T) {
	isolateSavedAI(t)
	const endpoint = "http://127.0.0.1:18095"
	cfg, err := (AIProviderOptions{
		ModelFlags: aiflags.ModelFlags{Model: "claude-sonnet-5"},
		APIURL:     "  " + endpoint + "  ",
	}).ToConfig()
	if err != nil {
		t.Fatalf("ToConfig: %v", err)
	}
	if cfg.APIURL != endpoint {
		t.Fatalf("APIURL = %q, want %q", cfg.APIURL, endpoint)
	}
}

func TestAIProviderOptions_ToConfig_LoadsSchemaRepairDefaults(t *testing.T) {
	seedSavedAI(t, `
ai:
  providers:
    anthropic:
      model: claude-sonnet-5
      mode: api
prompts:
  schemaRepair:
    model: gpt-5
    mode: api
    prompt: /repo/prompts/json-repair.prompt
`)
	cfg, err := (AIProviderOptions{}).ToConfig()
	if err != nil {
		t.Fatalf("ToConfig: %v", err)
	}
	// Saved `mode:` is the mechanism; the provider follows from the model name.
	if cfg.SchemaRepair.Model.Name != "gpt-5" || cfg.SchemaRepair.Model.Mode != api.ModeAPI {
		t.Fatalf("schema repair model = %#v", cfg.SchemaRepair.Model)
	}
	resolved, err := api.ResolveModel(cfg.SchemaRepair.Model)
	if err != nil || resolved.Provider != api.OpenAI || resolved.Mode != api.ModeAPI {
		t.Fatalf("schema repair resolves to %#v (err %v)", resolved, err)
	}
	if cfg.SchemaRepair.Prompt != "/repo/prompts/json-repair.prompt" {
		t.Fatalf("schema repair prompt = %q", cfg.SchemaRepair.Prompt)
	}
}

// TestAIRuntimeOptions_ToRequest_MaxTokensPrecedence pins the flag > saved >
// built-in order, replacing the old magic-4096 sentinel.
func TestAIRuntimeOptions_ToRequest_MaxTokensPrecedence(t *testing.T) {
	t.Run("explicit flag wins over saved", func(t *testing.T) {
		seedSavedAI(t, "ai:\n  maxTokens: 16000\n")
		req, err := AIRuntimeOptions{MaxTokens: 2048}.ToRequest("", "", "p")
		if err != nil {
			t.Fatal(err)
		}
		if req.Budget.MaxTokens != 2048 {
			t.Errorf("MaxTokens = %d, want 2048 (explicit flag)", req.Budget.MaxTokens)
		}
	})
	t.Run("unset falls back to saved", func(t *testing.T) {
		seedSavedAI(t, "ai:\n  maxTokens: 16000\n")
		req, err := AIRuntimeOptions{}.ToRequest("", "", "p")
		if err != nil {
			t.Fatal(err)
		}
		if req.Budget.MaxTokens != 16000 {
			t.Errorf("MaxTokens = %d, want 16000 (saved)", req.Budget.MaxTokens)
		}
	})
	t.Run("unset with no saved uses built-in 4096", func(t *testing.T) {
		isolateSavedAI(t)
		req, err := AIRuntimeOptions{}.ToRequest("", "", "p")
		if err != nil {
			t.Fatal(err)
		}
		if req.Budget.MaxTokens != 4096 {
			t.Errorf("MaxTokens = %d, want 4096 (built-in)", req.Budget.MaxTokens)
		}
	})
}

// TestAIRuntimeOptions_ToRequest_OverlaysSaved verifies the path gavel (and any
// other embedder) takes: AIRuntimeOptions with zero flag values should pick up
// NoMCP/.../MaxTokens/ReasoningEffort from ~/.captain.yaml, and an explicit
// --effort flag should override the saved value.
func TestAIRuntimeOptions_ToRequest_OverlaysSaved(t *testing.T) {
	seedSavedAI(t, "ai:\n  noMCP: true\n  noHooks: true\n  noSkills: true\n  noUser: true\n  noProject: true\n  noMemory: true\n  maxTokens: 16000\n  providers:\n    anthropic:\n      reasoningEffort: low\n")

	opts := AIRuntimeOptions{AIProviderOptions: AIProviderOptions{ModelFlags: aiflags.ModelFlags{Effort: "high"}}} // flag overrides saved low
	req, err := opts.ToRequest("sys", "", "user")
	if err != nil {
		t.Fatalf("ToRequest: %v", err)
	}

	if req.Prompt.System != "sys" || req.Prompt.User != "user" {
		t.Errorf("prompt fields = %q/%q, want sys/user", req.Prompt.System, req.Prompt.User)
	}
	if req.Budget.MaxTokens != 16000 {
		t.Errorf("MaxTokens = %d, want 16000 (saved overlay)", req.Budget.MaxTokens)
	}
	if req.Effort != api.EffortHigh {
		t.Errorf("ReasoningEffort = %q, want high (flag overrides saved)", req.Effort)
	}
	for name, got := range map[string]bool{
		"NoMCP": req.Permissions.MCP.Disabled, "NoHooks": req.Memory.SkipHooks, "NoSkills": req.Memory.SkipSkills,
		"NoUser": req.Memory.SkipUser, "NoProject": req.Memory.SkipProject, "NoMemory": req.Memory.SkipMemory,
	} {
		if !got {
			t.Errorf("%s = false, want true (from saved config)", name)
		}
	}
}

// TestRuntimeFlagHelpEnumeratesEveryAxis guards the static --mode and --provider
// help strings against drift from the registry, the single source of truth.
func TestRuntimeFlagHelpEnumeratesEveryAxis(t *testing.T) {
	modes := make([]string, 0, len(api.AllRuntimeModes()))
	for _, mode := range api.AllRuntimeModes() {
		modes = append(modes, string(mode))
	}
	providers := make([]string, 0, len(api.Providers()))
	for _, p := range api.Providers() {
		providers = append(providers, p.Name)
	}
	for _, c := range []struct {
		typ   reflect.Type
		name  string
		field string
		want  []string
	}{
		{reflect.TypeOf(aiflags.ModelFlags{}), "ModelFlags", "Mode", modes},
		{reflect.TypeOf(AIModelsOptions{}), "AIModelsOptions", "Mode", modes},
		{reflect.TypeOf(AIModelsOptions{}), "AIModelsOptions", "Provider", providers},
	} {
		f, ok := c.typ.FieldByName(c.field)
		if !ok {
			t.Fatalf("%s has no %s field", c.name, c.field)
		}
		help := f.Tag.Get("help")
		for _, want := range c.want {
			if !strings.Contains(help, want) {
				t.Errorf("%s %s help %q is missing %q", c.name, c.field, help, want)
			}
		}
	}
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

func defaultPromptOptions(t *testing.T) AIPromptOptions {
	t.Helper()
	return AIPromptOptions{
		AIRuntimeOptions: AIRuntimeOptions{AIProviderOptions: AIProviderOptions{ModelFlags: aiflags.ModelFlags{Temperature: "0"}}},
		Timeout:          "120s",
		Prompt:           "hello",
	}
}
