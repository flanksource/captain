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
		if strings.Contains(snippet, "nodejs") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("golang install snippets should reference 'nodejs', got: %v", result)
	}
}

func TestGetDependencyDirsNpm(t *testing.T) {
	result := GetDependencyDirs([]string{"npm"})
	if len(result) != 1 || result[0] != "node_modules" {
		t.Errorf("expected [node_modules], got %v", result)
	}
}

func TestGetDependencyDirsDedup(t *testing.T) {
	result := GetDependencyDirs([]string{"npm", "nextjs"})
	count := 0
	for _, d := range result {
		if d == "node_modules" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected node_modules once, got %d times in %v", count, result)
	}
	hasNext := false
	for _, d := range result {
		if d == ".next" {
			hasNext = true
		}
	}
	if !hasNext {
		t.Errorf("expected .next in result, got %v", result)
	}
}

func TestGetDependencyDirsEmpty(t *testing.T) {
	if result := GetDependencyDirs(nil); result != nil {
		t.Errorf("expected nil, got %v", result)
	}
	if result := GetDependencyDirs([]string{"nonexistent"}); result != nil {
		t.Errorf("expected nil for unknown, got %v", result)
	}
}

func TestInstallSnippetsMultiple(t *testing.T) {
	result := InstallSnippets([]string{"golang", "playwright"})
	if len(result) < 2 {
		t.Errorf("expected at least 2 snippets from golang+playwright, got %d", len(result))
	}

	hasNodejs := false
	hasPlaywright := false
	for _, snippet := range result {
		if strings.Contains(snippet, "nodejs") {
			hasNodejs = true
		}
		if strings.Contains(snippet, "playwright") {
			hasPlaywright = true
		}
	}
	if !hasNodejs {
		t.Error("expected nodejs snippet in result")
	}
	if !hasPlaywright {
		t.Error("expected playwright snippet in result")
	}
}

func TestGetBaseImageGolang(t *testing.T) {
	versions := map[string]string{"golang": "1.24", "node": "22"}
	result := GetBaseImage([]string{"golang"}, versions)
	if result != "golang:1.24" {
		t.Errorf("expected golang:1.24, got %q", result)
	}
}

func TestGetBaseImageNpm(t *testing.T) {
	versions := map[string]string{"node": "20"}
	result := GetBaseImage([]string{"npm"}, versions)
	if result != "node:20" {
		t.Errorf("expected node:20, got %q", result)
	}
}

func TestGetBaseImageNoVersion(t *testing.T) {
	result := GetBaseImage([]string{"docker"}, map[string]string{})
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestGetBaseImageFirstWins(t *testing.T) {
	versions := map[string]string{"golang": "1.24", "node": "22"}
	result := GetBaseImage([]string{"golang", "npm"}, versions)
	if result != "golang:1.24" {
		t.Errorf("expected golang:1.24 (first wins), got %q", result)
	}
}

func TestResolveInstallSnippets(t *testing.T) {
	versions := map[string]string{"node": "22"}
	result := ResolveInstallSnippets([]string{"golang"}, versions)
	if len(result) != 1 {
		t.Fatalf("expected 1 snippet, got %d", len(result))
	}
	if !strings.Contains(result[0], "setup_22.x") {
		t.Errorf("expected nodeVersion resolved to 22, got %q", result[0])
	}
}

func TestResolveTemplate(t *testing.T) {
	versions := map[string]string{"golang": "1.24", "node": "22"}
	if r := ResolveTemplate("golang:{{.golang}}", versions); r != "golang:1.24" {
		t.Errorf("expected golang:1.24, got %q", r)
	}
	if r := ResolveTemplate("setup_{{.nodeVersion}}.x", versions); r != "setup_22.x" {
		t.Errorf("expected setup_22.x, got %q", r)
	}
}
