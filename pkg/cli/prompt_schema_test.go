package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/spf13/cobra"
)

// fakeSchemaProbe reports no API keys and no CLI login files but all CLI binaries
// present. This keeps the probe hermetic (fetchAPIModels makes no network call
// without an API key) while CLI/agent backends still list their static catalog
// models, giving a deterministic adapter set.
func fakeSchemaProbe() authProbe {
	return authProbe{
		getenv:     func(string) string { return "" },
		lookPath:   func(bin string) (string, error) { return "/usr/local/bin/" + bin, nil },
		fileExists: func(string) bool { return false },
		home:       "/home/test",
	}
}

func stubbedSchemaAdapters(t *testing.T) []AdapterStatus {
	t.Helper()
	adapters, err := ProbeAdapters(WhoamiOptions{Models: true}, fakeSchemaProbe())
	if err != nil {
		t.Fatalf("ProbeAdapters: %v", err)
	}
	return adapters
}

func TestPromptSchemaDocumentBackendsAndConditionals(t *testing.T) {
	doc, err := buildPromptSchemaDocument(stubbedSchemaAdapters(t))
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

	// codex-cli's models come from the static catalog (one entry, AgentModel
	// "gpt-5-codex") - an independent baseline unaffected by API keys.
	codexModels := byName[string(api.BackendCodexCLI)]["models"].([]string)
	if !contains(codexModels, "gpt-5-codex") {
		t.Errorf("codex-cli models = %v, want to contain gpt-5-codex", codexModels)
	}

	// anthropic is unauthenticated in the fake probe, so its models fall back to
	// the catalog and the "set your key" hint is cleared.
	anthropic := byName[string(api.BackendAnthropic)]
	anthropicModels, _ := anthropic["models"].([]string)
	if !contains(anthropicModels, "claude-sonnet-5") {
		t.Errorf("unauthenticated anthropic models = %v, want catalog fallback incl claude-sonnet-5", anthropicModels)
	}
	if _, hasErr := anthropic["modelError"]; hasErr {
		t.Errorf("anthropic should carry no modelError once the catalog answers")
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

func TestCachedSchemaAdaptersReusesWithinTTL(t *testing.T) {
	prevProbe := probeSchemaAdapters
	prevCache, prevAt := schemaAdapterCache, schemaAdapterAt
	t.Cleanup(func() {
		probeSchemaAdapters = prevProbe
		schemaAdapterCache, schemaAdapterAt = prevCache, prevAt
	})

	stub := stubbedSchemaAdapters(t)
	calls := 0
	probeSchemaAdapters = func() ([]AdapterStatus, error) {
		calls++
		return stub, nil
	}
	schemaAdapterCache, schemaAdapterAt = nil, time.Time{}

	base := time.Unix(1_000_000, 0)
	if _, err := cachedSchemaAdapters(base); err != nil {
		t.Fatalf("cachedSchemaAdapters: %v", err)
	}
	if _, err := cachedSchemaAdapters(base.Add(10 * time.Second)); err != nil {
		t.Fatalf("cachedSchemaAdapters: %v", err)
	}
	if calls != 1 {
		t.Errorf("probe called %d times within TTL, want 1", calls)
	}
	if _, err := cachedSchemaAdapters(base.Add(2 * schemaAdapterCacheTTL)); err != nil {
		t.Fatalf("cachedSchemaAdapters: %v", err)
	}
	if calls != 2 {
		t.Errorf("probe called %d times after TTL expiry, want 2", calls)
	}
}

func TestWritePromptSchemaEmitsValidJSON(t *testing.T) {
	prevProbe := probeSchemaAdapters
	prevCache, prevAt := schemaAdapterCache, schemaAdapterAt
	t.Cleanup(func() {
		probeSchemaAdapters = prevProbe
		schemaAdapterCache, schemaAdapterAt = prevCache, prevAt
	})
	stub := stubbedSchemaAdapters(t)
	probeSchemaAdapters = func() ([]AdapterStatus, error) { return stub, nil }
	schemaAdapterCache, schemaAdapterAt = nil, time.Time{}

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
