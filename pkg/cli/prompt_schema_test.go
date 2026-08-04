package cli

import (
	"bytes"
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
	flat := doc["models"].([]map[string]any)
	codexModel := schemaModelForBackend(t, flat, "gpt-5.6-sol", string(api.BackendCodexCLI))
	// The provider is the catalog namespace, not the backend: every Codex mode
	// buckets under "openai" so one family filter reaches all of them.
	if got := codexModel["provider"]; got != "openai" {
		t.Errorf("flat model provider = %v, want openai", got)
	}
	if got, ok := codexModel["label"].(string); !ok || got == "" {
		t.Errorf("flat model label = %#v, want non-empty string", codexModel["label"])
	}
	if _, ok := codexModel["reasoning"].(bool); !ok {
		t.Errorf("flat model reasoning = %#v, want bool", codexModel["reasoning"])
	}
	if efforts, ok := codexModel["supportedEfforts"].([]string); !ok || !containsString(efforts, "max") || !containsString(efforts, "ultra") {
		t.Errorf("flat model supportedEfforts = %#v, want patched Codex ultra", codexModel["supportedEfforts"])
	}
	if _, ok := codexModel["defaultEffort"]; ok {
		t.Errorf("flat model should not have a locally patched default effort: %#v", codexModel["defaultEffort"])
	}
	if got, ok := codexModel["configured"].(bool); !ok || got {
		t.Errorf("flat model configured = %#v, want false for fake unauthenticated CLI", codexModel["configured"])
	}
	assertSchemaModelBackends(t, codexModel, string(api.BackendCodexCLI), string(api.BackendCodexAgent), string(api.BackendCodexCmux))

	anthropic := byName[string(api.BackendAnthropic)]
	if _, hasModels := anthropic["models"]; hasModels {
		t.Errorf("anthropic should not synthesize models without live provider data: %+v", anthropic)
	}
	if errText, _ := anthropic["modelError"].(string); !strings.Contains(errText, "ANTHROPIC_API_KEY") {
		t.Errorf("anthropic modelError = %q, want missing-key hint", errText)
	}

	spec := doc["spec"].(map[string]any)
	allOf := spec["allOf"].([]any)
	if len(allOf) != len(api.AllBackends()) {
		t.Fatalf("spec.allOf length = %d, want %d", len(allOf), len(api.AllBackends()))
	}
	thenByBackend := map[string]map[string]any{}
	for _, c := range allOf {
		cm := c.(map[string]any)
		backend := cm["if"].(map[string]any)["properties"].(map[string]any)["backend"].(map[string]any)["const"].(string)
		thenByBackend[backend] = cm["then"].(map[string]any)["properties"].(map[string]any)
	}

	// cmux backends: cliArgs constrained by a $ref into $defs.
	defs := spec["$defs"].(map[string]any)
	for _, b := range []api.Backend{api.BackendClaudeCmux, api.BackendCodexCmux} {
		name := argDefName(b)
		if _, ok := defs[name]; !ok {
			t.Errorf("spec.$defs missing %q", name)
		}
		ref := thenByBackend[string(b)]["cliArgs"].(map[string]any)["$ref"].(string)
		if ref != "#/$defs/"+name {
			t.Errorf("backend %s cliArgs $ref = %q, want #/$defs/%s", b, ref, name)
		}
	}
	// non-cmux backend: cliArgs forbidden.
	if forbidden := thenByBackend[string(api.BackendAnthropic)]["cliArgs"]; forbidden != false {
		t.Errorf("anthropic then.cliArgs = %v, want false", forbidden)
	}

	// Every backend's model enum matches that backend's live model list; backends
	// with no models carry no model constraint.
	for _, e := range backends {
		backend := e["backend"].(string)
		models, _ := e["models"].([]string)
		enumWrap, hasEnum := thenByBackend[backend]["model"]
		if len(models) == 0 {
			if hasEnum {
				t.Errorf("backend %s has a model enum but no models", backend)
			}
			continue
		}
		enum := enumWrap.(map[string]any)["enum"].([]any)
		if len(enum) != len(models) {
			t.Fatalf("backend %s enum length %d != models length %d", backend, len(enum), len(models))
		}
		for i := range models {
			if enum[i].(string) != models[i] {
				t.Errorf("backend %s enum[%d] = %q, want %q", backend, i, enum[i], models[i])
			}
		}
	}
}

