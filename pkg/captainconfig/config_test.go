package captainconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func withTempPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".captain.yaml")
	SetPathForTesting(path)
	t.Cleanup(func() { SetPathForTesting("") })
	return path
}

func TestLoad_MissingFileReturnsZero(t *testing.T) {
	withTempPath(t)
	cfg, exists, err := Load()
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if exists {
		t.Errorf("Load() exists = true, want false for missing file")
	}
	if !reflect.DeepEqual(cfg, Config{}) {
		t.Errorf("Load() cfg = %+v, want zero", cfg)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	path := withTempPath(t)
	want := Config{
		AI: AIDefaults{
			Backend:         "anthropic",
			Model:           "claude-sonnet-4-6",
			ReasoningEffort: "medium",
			BudgetUSD:       2.5,
			MaxTokens:       8192,
			Temperature:     0.2,
			Timeout:         "180s",
			NoCache:         true,
			NoMCP:           true,
			NoHooks:         false,
			NoMemory:        true,
		},
	}
	if err := Save(want); err != nil {
		t.Fatalf("Save() err = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat saved file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Errorf("saved file mode = %o, want 0644", mode)
	}

	got, exists, err := Load()
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if !exists {
		t.Fatalf("Load() exists = false after Save")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got  = %+v\n want = %+v", got, want)
	}
}

func TestLoad_MalformedYAMLReturnsError(t *testing.T) {
	path := withTempPath(t)
	if err := os.WriteFile(path, []byte("ai: [not, a, mapping"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	_, _, err := Load()
	if err == nil {
		t.Fatal("Load() err = nil, want parse error for malformed YAML")
	}
}

func TestSave_AtomicLeavesNoTempFile(t *testing.T) {
	path := withTempPath(t)
	if err := Save(Config{AI: AIDefaults{Model: "x"}}); err != nil {
		t.Fatalf("Save() err = %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == ".captain.yaml" {
			continue
		}
		t.Errorf("found stray file alongside config: %s", name)
	}
}
