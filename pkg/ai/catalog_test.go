package ai

import "testing"

func TestLookupModelDefault(t *testing.T) {
	m, err := LookupModel("")
	if err != nil {
		t.Fatalf("LookupModel(\"\"): %v", err)
	}
	if m.ID != DefaultModelID {
		t.Fatalf("default = %q, want %q", m.ID, DefaultModelID)
	}
	if !m.Default {
		t.Fatalf("default model %q must have Default=true", m.ID)
	}
}

func TestLookupModelUnknownFailsLoud(t *testing.T) {
	if _, err := LookupModel("provider/does-not-exist"); err == nil {
		t.Fatal("expected error for unknown model id")
	}
}

func TestExactlyOneDefaultMatchesConst(t *testing.T) {
	var defaults []string
	for _, m := range Catalog() {
		if m.Default {
			defaults = append(defaults, m.ID)
		}
	}
	if len(defaults) != 1 {
		t.Fatalf("want exactly one Default model, got %v", defaults)
	}
	if defaults[0] != DefaultModelID {
		t.Fatalf("Default model %q != DefaultModelID %q", defaults[0], DefaultModelID)
	}
}

func TestBareID(t *testing.T) {
	cases := map[string]struct {
		model Model
		want  string
	}{
		"api strips provider prefix": {Model{ID: "anthropic/claude-sonnet-4-5", Provider: Anthropic, Mode: ModeAPI}, "claude-sonnet-4-5"},
		"gemini googleai prefix":     {Model{ID: "googleai/gemini-2.5-pro", Provider: Google, Mode: ModeAPI}, "gemini-2.5-pro"},
		"agent id verbatim":          {Model{ID: "claude-sonnet-5", Provider: Anthropic, Mode: ModeAgent}, "claude-sonnet-5"},
		"codex slug id verbatim":     {Model{ID: "gpt-5.5", Provider: OpenAI, Mode: ModeCLI}, "gpt-5.5"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.model.BareID(); got != tc.want {
				t.Fatalf("BareID() = %q, want %q", got, tc.want)
			}
		})
	}
}

// "Agent" is a property of the mode alone — any local transport supervises a
// binary — so it needs no provider to answer.
func TestIsAgent(t *testing.T) {
	cases := map[RuntimeMode]bool{
		ModeAPI:   false,
		ModeAgent: true,
		ModeCLI:   true,
		ModeCmux:  true,
	}
	for mode, want := range cases {
		if got := (Model{Mode: mode}).IsAgent(); got != want {
			t.Fatalf("mode %q IsAgent() = %v, want %v", mode, got, want)
		}
	}
}

func TestRegisterModelAddsAndUpdates(t *testing.T) {
	t.Cleanup(ResetModelCatalog)

	if err := RegisterModel(Model{ID: "anthropic/new-model", Provider: Anthropic, Mode: ModeAPI, Label: "New"}); err != nil {
		t.Fatalf("RegisterModel add: %v", err)
	}
	m, err := LookupModel("anthropic/new-model")
	if err != nil || m.Label != "New" {
		t.Fatalf("added model missing/wrong: %+v err=%v", m, err)
	}

	if err := RegisterModel(Model{ID: "anthropic/new-model", Provider: Anthropic, Mode: ModeAPI, Label: "Updated"}); err != nil {
		t.Fatalf("RegisterModel update: %v", err)
	}
	m, _ = LookupModel("anthropic/new-model")
	if m.Label != "Updated" {
		t.Fatalf("update did not replace label: %q", m.Label)
	}
}

func TestSetModelCatalogAndReset(t *testing.T) {
	t.Cleanup(ResetModelCatalog)

	if err := SetModelCatalog([]Model{{ID: "openai/only", Provider: OpenAI, Mode: ModeAPI}}); err != nil {
		t.Fatalf("SetModelCatalog: %v", err)
	}
	if got := len(Catalog()); got != 1 {
		t.Fatalf("catalog size after Set = %d, want 1", got)
	}

	ResetModelCatalog()
	if got := len(Catalog()); got != len(defaultCatalog) {
		t.Fatalf("catalog size after Reset = %d, want %d", got, len(defaultCatalog))
	}
}

