package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/spf13/cobra"
)

// fakeSchemaProbe reports no API keys and no CLI login files but all CLI
// binaries present. This keeps the probe hermetic: fetchAPIModels makes no
// network call without an API key, API backends stay key-gated, and CLI-style
// backends project exact IDs from Captain's internal registry.
func fakeSchemaProbe() ai.AuthProbe {
	return ai.AuthProbe{
		Getenv:     func(string) string { return "" },
		LookPath:   func(bin string) (string, error) { return "/usr/local/bin/" + bin, nil },
		FileExists: func(string) bool { return false },
		Home:       "/home/test",
	}
}

func stubbedSchemaAdapters(t *testing.T) []AdapterStatus {
	t.Helper()
	adapters, err := ai.ProbeAdapters(ai.WhoamiOptions{Models: true}, fakeSchemaProbe())
	if err != nil {
		t.Fatalf("ProbeAdapters: %v", err)
	}
	return adapters
}

func TestPromptSchemaDocumentBackendsAndConditionals(t *testing.T) {
	doc, err := buildPromptSchemaDocument(stubbedSchemaAdapters(t), captainconfig.SandboxDefaults{})
	if err != nil {
		t.Fatalf("buildPromptSchemaDocument: %v", err)
	}
	if doc["schemaVersion"].(int) != 2 {
		t.Fatalf("schemaVersion = %v, want 2", doc["schemaVersion"])
	}

	backends := doc["backends"].([]map[string]any)
	if len(backends) != len(api.AllBackends()) {
		t.Fatalf("backends length = %d, want %d", len(backends), len(api.AllBackends()))
	}
	byName := map[string]map[string]any{}
	for _, e := range backends {
		byName[e["backend"].(string)] = e
	}
	for _, b := range api.AllBackends() {
		e, ok := byName[string(b)]
		if !ok {
			t.Fatalf("backends[] missing %q", b)
		}
		_, hasArgs := e["args"]
		wantArgs := b == api.BackendClaudeCmux || b == api.BackendCodexCmux
		if hasArgs != wantArgs {
			t.Errorf("backend %s args present = %v, want %v", b, hasArgs, wantArgs)
		}
	}

	codexCLIModels, hasModels := byName[string(api.BackendCodexCLI)]["models"].([]string)
	if !hasModels || len(codexCLIModels) == 0 {
		t.Fatalf("codex-cli should expose exact registry models without API provider data: %+v", byName[string(api.BackendCodexCLI)])
	}
	if got := codexCLIModels[0]; got != "gpt-5.6-sol" {
		t.Errorf("codex-cli first model = %q, want gpt-5.6-sol", got)
	}
	flat := doc["models"].([]PromptModelCatalogEntry)
	codexModel := schemaModelForBackend(t, flat, "gpt-5.6-sol", "cli")
	// The provider is the catalog namespace, not the backend: every Codex mode
	// buckets under "openai" so one family filter reaches all of them.
	if got := codexModel.Provider; got != "openai" {
		t.Errorf("flat model provider = %v, want openai", got)
	}
	if codexModel.Label == "" {
		t.Error("flat model label is empty")
	}
	if !codexModel.Reasoning {
		t.Error("flat model reasoning = false, want true")
	}
	if !containsString(codexModel.SupportedEfforts, "max") || !containsString(codexModel.SupportedEfforts, "ultra") {
		t.Errorf("flat model supportedEfforts = %#v, want patched Codex ultra", codexModel.SupportedEfforts)
	}
	if codexModel.DefaultEffort != "" {
		t.Errorf("flat model should not have a locally patched default effort: %#v", codexModel.DefaultEffort)
	}
	if codexModel.Configured {
		t.Error("flat model configured = true, want false for fake unauthenticated CLI")
	}
	if got := codexModel.Runtime; !reflect.DeepEqual(got, api.Model{Name: "gpt-5.6-sol"}) {
		t.Errorf("flat model runtime = %#v, want provider-independent model", got)
	}

	anthropic := byName[string(api.BackendAnthropic)]
	if _, hasModels := anthropic["models"]; hasModels {
		t.Errorf("anthropic should not synthesize models without live provider data: %+v", anthropic)
	}
	if errText, _ := anthropic["modelError"].(string); !strings.Contains(errText, "ANTHROPIC_API_KEY") {
		t.Errorf("anthropic modelError = %q, want missing-key hint", errText)
	}

	spec := doc["spec"].(map[string]any)
	for _, raw := range spec["allOf"].([]any) {
		condition := raw.(map[string]any)["if"].(map[string]any)
		if props, ok := condition["properties"].(map[string]any); ok {
			if _, leaked := props["backend"]; leaked {
				t.Fatal("spec schema must not key runtime rules on exact Captain adapters")
			}
		}
	}
}

