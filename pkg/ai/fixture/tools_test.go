package fixture

import (
	"testing"
	"time"
)

func TestEstimateTokens(t *testing.T) {
	cases := map[string]int{
		"":      0,
		"a":     1,
		"abcd":  1,
		"abcde": 2,
		"abcdefghij": 3, // 10 chars / 4 → ceil = 3
	}
	for in, want := range cases {
		if got := estimateTokens(in); got != want {
			t.Errorf("estimateTokens(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCorrelateKubectlNetworkRequests(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := []ToolCallEntry{
		{ToolName: "Bash", IsKubectl: true, Time: t0, EndTime: t0.Add(2 * time.Second)},
		{ToolName: "Bash", IsKubectl: false, Time: t0.Add(3 * time.Second), EndTime: t0.Add(4 * time.Second)},
		{ToolName: "Bash", IsKubectl: true, Time: t0.Add(5 * time.Second), EndTime: t0.Add(6 * time.Second)},
	}
	api := []KubectlAPIEntry{
		{Time: t0.Add(500 * time.Millisecond)},                                 // → call 0
		{Time: t0.Add(1500 * time.Millisecond)},                                // → call 0
		{Time: t0.Add(3500 * time.Millisecond)},                                // skipped (non-kubectl Bash)
		{Time: t0.Add(5500 * time.Millisecond)},                                // → call 2
		{Time: t0.Add(10 * time.Second)},                                       // outside any window
	}
	correlateKubectlNetworkRequests(calls, api)
	if calls[0].NetworkRequests != 2 {
		t.Errorf("call[0].NetworkRequests = %d, want 2", calls[0].NetworkRequests)
	}
	if calls[1].NetworkRequests != 0 {
		t.Errorf("call[1].NetworkRequests = %d, want 0 (non-kubectl)", calls[1].NetworkRequests)
	}
	if calls[2].NetworkRequests != 1 {
		t.Errorf("call[2].NetworkRequests = %d, want 1", calls[2].NetworkRequests)
	}
}

func TestCorrelateMCPNetworkRequests(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := []ToolCallEntry{
		{ToolName: "mcp__mc__query", Time: t0, EndTime: t0.Add(2 * time.Second)},
		{ToolName: "Read", Time: t0.Add(3 * time.Second), EndTime: t0.Add(4 * time.Second)},
		{ToolName: "mcp__mc__list", Time: t0.Add(5 * time.Second), EndTime: t0.Add(6 * time.Second)},
	}
	api := []MCPAPIEntry{
		{Time: t0.Add(500 * time.Millisecond)},
		{Time: t0.Add(5500 * time.Millisecond)},
		{Time: t0.Add(5800 * time.Millisecond)},
	}
	correlateMCPNetworkRequests(calls, api)
	if calls[0].NetworkRequests != 1 || !calls[0].IsMCPProxy {
		t.Errorf("mcp call[0] = {%d, %v}, want {1, true}", calls[0].NetworkRequests, calls[0].IsMCPProxy)
	}
	if calls[1].IsMCPProxy {
		t.Error("non-mcp call[1] should not be marked IsMCPProxy")
	}
	if calls[2].NetworkRequests != 2 {
		t.Errorf("mcp call[2].NetworkRequests = %d, want 2", calls[2].NetworkRequests)
	}
}

func TestFlushPendingCalls(t *testing.T) {
	inflight := map[string]*pendingCall{
		"id-1": {Time: time.Now().UTC(), ToolName: "Bash", Command: "ls", InputTokens: 5},
	}
	summary := &Summary{}
	flushPendingCalls(inflight, summary)
	if len(summary.ToolCallLog) != 1 {
		t.Fatalf("ToolCallLog = %v, want 1 entry", summary.ToolCallLog)
	}
	got := summary.ToolCallLog[0]
	if got.Duration != "-" {
		t.Errorf("Duration = %q, want %q", got.Duration, "-")
	}
	if got.InputTokens != 5 {
		t.Errorf("InputTokens = %d, want 5", got.InputTokens)
	}
	if len(inflight) != 0 {
		t.Errorf("inflight should be cleared, still has %d", len(inflight))
	}
}

func TestToolCallEntry_Label(t *testing.T) {
	bash := ToolCallEntry{ToolName: "Bash", Command: "kubectl get pods"}
	if bash.Label() != "kubectl get pods" {
		t.Errorf("Bash label = %q, want command", bash.Label())
	}
	mcp := ToolCallEntry{ToolName: "mcp__mc__query"}
	if mcp.Label() != "mcp__mc__query" {
		t.Errorf("mcp label = %q, want tool name", mcp.Label())
	}
}

func TestMCPAPIEntry_Operation(t *testing.T) {
	cases := []struct {
		entry MCPAPIEntry
		want  string
	}{
		{MCPAPIEntry{}, ""},
		{MCPAPIEntry{RPCMethod: "initialize"}, "initialize"},
		{MCPAPIEntry{RPCMethod: "tools/list"}, "tools/list"},
		{MCPAPIEntry{RPCMethod: "tools/call", Tool: "mc__query"}, "tools/call: mc__query"},
		{MCPAPIEntry{RPCMethod: "tools/call"}, "tools/call"}, // no tool name
	}
	for _, c := range cases {
		if got := c.entry.Operation(); got != c.want {
			t.Errorf("Operation(%+v) = %q, want %q", c.entry, got, c.want)
		}
	}
}
