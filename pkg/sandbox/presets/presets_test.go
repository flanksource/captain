package presets

import (
	"strings"
	"testing"
)

func TestInstallSnippetsEmpty(t *testing.T) {
	if result := InstallSnippets(nil); result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}
}

func TestInstallSnippetsUnknown(t *testing.T) {
	result := InstallSnippets([]string{"nonexistent-preset-xyz"})
	if result != nil {
		t.Errorf("expected nil for unknown preset, got %v", result)
	}
}

func TestInstallSnippetsGolang(t *testing.T) {
	result := InstallSnippets([]string{"golang"})
	if len(result) == 0 {
		t.Fatal("expected non-empty install snippets for golang preset")
	}
	found := false
	for _, snippet := range result {
		if strings.Contains(snippet, "go") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("golang install snippets should reference 'go', got: %v", result)
	}
}

func TestInstallSnippetsMultiple(t *testing.T) {
	result := InstallSnippets([]string{"golang", "python"})
	if len(result) < 2 {
		t.Errorf("expected at least 2 snippets from golang+python, got %d", len(result))
	}

	hasGo := false
	hasPython := false
	for _, snippet := range result {
		if strings.Contains(snippet, "go") {
			hasGo = true
		}
		if strings.Contains(snippet, "python") {
			hasPython = true
		}
	}
	if !hasGo {
		t.Error("expected golang snippet in result")
	}
	if !hasPython {
		t.Error("expected python snippet in result")
	}
}
