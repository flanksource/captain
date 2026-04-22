package fixture

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_PopulatesDir(t *testing.T) {
	path := writeFixture(t, `prompt: hi
runs:
  - name: a
    model: m
`)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Dir != filepath.Dir(path) {
		t.Errorf("Dir = %q, want %q", f.Dir, filepath.Dir(path))
	}
	if len(f.Runs) != 1 {
		t.Errorf("got %d runs, want 1", len(f.Runs))
	}
}

func TestLoad_RequiresAtLeastOneRun(t *testing.T) {
	path := writeFixture(t, `prompt: hi
runs: []
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty runs")
	}
}

func TestMerge_RunOverridesDefaults(t *testing.T) {
	tru := true
	fls := false
	f := &Fixture{
		Prompt: "top-prompt",
		Defaults: Run{
			Model:         "default-model",
			Timeout:       "1m",
			PromptCaching: &tru,
			Tools:         []string{"Bash"},
		},
	}
	merged := f.Merge(Run{
		Name:          "override",
		Model:         "custom-model",
		PromptCaching: &fls,
		Tools:         []string{"Read"},
	})
	if merged.Model != "custom-model" {
		t.Errorf("Model = %q, want custom-model", merged.Model)
	}
	if merged.Timeout != "1m" {
		t.Errorf("Timeout = %q, want inherited 1m", merged.Timeout)
	}
	if merged.PromptCaching == nil || *merged.PromptCaching {
		t.Errorf("PromptCaching = %v, want false override", merged.PromptCaching)
	}
	if len(merged.Tools) != 1 || merged.Tools[0] != "Read" {
		t.Errorf("Tools = %v, want [Read]", merged.Tools)
	}
	if merged.Prompt != "top-prompt" {
		t.Errorf("Prompt = %q, want fixture-level fallback", merged.Prompt)
	}
}