func TestRegisterModelValidation(t *testing.T) {
	t.Cleanup(ResetModelCatalog)

	if err := RegisterModel(Model{ID: "", Provider: Anthropic, Mode: ModeAPI}); err == nil {
		t.Fatal("expected error for empty ID")
	}
	if err := RegisterModel(Model{ID: "x/y"}); err == nil {
		t.Fatal("expected error for a missing runtime")
	}
	if err := RegisterModel(Model{ID: "x/y", Provider: OpenAI, Mode: RuntimeMode("nonsense")}); err == nil {
		t.Fatal("expected error for an invalid mode")
	}
	if err := RegisterModel(Model{ID: "x/y", Provider: OpenAI, Mode: ModeAPI, ReleaseDate: "not-a-date"}); err == nil {
		t.Fatal("expected error for invalid release date")
	}
}

func TestCatalogReleaseDateMatchesBareAndRuntimeIDs(t *testing.T) {
	t.Cleanup(ResetModelCatalog)

	if err := SetModelCatalog([]Model{
		{ID: "openai/gpt-test", Provider: OpenAI, Mode: ModeAPI, ReleaseDate: "2026-01-02"},
		{ID: "codex-gpt-test", Provider: OpenAI, Mode: ModeCLI, AgentModel: "gpt-test-runtime", ReleaseDate: "2026-03-04"},
	}); err != nil {
		t.Fatalf("SetModelCatalog: %v", err)
	}

	if got := CatalogReleaseDate(OpenAI, ModeAPI, "gpt-test"); got != "2026-01-02" {
		t.Fatalf("bare API release date = %q", got)
	}
	if got := CatalogReleaseDate(OpenAI, ModeCLI, "gpt-test-runtime"); got != "2026-03-04" {
		t.Fatalf("agent runtime release date = %q", got)
	}
}

func TestCurrentModelsByReleaseDateFiltersAndSorts(t *testing.T) {
	models := []ModelDef{
		{ID: "claude-sonnet-5", Provider: Anthropic.Name, Mode: ModeAPI, ReleaseDate: "2026-06-01"},
		{ID: "claude-sonnet-4-6", Provider: Anthropic.Name, Mode: ModeAPI, ReleaseDate: "2026-05-01"},
		{ID: "claude-sonnet-4-5", Provider: Anthropic.Name, Mode: ModeAPI, ReleaseDate: "2026-04-01"},
		{ID: "claude-sonnet-4-4", Provider: Anthropic.Name, Mode: ModeAPI, ReleaseDate: "2026-03-01"},
		{ID: "sonnet-5", Provider: Anthropic.Name, Mode: ModeCmux, ReleaseDate: "2026-06-01"},
		{ID: "sonnet-4-6", Provider: Anthropic.Name, Mode: ModeCmux, ReleaseDate: "2026-05-01"},
		{ID: "claude-haiku-4-5", Provider: Anthropic.Name, Mode: ModeAPI, ReleaseDate: "2025-10-15"},
		{ID: "gemini-3.0-pro", Provider: Google.Name, Mode: ModeAPI},
		{ID: "gpt-4o", Provider: OpenAI.Name, Mode: ModeAPI, ReleaseDate: "2026-04-01"},
		{ID: "gpt-5-codex", Provider: OpenAI.Name, Mode: ModeCLI, ReleaseDate: "2026-03-01"},
	}

	got := CurrentModelsByReleaseDate(models)
	want := []string{
		"claude-sonnet-5",
		"claude-sonnet-4-6",
		"claude-sonnet-4-5",
		"claude-haiku-4-5",
		"gemini-3.0-pro",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].ID != w {
			t.Errorf("got[%d].ID = %q, want %q", i, got[i].ID, w)
		}
	}
}

func TestModelFamilyPrefix(t *testing.T) {
	cases := map[string]string{
		"anthropic/claude-haiku-4-5": "claude-haiku",
		"claude-sonnet-5":            "claude-sonnet",
		"sonnet-5":                   "sonnet",
		"sonnet-4-6":                 "sonnet",
		"opus-4-8":                   "opus",
		"googleai/gemini-3.0-pro":    "gemini-pro",
		"gemini-3.5-flash":           "gemini-flash",
		"gemini-2.5-flash-lite":      "gemini-flash-lite",
		"openai/gpt-5.4-mini":        "gpt-mini",
		"openai/gpt-5.5":             "gpt",
	}
	for id, want := range cases {
		if got := ModelFamilyPrefix(id); got != want {
			t.Errorf("ModelFamilyPrefix(%q) = %q, want %q", id, got, want)
		}
	}
}
