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
	TotalToolUses    int                      `json:"totalToolUses" pretty:"label=Total Tool Uses"`
	DeniedCount      int                      `json:"deniedCount,omitempty" pretty:"label=Denied"`
	Tools            []UsageSummary           `json:"tools" pretty:"label=Tools"`
	Paths            []UsageSummary           `json:"paths" pretty:"label=Paths"`
	Domains          []UsageSummary           `json:"domains" pretty:"label=Domains"`
	EnvVars          []UsageSummary           `json:"envVars" pretty:"label=Env Vars"`
	Binaries         []UsageSummary           `json:"binaries" pretty:"label=Binaries"`
	Categories       []UsageSummary           `json:"categories" pretty:"label=Categories"`
	Cost             CostSummary              `json:"cost" pretty:"label=Cost"`
	ContextBreakdown *claude.ContextBreakdown `json:"contextBreakdown,omitempty" pretty:"label=Context Breakdown"`
}

type UsageSummary struct {
	Name     string `json:"name" pretty:"label=Name,table"`
	Count    int    `json:"count" pretty:"label=Count,table"`
	Tokens   string `json:"tokens,omitempty" pretty:"label=Tokens,table"`
	Cost     string `json:"cost,omitempty" pretty:"label=Cost,table"`
	Errors   int    `json:"errors,omitempty" pretty:"label=Errors,table"`
	Duration string `json:"duration,omitempty" pretty:"label=Duration,table"`

	tokens int // raw token count for sorting
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
	toolTokens := make(map[string]int)
	toolErrors := make(map[string]int)
	paths := make(map[string]int)
	pathTokens := make(map[string]int)
	domains := make(map[string]int)
	domainTokens := make(map[string]int)
	envVars := make(map[string]int)
	binaries := make(map[string]int)
	binaryTokens := make(map[string]int)
	categories := make(map[string]int)
	categoryTokens := make(map[string]int)
	var denied int

	for _, tu := range toolUses {
		if tu.CWD != "" && tu.ProjectRoot == "" {
			tu.ProjectRoot = claude.FindProjectRoot(tu.CWD)
		}

		name := tu.DisplayTool()
		tuTokens := tu.InputTokens + tu.OutputTokens
		tools[name]++
		toolTokens[name] += tuTokens
		if tu.IsError {
			toolErrors[name]++
		}

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
		categoryTokens[string(category)] += tuTokens

		analysis := AnalyzeToolUse(tu, tu.ProjectRoot)
		for _, p := range analysis.ReadPaths {
			paths[p]++
			pathTokens[p] += tuTokens
		}
		for _, p := range analysis.WritePaths {
			paths[p]++
			pathTokens[p] += tuTokens
		}
		for _, d := range analysis.Domains {
			domains[d]++
			domainTokens[d] += tuTokens
		}
		for _, b := range analysis.Binaries {
			binaries[b]++
			binaryTokens[b] += tuTokens
		}
		for _, v := range ExtractEnvVars(tu) {
			envVars[v]++
		}
	}

	toolSummaries := toUsageSummaries(tools, toolTokens)
	for i := range toolSummaries {
		toolSummaries[i].Errors = toolErrors[toolSummaries[i].Name]
	}

	result := SummaryResult{
		TotalToolUses: len(toolUses),
		DeniedCount:   denied,
		Tools:         toolSummaries,
		Paths:         toUsageSummaries(paths, pathTokens),
		Domains:       toUsageSummaries(domains, domainTokens),
		EnvVars:       toUsageSummaries(envVars, nil),
		Binaries:      toUsageSummaries(binaries, binaryTokens),
		Categories:    toUsageSummaries(categories, categoryTokens),
	}

	applyCostToSummary(&result, costs)
	return result
}

func toUsageSummaries(counts map[string]int, tokens map[string]int) []UsageSummary {
	result := make([]UsageSummary, 0, len(counts))
	for name, count := range counts {
		t := 0
		if tokens != nil {
			t = tokens[name]
		}
		us := UsageSummary{
			Name:   name,
			Count:  count,
			tokens: t,
		}
		if t > 0 {
			us.Tokens = formatTokens(t)
		}
		result = append(result, us)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].tokens != result[j].tokens {
			return result[i].tokens > result[j].tokens
		}
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func applyCostToSummary(result *SummaryResult, costs []claude.SessionCost) {
	cb := &claude.ContextBreakdown{Categories: make(map[claude.ContentCategory]int)}

	for _, c := range costs {
		result.Cost.InputTokens += c.Tokens.InputTokens
		result.Cost.OutputTokens += c.Tokens.OutputTokens
		result.Cost.CacheReadTokens += c.Tokens.CacheReadTokens
		result.Cost.CacheWriteTokens += c.Tokens.CacheWriteTokens
		result.Cost.TotalCost += c.Tokens.TotalCost

		if c.Context != nil {
			for cat, tokens := range c.Context.Categories {
				cb.Categories[cat] += tokens
				cb.Total += tokens
			}
		}
	}

	result.Cost.CostDisplay = fmt.Sprintf("$%.4f", result.Cost.TotalCost)

	totalInput := result.Cost.InputTokens + result.Cost.CacheReadTokens + result.Cost.CacheWriteTokens
	if totalInput > 0 {
		result.Cost.CacheHitRatio = float64(result.Cost.CacheReadTokens) / float64(totalInput) * 100
	}

	if cb.Total > 0 {
		result.ContextBreakdown = cb
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
