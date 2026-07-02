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
		"api strips provider prefix": {Model{ID: "anthropic/claude-sonnet-4-5", Backend: BackendAnthropic}, "claude-sonnet-4-5"},
		"gemini googleai prefix":     {Model{ID: "googleai/gemini-2.5-pro", Backend: BackendGemini}, "gemini-2.5-pro"},
		"agent id verbatim":          {Model{ID: "claude-agent-sonnet", Backend: BackendClaudeAgent}, "claude-agent-sonnet"},
		"codex slug id verbatim":     {Model{ID: "codex-gpt-5-codex", Backend: BackendCodexCLI, AgentModel: "gpt-5-codex"}, "codex-gpt-5-codex"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.model.BareID(); got != tc.want {
				t.Fatalf("BareID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsAgent(t *testing.T) {
	cases := map[Backend]bool{
		BackendAnthropic:   false,
		BackendOpenAI:      false,
		BackendGemini:      false,
		BackendClaudeAgent: true,
		BackendCodexCLI:    true,
		BackendClaudeCLI:   true,
	}
	for b, want := range cases {
		if got := (Model{Backend: b}).IsAgent(); got != want {
			t.Fatalf("Backend %q IsAgent() = %v, want %v", b, got, want)
		}
	}
}

func TestRegisterModelAddsAndUpdates(t *testing.T) {
	t.Cleanup(ResetModelCatalog)

	if err := RegisterModel(Model{ID: "anthropic/new-model", Backend: BackendAnthropic, Label: "New"}); err != nil {
		t.Fatalf("RegisterModel add: %v", err)
	}
	m, err := LookupModel("anthropic/new-model")
	if err != nil || m.Label != "New" {
		t.Fatalf("added model missing/wrong: %+v err=%v", m, err)
	}

	if err := RegisterModel(Model{ID: "anthropic/new-model", Backend: BackendAnthropic, Label: "Updated"}); err != nil {
		t.Fatalf("RegisterModel update: %v", err)
	}
	m, _ = LookupModel("anthropic/new-model")
	if m.Label != "Updated" {
		t.Fatalf("update did not replace label: %q", m.Label)
	}
}

func TestSetModelCatalogAndReset(t *testing.T) {
	t.Cleanup(ResetModelCatalog)

	if err := SetModelCatalog([]Model{{ID: "openai/only", Backend: BackendOpenAI}}); err != nil {
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

	if err := RegisterModel(Model{ID: "", Backend: BackendAnthropic}); err == nil {
		t.Fatal("expected error for empty ID")
	}
	if err := RegisterModel(Model{ID: "x/y"}); err == nil {
		t.Fatal("expected error for missing Backend")
	}
	if err := RegisterModel(Model{ID: "x/y", Backend: Backend("nonsense")}); err == nil {
		t.Fatal("expected error for invalid Backend")
	}
	if err := RegisterModel(Model{ID: "x/y", Backend: BackendOpenAI, ReleaseDate: "not-a-date"}); err == nil {
		t.Fatal("expected error for invalid release date")
	}
}

func TestCatalogReleaseDateMatchesBareAndRuntimeIDs(t *testing.T) {
	t.Cleanup(ResetModelCatalog)

	if err := SetModelCatalog([]Model{
		{ID: "openai/gpt-test", Backend: BackendOpenAI, ReleaseDate: "2026-01-02"},
		{ID: "codex-gpt-test", Backend: BackendCodexCLI, AgentModel: "gpt-test-runtime", ReleaseDate: "2026-03-04"},
	}); err != nil {
		t.Fatalf("SetModelCatalog: %v", err)
	}

	if got := CatalogReleaseDate(BackendOpenAI, "gpt-test"); got != "2026-01-02" {
		t.Fatalf("bare API release date = %q", got)
	}
	if got := CatalogReleaseDate(BackendCodexCLI, "gpt-test-runtime"); got != "2026-03-04" {
		t.Fatalf("agent runtime release date = %q", got)
	}
}

func TestCurrentModelsByReleaseDateFiltersAndSorts(t *testing.T) {
	models := []ModelDef{
		{ID: "gpt-old-unknown", Backend: BackendOpenAI},
		{ID: "gpt-4o", Backend: BackendOpenAI, ReleaseDate: "2026-04-01"},
		{ID: "gpt-new", Backend: BackendOpenAI, ReleaseDate: "2026-05-01"},
		{ID: "gpt-mid", Backend: BackendOpenAI, ReleaseDate: "2026-04-15"},
		{ID: "gpt-5-codex", Backend: BackendCodexCLI, ReleaseDate: "2026-03-01"},
	}

	got := CurrentModelsByReleaseDate(models)
	want := []string{"gpt-new", "gpt-mid", "gpt-5-codex", "gpt-old-unknown"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].ID != w {
			t.Errorf("got[%d].ID = %q, want %q", i, got[i].ID, w)
		}
	}
}
