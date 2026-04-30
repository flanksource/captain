// ABOUTME: Per-Run statistics accumulator — adds Summary records over N iterations and finalizes means.

package fixture

import "math"

type aggregate struct {
	N             int
	Success       bool
	Error         string
	SessionID     string
	DurationMean  float64
	DurationStdev float64
	CostMean      float64

	Input      int
	Output     int
	CacheRead  int
	CacheWrite int

	ToolCalls       int
	MCPCalls        int
	BashCalls       int
	KubectlCalls    int
	KubectlAPICalls int
	MCPAPICalls     int

	KubectlAPILog []KubectlAPIEntry
	MCPAPILog     []MCPAPIEntry
	ToolCallLog   []ToolCallEntry
	ToolCounts    map[string]int

	durations []float64
	costs     []float64
}

func (a *aggregate) add(s Summary) {
	a.N++
	a.durations = append(a.durations, s.DurationMS)
	a.costs = append(a.costs, s.CostUSD)
	if s.SessionID != "" {
		a.SessionID = s.SessionID
	}
	if s.Error != "" && a.Error == "" {
		a.Error = s.Error
	}
	if s.Success {
		a.Success = true
	}
	a.Input += s.Input
	a.Output += s.Output
	a.CacheRead += s.CacheRead
	a.CacheWrite += s.CacheWrite
	a.ToolCalls += s.ToolCalls
	a.MCPCalls += s.MCPCalls
	a.BashCalls += s.BashCalls
	a.KubectlCalls += s.KubectlCalls
	a.KubectlAPICalls += s.KubectlAPICalls
	a.KubectlAPILog = append(a.KubectlAPILog, s.KubectlAPILog...)
	a.MCPAPICalls += s.MCPAPICalls
	a.MCPAPILog = append(a.MCPAPILog, s.MCPAPILog...)
	a.ToolCallLog = append(a.ToolCallLog, s.ToolCallLog...)
	if a.ToolCounts == nil {
		a.ToolCounts = map[string]int{}
	}
	for k, v := range s.ToolCounts {
		a.ToolCounts[k] += v
	}
}

func (a *aggregate) finalize() {
	if a.N == 0 {
		return
	}
	a.DurationMean = mean(a.durations)
	a.DurationStdev = stdev(a.durations, a.DurationMean)
	a.CostMean = mean(a.costs)
	// Per-iteration averages for token/tool aggregate counts.
	a.Input /= a.N
	a.Output /= a.N
	a.CacheRead /= a.N
	a.CacheWrite /= a.N
	a.ToolCalls /= a.N
	a.MCPCalls /= a.N
	a.BashCalls /= a.N
	a.KubectlCalls /= a.N
	a.KubectlAPICalls /= a.N
	a.MCPAPICalls /= a.N
	for k, v := range a.ToolCounts {
		a.ToolCounts[k] = v / a.N
	}
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func stdev(xs []float64, m float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var s float64
	for _, x := range xs {
		d := x - m
		s += d * d
	}
	return math.Sqrt(s / float64(len(xs)-1))
}
