package pricing

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type ModelInfo struct {
	ModelID          string
	MaxTokens        int
	ContextWindow    int
	InputPrice       float64 // per million tokens
	OutputPrice      float64 // per million tokens
	CacheReadsPrice  float64 // per million tokens
	CacheWritesPrice float64 // per million tokens
}

var (
	registry   = map[string]ModelInfo{}
	registryMu sync.RWMutex
)

func GetModelInfo(model string) (ModelInfo, bool) {
	EnsureLoaded()
	registryMu.RLock()
	defer registryMu.RUnlock()
	info, ok := registry[model]
	return info, ok
}

func MergeModels(models map[string]*ModelInfo) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for id, info := range models {
		registry[id] = *info
	}
}

func RegistrySize() int {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return len(registry)
}

type CostResult struct {
	Model        string
	InputTokens  int
	OutputTokens int
	TotalCost    float64
}

func CalculateCost(model string, inputTokens, outputTokens, reasoningTokens, cacheReadTokens, cacheWriteTokens int) (CostResult, error) {
	info, ok := GetModelInfo(model)
	if !ok {
		suggestions := findSimilarModels(model, 3)
		return CostResult{}, fmt.Errorf("model %s not found in pricing registry (%d models). Did you mean: %s",
			model, RegistrySize(), strings.Join(suggestions, ", "))
	}

	cost := float64(inputTokens)*info.InputPrice/1_000_000 +
		float64(outputTokens)*info.OutputPrice/1_000_000 +
		float64(reasoningTokens)*info.OutputPrice/1_000_000

	if cacheReadTokens > 0 && info.CacheReadsPrice > 0 {
		cost += float64(cacheReadTokens) * info.CacheReadsPrice / 1_000_000
	}
	if cacheWriteTokens > 0 && info.CacheWritesPrice > 0 {
		cost += float64(cacheWriteTokens) * info.CacheWritesPrice / 1_000_000
	}

	return CostResult{
		Model:        model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalCost:    cost,
	}, nil
}

func findSimilarModels(target string, topN int) []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	if len(registry) == 0 {
		return nil
	}

	type match struct {
		name     string
		distance int
	}

	targetLower := strings.ToLower(target)
	matches := make([]match, 0, len(registry))
	for name := range registry {
		matches = append(matches, match{name: name, distance: levenshtein(targetLower, strings.ToLower(name))})
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].distance < matches[j].distance })

	n := min(topN, len(matches))
	result := make([]string, n)
	for i := range n {
		result[i] = matches[i].name
	}
	return result
}

func levenshtein(s1, s2 string) int {
	if len(s1) == 0 {
		return len(s2)
	}
	if len(s2) == 0 {
		return len(s1)
	}

	matrix := make([][]int, len(s1)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(s2)+1)
		matrix[i][0] = i
	}
	for j := range len(s2) + 1 {
		matrix[0][j] = j
	}

	for i := 1; i <= len(s1); i++ {
		for j := 1; j <= len(s2); j++ {
			cost := 1
			if s1[i-1] == s2[j-1] {
				cost = 0
			}
			matrix[i][j] = min(matrix[i-1][j]+1, min(matrix[i][j-1]+1, matrix[i-1][j-1]+cost))
		}
	}
	return matrix[len(s1)][len(s2)]
}