func TestPromptSchemaDocumentSandboxEnumIncludesConfiguredBackends(t *testing.T) {
	defaults := captainconfig.SandboxDefaults{Backends: map[string]captainconfig.SandboxBackend{
		"prod-pool":    {Kind: "git-agent"},
		"local-docker": {Kind: "docker"},
		"native":       {Kind: "native"}, // a configured name must not duplicate a bare kind
	}}
	doc, err := buildPromptSchemaDocument(stubbedSchemaAdapters(t), defaults)
	if err != nil {
		t.Fatalf("buildPromptSchemaDocument: %v", err)
	}

	spec := doc["spec"].(map[string]any)
	ref := spec["$defs"].(map[string]any)["SandboxRef"].(map[string]any)
	forms := ref["oneOf"].([]any)
	scalar := forms[0].(map[string]any)
	object := forms[1].(map[string]any)
	backend := object["properties"].(map[string]any)["backend"].(map[string]any)
	want := []any{"off", "native", "docker", "git-agent", "local-docker", "prod-pool"}
	if !reflect.DeepEqual(scalar["enum"], want) {
		t.Errorf("scalar sandbox enum = %v, want %v", scalar["enum"], want)
	}
	if !reflect.DeepEqual(backend["enum"], want) {
		t.Errorf("object backend enum = %v, want %v", backend["enum"], want)
	}
}

// TestPromptSchemaDocumentDropsDisabledEntries covers the opposite policy to
// whoami: a schema that still offered a disabled backend, model or tier would
// let a run pick something the user opted out of.
func TestPromptSchemaDocumentDropsDisabledEntries(t *testing.T) {
	api.SetDisabled(api.NewDisabledSet(
		[]string{"cmux"}, nil, nil, []string{"codex-cli/gpt-5.6-sol"}, []string{"ultra"}))
	t.Cleanup(func() { api.SetDisabled(api.DisabledSet{}) })

	doc, err := buildPromptSchemaDocument(stubbedSchemaAdapters(t), captainconfig.SandboxDefaults{})
	if err != nil {
		t.Fatalf("buildPromptSchemaDocument: %v", err)
	}

	backends := doc["backends"].([]map[string]any)
	for _, entry := range backends {
		if backend := api.Backend(entry["backend"].(string)); backend.Mode() == api.ModeCmux {
			t.Errorf("backends[] still offers disabled %s", backend)
		}
		if backend := entry["backend"].(string); backend == string(api.BackendCodexCLI) {
			if models, _ := entry["models"].([]string); containsString(models, "gpt-5.6-sol") {
				t.Errorf("codex-cli still offers the disabled model: %v", models)
			}
		}
	}
	want := 0
	for _, backend := range api.AllBackends() {
		if backend.Mode() != api.ModeCmux {
			want++
		}
	}
	if len(backends) != want {
		t.Errorf("backends length = %d, want %d with every cmux backend dropped", len(backends), want)
	}

	for _, family := range doc["runtimes"].([]api.RuntimeFamily) {
		for _, mode := range family.Modes {
			if mode.Backend == string(api.ModeCmux) {
				t.Errorf("runtimes[] still offers the disabled %s mode", mode.Backend)
			}
			if mode.Disabled {
				t.Errorf("runtimes[] served a disabled entry instead of dropping it: %+v", mode)
			}
		}
	}

	for _, model := range doc["models"].([]PromptModelCatalogEntry) {
		if containsString(model.SupportedEfforts, "ultra") {
			t.Errorf("model %v still offers the disabled ultra tier: %v", model.ID, model.SupportedEfforts)
		}
	}
	if efforts := doc["efforts"].([]string); containsString(efforts, "ultra") || len(efforts) != len(api.AllEfforts())-1 {
		t.Errorf("doc.efforts = %v, want every tier but ultra", efforts)
	}
}

// TestPromptSchemaDocumentServesTheRuntimeCatalog covers what lets the workbench
// runtime picker stop shipping its own family list: the document already carries
// every provider×mode pair and the model each one seeds with.
func TestPromptSchemaDocumentServesTheRuntimeCatalog(t *testing.T) {
	doc, err := buildPromptSchemaDocument(stubbedSchemaAdapters(t), captainconfig.SandboxDefaults{})
	if err != nil {
		t.Fatalf("buildPromptSchemaDocument: %v", err)
	}

	seen := map[string]bool{}
	for _, family := range doc["runtimes"].([]api.RuntimeFamily) {
		if family.Family == "" || family.Provider == "" || family.CatalogPrefix == "" {
			t.Errorf("runtime family is missing an identity: %+v", family)
		}
		for _, mode := range family.Modes {
			seen[family.Provider+":"+mode.Backend] = true
			if mode.DefaultModel == "" {
				t.Errorf("%s serves no default model, so a picker would have to hardcode one", mode.Backend)
			}
			if _, ok := api.ParseRuntimeMode(mode.Backend); !ok {
				t.Errorf("runtime backend %q is outside the canonical grammar", mode.Backend)
			}
		}
	}
	if len(seen) != len(api.AllBackends()) {
		t.Errorf("runtimes cover %d backends, want all %d", len(seen), len(api.AllBackends()))
	}
}