func TestPromptSchemaDocumentSandboxEnumIncludesConfiguredBackends(t *testing.T) {
	defaults := captainconfig.SandboxDefaults{Backends: map[string]captainconfig.SandboxBackend{
		"prod-pool":    {Kind: "git-agent"},
		"local-docker": {Kind: "container"},
		"srt":          {Kind: "srt"}, // a configured name must not duplicate a bare kind
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
	want := []any{"none", "srt", "container", "git-agent", "local-docker", "prod-pool"}
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
			if mode.Mode == string(api.ModeCmux) {
				t.Errorf("runtimes[] still offers the disabled %s mode", mode.Backend)
			}
			if mode.Disabled {
				t.Errorf("runtimes[] served a disabled entry instead of dropping it: %+v", mode)
			}
		}
	}

	for _, model := range doc["models"].([]map[string]any) {
		if efforts, ok := model["supportedEfforts"].([]string); ok && containsString(efforts, "ultra") {
			t.Errorf("model %v still offers the disabled ultra tier: %v", model["id"], efforts)
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
			seen[mode.Backend] = true
			if mode.DefaultModel == "" {
				t.Errorf("%s serves no default model, so a picker would have to hardcode one", mode.Backend)
			}
			if got := api.Backend(mode.Backend).Mode(); string(got) != mode.Mode {
				t.Errorf("%s mode = %q, but the backend parses as %q", mode.Backend, mode.Mode, got)
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

func TestInjectSpecConditionalsUsesOnlyModelBackedEfforts(t *testing.T) {
	spec := map[string]any{}
	adapters := []AdapterStatus{{
		Backend: string(api.BackendCodexAgent),
		ModelDetails: []ai.ModelDef{
			{ID: "gpt-5.6-sol", SupportedEfforts: []api.Effort{api.EffortLow, api.EffortMax}},
			{ID: "no-effort-model"},
		},
	}}
	if err := injectSpecConditionals(spec, adapters, nil); err != nil {
		t.Fatalf("injectSpecConditionals: %v", err)
	}
	rules := spec["allOf"].([]any)[0].(map[string]any)["then"].(map[string]any)["allOf"].([]any)
	got := map[string][]any{}
	for _, raw := range rules {
		rule := raw.(map[string]any)
		model := rule["if"].(map[string]any)["properties"].(map[string]any)["model"].(map[string]any)["const"].(string)
		got[model] = rule["then"].(map[string]any)["properties"].(map[string]any)["effort"].(map[string]any)["enum"].([]any)
	}
	if want := []any{"", "low", "max"}; !reflect.DeepEqual(got["gpt-5.6-sol"], want) {
		t.Errorf("Sol effort enum = %v, want %v", got["gpt-5.6-sol"], want)
	}
	if want := []any{""}; !reflect.DeepEqual(got["no-effort-model"], want) {
		t.Errorf("no-effort model enum = %v, want %v", got["no-effort-model"], want)
	}
}

func schemaModelForBackend(t *testing.T, models []map[string]any, id, backend string) map[string]any {
	t.Helper()
	for _, model := range models {
		if model["id"] != id {
			continue
		}
		backends, ok := model["backends"].([]string)
		if !ok {
			t.Fatalf("model %s backends = %T, want []string", id, model["backends"])
		}
		if containsString(backends, backend) {
			return model
		}
	}
	t.Fatalf("models[] missing id %q with backend %q: %+v", id, backend, models)
	return nil
}

func assertSchemaModelBackends(t *testing.T, model map[string]any, want ...string) {
	t.Helper()
	backends, ok := model["backends"].([]string)
	if !ok {
		t.Fatalf("model backends = %T, want []string", model["backends"])
	}
	for _, backend := range want {
		if !containsString(backends, backend) {
			t.Errorf("model backends = %v, missing %s", backends, backend)
		}
	}
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
	prev := schemaAdapters
	t.Cleanup(func() { schemaAdapters = prev })
	stub := stubbedSchemaAdapters(t)
	schemaAdapters = func() ([]AdapterStatus, error) { return stub, nil }

	var buf bytes.Buffer
	if err := WritePromptSchema(&buf); err != nil {
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
