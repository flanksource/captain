package bash

import (
	"embed"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed category_config.yaml
var defaultCategoryConfigFS embed.FS

type Category string

const (
	CategoryBuild   Category = "build"
	CategoryTest    Category = "test"
	CategoryInstall Category = "install"
	CategoryExplore Category = "explore"
	CategoryLint    Category = "lint"
	CategoryCleanup Category = "cleanup"
	CategoryGit     Category = "git"
	CategoryDocker  Category = "docker"
	CategoryK8s     Category = "k8s"
	CategoryRun     Category = "run"
	CategoryPlan    Category = "plan"
	CategoryEdit    Category = "edit"
	CategoryClarify Category = "clarify"
	CategoryRead    Category = "read"
	CategoryOther   Category = "other"
)

var categoryPriority = map[Category]int{
	CategoryInstall: 100,
	CategoryEdit:    90,
	CategoryRun:     80,
	CategoryTest:    70,
	CategoryBuild:   60,
	CategoryCleanup: 50,
	CategoryDocker:  40,
	CategoryK8s:     40,
	CategoryGit:     30,
	CategoryExplore: 20,
	CategoryLint:    20,
	CategoryPlan:    20,
	CategoryRead:    15,
	CategoryClarify: 10,
	CategoryOther:   0,
}

type CategoryRule struct {
	Commands []string `yaml:"commands"`
	Patterns []string `yaml:"patterns"`
	Tools    []string `yaml:"tools"`
}

type CategoryConfig struct {
	Categories map[Category]CategoryRule `yaml:"categories"`
}

type CategoryClassifier struct {
	config   *CategoryConfig
	compiled map[Category][]*regexp.Regexp
}

type categoryMatch struct {
	category    Category
	specificity int
	matched     bool
}

func NewCategoryClassifier(config *CategoryConfig) *CategoryClassifier {
	c := &CategoryClassifier{
		config:   config,
		compiled: make(map[Category][]*regexp.Regexp),
	}
	for cat, rule := range config.Categories {
		for _, pattern := range rule.Patterns {
			if re, err := regexp.Compile(pattern); err == nil {
				c.compiled[cat] = append(c.compiled[cat], re)
			}
		}
	}
	return c
}

func (c *CategoryClassifier) Classify(command string) Category {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return CategoryOther
	}

	best := categoryMatch{category: CategoryOther}
	for cat, rule := range c.config.Categories {
		for _, prefix := range rule.Commands {
			if cmd == prefix || strings.HasPrefix(cmd, prefix+" ") {
				best = betterCategoryMatch(best, categoryMatch{
					category:    cat,
					specificity: len(prefix),
					matched:     true,
				})
			}
		}
	}

	for cat, regexps := range c.compiled {
		for _, re := range regexps {
			if re.MatchString(cmd) {
				best = betterCategoryMatch(best, categoryMatch{
					category:    cat,
					specificity: len(re.String()),
					matched:     true,
				})
			}
		}
	}

	return best.category
}

func higherPriorityCategory(current, candidate Category) Category {
	if candidate == CategoryOther {
		return current
	}
	if current == CategoryOther {
		return candidate
	}

	currentPriority := categoryPriority[current]
	candidatePriority := categoryPriority[candidate]
	if candidatePriority > currentPriority {
		return candidate
	}
	if candidatePriority == currentPriority && current != CategoryOther && string(candidate) < string(current) {
		return candidate
	}
	return current
}

func betterCategoryMatch(current, candidate categoryMatch) categoryMatch {
	if !candidate.matched || candidate.category == CategoryOther {
		return current
	}
	if !current.matched || current.category == CategoryOther {
		return candidate
	}
	if candidate.specificity > current.specificity {
		return candidate
	}
	if candidate.specificity < current.specificity {
		return current
	}

	candidatePriority := categoryPriority[candidate.category]
	currentPriority := categoryPriority[current.category]
	if candidatePriority > currentPriority {
		return candidate
	}
	if candidatePriority < currentPriority {
		return current
	}
	if string(candidate.category) < string(current.category) {
		return candidate
	}
	return current
}

func LoadCategoryConfig(path string) (*CategoryConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config CategoryConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func DefaultCategoryConfig() *CategoryConfig {
	data, _ := defaultCategoryConfigFS.ReadFile("category_config.yaml")
	var config CategoryConfig
	_ = yaml.Unmarshal(data, &config)
	return &config
}

// ClassifyTool returns the category for a Claude tool name
func (c *CategoryClassifier) ClassifyTool(tool string) Category {
	for cat, rule := range c.config.Categories {
		for _, t := range rule.Tools {
			if tool == t {
				return cat
			}
		}
	}
	return CategoryOther
}

// ClassifyToolWithPath returns CategoryPlan for .claude/plans/ paths, otherwise delegates to ClassifyTool
func (c *CategoryClassifier) ClassifyToolWithPath(tool, filePath string) Category {
	if filePath != "" && strings.Contains(filePath, "/.claude/plans/") {
		return CategoryPlan
	}
	return c.ClassifyTool(tool)
}

// ClassifyBash parses a bash command and returns the highest priority category
func (c *CategoryClassifier) ClassifyBash(command string) Category {
	result, err := Analyze(command)
	if err != nil || len(result.Commands) == 0 {
		return c.Classify(command)
	}

	highest := CategoryOther
	for _, cmd := range result.Commands {
		cat := c.Classify(cmd)
		highest = higherPriorityCategory(highest, cat)
	}
	return highest
}

// CategoryPriority returns the priority value for a category (higher = more impactful)
func CategoryPriority(cat Category) int {
	return categoryPriority[cat]
}
