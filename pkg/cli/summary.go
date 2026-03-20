package cli

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
)

type SummaryResult struct {
	TotalToolUses int           `json:"totalToolUses" pretty:"label=Total Tool Uses"`
	DeniedCount   int           `json:"deniedCount,omitempty" pretty:"label=Denied"`
	Tools         []NameCount   `json:"tools" pretty:"label=Tools"`
	Paths         []PathSummary `json:"paths" pretty:"label=Paths"`
	Domains       []NameCount   `json:"domains" pretty:"label=Domains"`
	EnvVars       []NameCount   `json:"envVars" pretty:"label=Env Vars"`
	Binaries      []NameCount   `json:"binaries" pretty:"label=Binaries"`
	Categories    []NameCount   `json:"categories" pretty:"label=Categories"`
	Cost          CostSummary   `json:"cost" pretty:"label=Cost"`
}

type NameCount struct {
	Name  string `json:"name" pretty:"label=Name,table"`
	Count int    `json:"count" pretty:"label=Count,table"`
}

type PathSummary struct {
	Path       string `json:"path" pretty:"label=Path,table"`
	ReadCount  int    `json:"readCount" pretty:"label=Reads,table"`
	WriteCount int    `json:"writeCount" pretty:"label=Writes,table"`
}

type CostSummary struct {
	InputTokens      int     `json:"inputTokens" pretty:"label=Input Tokens"`
	OutputTokens     int     `json:"outputTokens" pretty:"label=Output Tokens"`
	CacheReadTokens  int     `json:"cacheReadTokens" pretty:"label=Cache Read"`
	CacheWriteTokens int     `json:"cacheWriteTokens" pretty:"label=Cache Write"`
	TotalCost        float64 `json:"totalCost" pretty:"label=Total Cost"`
	CostDisplay      string  `json:"costDisplay" pretty:"label=Cost"`
	CacheHitRatio    float64 `json:"cacheHitRatio" pretty:"label=Cache Hit %"`
}

func BuildSummary(toolUses []claude.ToolUse, classifier *bash.CategoryClassifier, costs []claude.SessionCost) SummaryResult {
	tools := make(map[string]int)
	readDirs := make(map[string]int)
	writeDirs := make(map[string]int)
	domains := make(map[string]int)
	envVars := make(map[string]int)
	binaries := make(map[string]int)
	categories := make(map[string]int)
	var denied int

	for _, tu := range toolUses {
		if tu.CWD != "" && tu.ProjectRoot == "" {
			tu.ProjectRoot = claude.FindProjectRoot(tu.CWD)
		}

		tools[tu.DisplayTool()]++

		if tu.Denied {
			denied++
		}

		category := classifier.ClassifyToolWithPath(tu.Tool, tu.FilePath())
		if category == bash.CategoryOther && tu.Tool == "Bash" {
			if rawCmd, ok := tu.Input["command"].(string); ok {
				category = classifier.ClassifyBash(rawCmd)
			}
		}
		categories[string(category)]++

		analysis := AnalyzeToolUse(tu, tu.ProjectRoot)
		for _, p := range analysis.ReadPaths {
			readDirs[p]++
		}
		for _, p := range analysis.WritePaths {
			writeDirs[p]++
		}
		for _, d := range analysis.Domains {
			domains[d]++
		}
		for _, b := range analysis.Binaries {
			binaries[b]++
		}
		for _, v := range ExtractEnvVars(tu) {
			envVars[v]++
		}
	}

	result := SummaryResult{
		TotalToolUses: len(toolUses),
		DeniedCount:   denied,
		Tools:         toNameCounts(tools),
		Domains:       toNameCounts(domains),
		EnvVars:       toNameCounts(envVars),
		Binaries:      toNameCounts(binaries),
		Categories:    toNameCounts(categories),
		Paths:         toPathSummaries(readDirs, writeDirs),
	}

	applyCostToSummary(&result, costs)
	return result
}

func toNameCounts(m map[string]int) []NameCount {
	result := make([]NameCount, 0, len(m))
	for k, v := range m {
		result = append(result, NameCount{Name: k, Count: v})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func toPathSummaries(reads, writes map[string]int) []PathSummary {
	allPaths := make(map[string]bool)
	for k := range reads {
		allPaths[k] = true
	}
	for k := range writes {
		allPaths[k] = true
	}

	result := make([]PathSummary, 0, len(allPaths))
	for p := range allPaths {
		result = append(result, PathSummary{
			Path:       p,
			ReadCount:  reads[p],
			WriteCount: writes[p],
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})
	return result
}

func applyCostToSummary(result *SummaryResult, costs []claude.SessionCost) {
	for _, c := range costs {
		result.Cost.InputTokens += c.Tokens.InputTokens
		result.Cost.OutputTokens += c.Tokens.OutputTokens
		result.Cost.CacheReadTokens += c.Tokens.CacheReadTokens
		result.Cost.CacheWriteTokens += c.Tokens.CacheWriteTokens
		result.Cost.TotalCost += c.Tokens.TotalCost
	}
	result.Cost.CostDisplay = fmt.Sprintf("$%.4f", result.Cost.TotalCost)

	totalInput := result.Cost.InputTokens + result.Cost.CacheReadTokens + result.Cost.CacheWriteTokens
	if totalInput > 0 {
		result.Cost.CacheHitRatio = float64(result.Cost.CacheReadTokens) / float64(totalInput) * 100
	}
}

var envVarRe = regexp.MustCompile(`\$\{?([A-Z_][A-Z0-9_]*)\}?`)

func ExtractEnvVars(tu claude.ToolUse) []string {
	seen := make(map[string]bool)

	extract := func(s string) {
		for _, m := range envVarRe.FindAllStringSubmatch(s, -1) {
			seen[m[1]] = true
		}
	}

	switch tu.Tool {
	case "Bash":
		if cmd, ok := tu.Input["command"].(string); ok {
			extract(cmd)
		}
	case "WebFetch":
		if urlStr, ok := tu.Input["url"].(string); ok {
			extract(urlStr)
		}
	default:
		if strings.HasPrefix(tu.Tool, "mcp__") {
			for _, v := range tu.Input {
				if s, ok := v.(string); ok {
					extract(s)
				}
			}
		}
	}

	result := make([]string, 0, len(seen))
	for k := range seen {
		result = append(result, k)
	}
	sort.Strings(result)
	return result
}
