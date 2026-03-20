package container

import (
	"fmt"
	"os"
)

type Mode string

const (
	ModeCopy  Mode = "copy"
	ModeMount Mode = "mount"
)

type Category string

const (
	CategoryAgents       Category = "agents"
	CategorySkills       Category = "skills"
	CategoryMCP          Category = "mcp"
	CategoryPlugins      Category = "plugins"
	CategoryCommands     Category = "commands"
	CategoryHooks        Category = "hooks"
	CategorySettings     Category = "settings"
	CategoryMCPServers   Category = "mcp-servers"
	CategoryAuth         Category = "auth"
	CategoryFeatureFlags Category = "feature-flags"
	CategoryProjects     Category = "projects"
	CategoryClaudeMD     Category = "claude-md"
)

var AllCategories = []Category{
	CategoryAgents,
	CategorySkills,
	CategoryMCP,
	CategoryPlugins,
	CategoryCommands,
	CategoryHooks,
	CategorySettings,
	CategoryMCPServers,
	CategoryAuth,
	CategoryFeatureFlags,
	CategoryProjects,
	CategoryClaudeMD,
}

func (c Category) Icon() string {
	switch c {
	case CategoryAgents:
		return "*"
	case CategorySkills:
		return ">"
	case CategoryMCP:
		return "~"
	case CategoryPlugins:
		return "+"
	case CategoryCommands:
		return "$"
	case CategoryHooks:
		return "^"
	case CategorySettings:
		return "#"
	case CategoryMCPServers:
		return "@"
	case CategoryAuth:
		return "!"
	case CategoryFeatureFlags:
		return "%"
	case CategoryProjects:
		return "/"
	case CategoryClaudeMD:
		return "&"
	default:
		return "?"
	}
}

type Component struct {
	Category        Category `yaml:"category"`
	Name            string   `yaml:"name"`
	SourcePath      string   `yaml:"source_path"`
	TargetPath      string   `yaml:"target_path"`
	IsDir           bool     `yaml:"is_dir"`
	Description     string   `yaml:"description,omitempty"`
	Selected        bool     `yaml:"-"`
	ContentKey      string   `yaml:"content_key,omitempty"`
	ProjectPath     string   `yaml:"project_path,omitempty"`
	OptionValue     string   `yaml:"option_value,omitempty"`
	DefaultSelected bool     `yaml:"default_selected,omitempty"`
	GitRoot         string   `yaml:"git_root,omitempty"`
	LastAccess      string   `yaml:"last_access,omitempty"`
}

func (c Component) String() string {
	return fmt.Sprintf("%s/%s", c.Category, c.Name)
}

func FilterByCategory(components []Component, cat Category) []Component {
	var result []Component
	for _, c := range components {
		if c.Category == cat {
			result = append(result, c)
		}
	}
	return result
}

func CountSelected(components []Component) int {
	count := 0
	for _, c := range components {
		if c.Selected {
			count++
		}
	}
	return count
}

func ApplyDefaults(components []Component, autoSelectRoots ...[]string) {
	roots := make(map[string]bool)
	if pwd, err := os.Getwd(); err == nil {
		if cwdRoot := FindGitRoot(pwd); cwdRoot != "" {
			roots[cwdRoot] = true
		}
	}
	for _, list := range autoSelectRoots {
		for _, r := range list {
			roots[r] = true
		}
	}

	for i := range components {
		if components[i].DefaultSelected {
			components[i].Selected = true
		}
		if components[i].Category == CategorySkills {
			components[i].Selected = true
		}
		if components[i].Category == CategoryProjects && components[i].GitRoot != "" && roots[components[i].GitRoot] {
			components[i].Selected = true
		}
	}
}

func CountSelectedByCategory(components []Component, cat Category) (selected, total int) {
	for _, c := range components {
		if c.Category == cat {
			total++
			if c.Selected {
				selected++
			}
		}
	}
	return
}

func FilterSelected(components []Component) []Component {
	var result []Component
	for _, c := range components {
		if c.Selected {
			result = append(result, c)
		}
	}
	return result
}

func GroupByCategory(components []Component) map[Category][]Component {
	m := make(map[Category][]Component)
	for _, c := range components {
		m[c.Category] = append(m[c.Category], c)
	}
	return m
}
