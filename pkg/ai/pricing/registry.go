package pricing

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/collections"
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

func ListModels(filter string) []ModelInfo {
	EnsureLoaded()
	registryMu.RLock()
	defer registryMu.RUnlock()

	filterLower := strings.ToLower(filter)
	result := make([]ModelInfo, 0)
	for _, info := range registry {
		if filter != "" && !strings.Contains(strings.ToLower(info.ModelID), filterLower) {
			continue
		}
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ModelID < result[j].ModelID })
	return result
}

type CostResult struct {
	Model            string
	InputTokens      int
	OutputTokens     int
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
	InputCost        float64
	OutputCost       float64
	ReasoningCost    float64
	CacheReadCost    float64
	CacheWriteCost   float64
	TotalCost        float64
}

func CalculateCost(model string, inputTokens, outputTokens, reasoningTokens, cacheReadTokens, cacheWriteTokens int) (CostResult, error) {
	info, ok := GetModelInfo(model)
	if !ok {
		suggestions := findSimilarModels(model, 3)
		return CostResult{}, fmt.Errorf("model %s not found in pricing registry (%d models). Did you mean: %s",
			model, RegistrySize(), strings.Join(suggestions, ", "))
	}

	inputCost := float64(inputTokens) * info.InputPrice / 1_000_000
	outputCost := float64(outputTokens) * info.OutputPrice / 1_000_000
	reasoningCost := float64(reasoningTokens) * info.OutputPrice / 1_000_000

	var cacheReadCost float64
	if cacheReadTokens > 0 && info.CacheReadsPrice > 0 {
		cacheReadCost = float64(cacheReadTokens) * info.CacheReadsPrice / 1_000_000
	}
	var cacheWriteCost float64
	if cacheWriteTokens > 0 && info.CacheWritesPrice > 0 {
		cacheWriteCost = float64(cacheWriteTokens) * info.CacheWritesPrice / 1_000_000
	}
	cost := inputCost + outputCost + reasoningCost + cacheReadCost + cacheWriteCost

	return CostResult{
		Model:            model,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		ReasoningTokens:  reasoningTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		InputCost:        inputCost,
		OutputCost:       outputCost,
		ReasoningCost:    reasoningCost,
		CacheReadCost:    cacheReadCost,
		CacheWriteCost:   cacheWriteCost,
		TotalCost:        cost,
	}, nil
}

func findSimilarModels(target string, topN int) []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	candidates := make([]string, 0, len(registry))
	for name := range registry {
		candidates = append(candidates, name)
	}
	return collections.FindSimilar(target, candidates, topN)
}
