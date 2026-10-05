package history

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/flanksource/captain/pkg/api"
)

// CodexContext matches TokenUsage::percent_of_context_window_remaining in
// codex-rs/protocol/src/protocol.rs, including its 12,000-token baseline.
func CodexContext(usedTokens, windowTokens *int) *api.ContextUsage {
	if usedTokens == nil || windowTokens == nil {
		return nil
	}
	if *usedTokens < 0 || *windowTokens <= 0 {
		panic(fmt.Sprintf("invalid Codex context: used=%d window=%d", *usedTokens, *windowTokens))
	}
	free := 0
	const baseline = 12000
	if *windowTokens > baseline {
		effective := *windowTokens - baseline
		used := max(*usedTokens-baseline, 0)
		free = int(math.Round(float64(max(effective-used, 0)) / float64(effective) * 100))
	}
	return &api.ContextUsage{UsedTokens: *usedTokens, WindowTokens: *windowTokens, FreePercent: free}
}

func (info *CodexTokenInfo) UnmarshalJSON(data []byte) error {
	type tokenInfo CodexTokenInfo
	var reported struct {
		Last *struct {
			TotalTokens *int `json:"total_tokens"`
		} `json:"last_token_usage"`
		Window *int `json:"model_context_window"`
	}
	// Decode accounting independently: the optional native fields distinguish
	// an explicit zero occupancy from missing context telemetry.
	if err := json.Unmarshal(data, (*tokenInfo)(info)); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &reported); err != nil {
		return err
	}
	info.Context = nil
	if reported.Last != nil {
		info.Context = CodexContext(reported.Last.TotalTokens, reported.Window)
	}
	return nil
}
