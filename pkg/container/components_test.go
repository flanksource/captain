package container

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComponentString(t *testing.T) {
	c := Component{Category: CategoryAgents, Name: "design"}
	if got := c.String(); got != "agents/design" {
		t.Errorf("got %q, want %q", got, "agents/design")
	}
}

func TestCountSelected(t *testing.T) {
	components := []Component{
		{Category: CategoryAgents, Name: "a", Selected: true},
		{Category: CategoryAgents, Name: "b", Selected: false},
		{Category: CategorySkills, Name: "c", Selected: true},
	}
	if got := CountSelected(components); got != 2 {
		t.Errorf("CountSelected = %d, want 2", got)
	}
}

func TestCountSelectedByCategory(t *testing.T) {
	components := []Component{
		{Category: CategoryAgents, Name: "a", Selected: true},
		{Category: CategoryAgents, Name: "b", Selected: false},
		{Category: CategorySkills, Name: "c", Selected: true},
	}
	sel, tot := CountSelectedByCategory(components, CategoryAgents)
	if sel != 1 || tot != 2 {
		t.Errorf("got sel=%d tot=%d, want sel=1 tot=2", sel, tot)
	}
}

func TestFilterByCategory(t *testing.T) {
	components := []Component{
		{Category: CategoryAgents, Name: "a"},
		{Category: CategorySkills, Name: "b"},
		{Category: CategoryAgents, Name: "c"},
	}
	result := FilterByCategory(components, CategoryAgents)
	if len(result) != 2 {
		t.Errorf("got %d, want 2", len(result))
	}
}

func TestApplyDefaults(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	depDir := t.TempDir()
	depDir, _ = filepath.EvalSymlinks(depDir)
	_ = os.MkdirAll(filepath.Join(depDir, ".git"), 0o755)

	origDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(origDir) }()

	components := []Component{
		{Category: CategoryAgents, Name: "agent1"},
		{Category: CategorySkills, Name: "skill1"},
		{Category: CategoryClaudeMD, Name: "CLAUDE.md", DefaultSelected: true},
		{Category: CategoryProjects, Name: "cwd-project", GitRoot: dir},
		{Category: CategoryProjects, Name: "other-project", GitRoot: "/some/other/project"},
		{Category: CategoryProjects, Name: "dep-project", GitRoot: depDir},
	}

	ApplyDefaults(components, []string{depDir})

	expected := map[string]bool{
		"agent1":        false,
		"skill1":        true,
		"CLAUDE.md":     true,
		"cwd-project":   true,
		"other-project": false,
		"dep-project":   true,
	}
	for _, c := range components {
		want := expected[c.Name]
		if c.Selected != want {
			t.Errorf("%s: Selected=%v, want %v", c.Name, c.Selected, want)
		}
	}
}

func TestDefaultSelectionExcludesNonCWDProjects(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)

	origDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(origDir) }()

	components := []Component{
		{Category: CategoryAgents, Name: "agent1"},
		{Category: CategorySettings, Name: "setting1"},
		{Category: CategoryProjects, Name: "cwd-project", GitRoot: dir},
		{Category: CategoryProjects, Name: "other-project", GitRoot: "/some/other/project"},
	}

	// Simulate discoverDefaultSelected logic: select all non-projects, then ApplyDefaults
	for i := range components {
		if components[i].Category != CategoryProjects {
			components[i].Selected = true
		}
	}
	ApplyDefaults(components)

	expected := map[string]bool{
		"agent1":        true,
		"setting1":      true,
		"cwd-project":   true,
		"other-project": false,
	}
	for _, c := range components {
		want := expected[c.Name]
		if c.Selected != want {
			t.Errorf("%s: Selected=%v, want %v", c.Name, c.Selected, want)
		}
	}
}