func TestPromptSchemaDocumentServesTheEffortUniverse(t *testing.T) {
	doc, err := buildPromptSchemaDocument(stubbedSchemaAdapters(t), captainconfig.SandboxDefaults{})
	if err != nil {
		t.Fatalf("buildPromptSchemaDocument: %v", err)
	}
	efforts := doc["efforts"].([]string)
	want := make([]string, 0, len(api.AllEfforts()))
	for _, effort := range api.AllEfforts() {
		want = append(want, string(effort))
	}
	if !reflect.DeepEqual(efforts, want) {
		t.Errorf("doc.efforts = %v, want %v", efforts, want)
	}
}

func schemaModelForBackend(t *testing.T, models []PromptModelCatalogEntry, id, backend string) PromptModelCatalogEntry {
	t.Helper()
	for _, model := range models {
		if model.ID != id {
			continue
		}
		if containsString(model.Backends, backend) {
			return model
		}
	}
	t.Fatalf("models[] missing id %q with backend %q: %+v", id, backend, models)
	return PromptModelCatalogEntry{}
}

func TestPromptSchemaExampleIsPortable(t *testing.T) {
	ex := promptSchemaExampleSpec()

	raw, err := json.Marshal(ex)
	if err != nil {
		t.Fatalf("marshal example: %v", err)
	}
	if bytes.Contains(raw, []byte("/Users/")) {
		t.Errorf("example leaks a machine home path: %s", raw)
	}

	model := ex["model"].(string)
	if strings.Contains(model, "/") {
		t.Errorf("example model %q should be a bare slug (no provider prefix)", model)
	}
	if _, err := api.InferBackend(model); err != nil {
		t.Errorf("example model %q does not resolve to a backend: %v", model, err)
	}
	prompt := ex["prompt"].(map[string]any)
	for _, excluded := range []string{"source", "metadata"} {
		if _, ok := prompt[excluded]; ok {
			t.Errorf("example prompt includes editor-excluded %q", excluded)
		}
	}
	setup := ex["setup"].(map[string]any)
	if _, ok := setup["env"]; ok {
		t.Errorf("example setup uses stale env key: %+v", setup)
	}
	if _, ok := setup["envVars"]; !ok {
		t.Errorf("example setup missing envVars: %+v", setup)
	}

	// The example selects claude-cmux, so its cliArgs must validate as
	// ClaudeCmuxOptions — matching the schema's per-backend conditional.
	argsRaw, err := json.Marshal(ex["cliArgs"])
	if err != nil {
		t.Fatalf("marshal example cliArgs: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(argsRaw))
	dec.DisallowUnknownFields()
	var opts api.ClaudeCmuxOptions
	if err := dec.Decode(&opts); err != nil {
		t.Errorf("example cliArgs are not valid ClaudeCmuxOptions: %v", err)
	}
}

func TestWritePromptSchemaEmitsValidJSON(t *testing.T) {
	isolateCaptainConfig(t)
	prev := schemaAdapters
	t.Cleanup(func() { schemaAdapters = prev })
	stub := stubbedSchemaAdapters(t)
	schemaAdapters = func() ([]AdapterStatus, error) { return stub, nil }

	var buf bytes.Buffer
	if err := WritePromptSchema(context.Background(), &buf); err != nil {
		t.Fatalf("WritePromptSchema: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("schema output is not valid JSON: %v\n%s", err, buf.String())
	}
	if decoded["source"] != "captain prompt --schema" {
		t.Errorf("source = %v", decoded["source"])
	}
	if decoded["schemaVersion"].(float64) != 2 {
		t.Errorf("schemaVersion = %v, want 2", decoded["schemaVersion"])
	}
}

func TestPromptSchemaFlagRejectsPositionalArgs(t *testing.T) {
	root := &cobra.Command{Use: "captain"}
	root.AddCommand(&cobra.Command{Use: "prompt", RunE: func(*cobra.Command, []string) error { return nil }})
	if err := AttachPromptSchemaFlag(root); err != nil {
		t.Fatalf("AttachPromptSchemaFlag: %v", err)
	}

	root.SetArgs([]string{"prompt", "--schema", "unexpected"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error when --schema is given positional arguments")
	}
	if !strings.Contains(err.Error(), "positional") {
		t.Errorf("error = %q, want it to mention positional arguments", err)
	}
}
