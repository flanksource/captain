// ABOUTME: Cost resolution and money/byte formatters.
// ABOUTME: Falls back to OpenRouter pricing when the claude CLI doesn't report a cost.

package fixture

import (
	"fmt"

	"github.com/flanksource/captain/pkg/ai/pricing"
)

// resolveCost returns the per-iteration cost for a row. If the underlying
// claude CLI didn't report a cost (CostMean == 0) we fall back to the
// OpenRouter-backed pricing registry to estimate from the token counts.
// Returns (cost, estimated) — estimated is true when the value is from the
// fallback path.
func resolveCost(model string, a *aggregate) (float64, bool) {
	if a.CostMean > 0 {
		return a.CostMean, false
	}
	if a.Input == 0 && a.Output == 0 && a.CacheRead == 0 && a.CacheWrite == 0 {
		return 0, false
	}
	res, err := pricing.CalculateCost(model, a.Input, a.Output, 0, a.CacheRead, a.CacheWrite)
	if err != nil {
		return 0, false
	}
	return res.TotalCost, true
}

func formatCostWithEstimate(v float64, estimated bool) string {
	s := formatUSD(v)
	if estimated && v > 0 {
		return s + " (est)"
	}
	return s
}

func formatUSD(v float64) string {
	if v == 0 {
		return "$0"
	}
	return fmt.Sprintf("$%.4f", v)
}

// humanBytes renders a byte count with KB/MB/GB/... suffixes.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	suffix := "KMGTPE"[exp]
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), suffix)
}
