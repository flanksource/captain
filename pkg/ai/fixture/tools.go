// ABOUTME: Per-call tool tracking — pendingCall buffers tool_use events until matching tool_results arrive.
// ABOUTME: Also defines the ToolCallEntry, KubectlAPIEntry, MCPAPIEntry structs and proxy-log correlation.

package fixture

import (
	"fmt"
	"strings"
	"time"
)

// ToolCallEntry is one tool_use/tool_result pair captured from the stream:
// when the model issued the call, what it issued, how long it took, what came
// back, and how many tokens the parent assistant turn cost. NetworkRequests is
// populated only for kubectl Bash invocations or MCP tool calls once the run
// completes and the proxy log has been correlated by timestamp window.
type ToolCallEntry struct {
	Time            time.Time `json:"time"`
	EndTime         time.Time `json:"endTime"`
	ToolName        string    `json:"toolName"`
	Command         string    `json:"command,omitempty"`
	Duration        string    `json:"duration"`
	DurationMS      float64   `json:"durationMs"`
	InputTokens     int       `json:"inputTokens"`
	OutputTokens    int       `json:"outputTokens"`
	Output          string    `json:"output,omitempty"`
	NetworkRequests int       `json:"networkRequests,omitempty"`
	IsKubectl       bool      `json:"isKubectl,omitempty"`
	IsMCPProxy      bool      `json:"isMcpProxy,omitempty"`
}

// Label returns what to show in the unified tool-call table's command column:
// the literal bash command for Bash, the tool name for everything else.
func (e ToolCallEntry) Label() string {
	if e.ToolName == "Bash" {
		return e.Command
	}
	return e.ToolName
}

// KubectlAPIEntry is one HTTP request observed flowing through the proxy.
type KubectlAPIEntry struct {
	Time     time.Time `json:"time"`
	Method   string    `json:"method"`
	URL      string    `json:"url"`
	Status   int       `json:"status"`
	Duration string    `json:"duration"`
	Bytes    int64     `json:"bytes"`
}

func (e KubectlAPIEntry) Format() string {
	return fmt.Sprintf("%s  %s %s  %d  %s  %s",
		e.Time.Local().Format("15:04:05.000"), e.Method, e.URL, e.Status, e.Duration, humanBytes(e.Bytes))
}

// MCPAPIEntry is one HTTP request observed flowing through an MCP HTTP proxy.
// Server is the MCP server name from the mcpConfig that fielded the request.
// RPCMethod and Tool come from peeking the JSON-RPC request body before
// forwarding (e.g. "tools/call" + "mission-control__query").
type MCPAPIEntry struct {
	Time      time.Time `json:"time"`
	Server    string    `json:"server,omitempty"`
	Method    string    `json:"method"`
	URL       string    `json:"url"`
	RPCMethod string    `json:"rpcMethod,omitempty"`
	Tool      string    `json:"tool,omitempty"`
	Status    int       `json:"status"`
	Duration  string    `json:"duration"`
	Bytes     int64     `json:"bytes"`
}

// Operation returns a one-line human label of what this request was doing —
// either the JSON-RPC method, or "tools/call: <tool>" when calling a tool.
func (e MCPAPIEntry) Operation() string {
	if e.RPCMethod == "" {
		return ""
	}
	if e.RPCMethod == "tools/call" && e.Tool != "" {
		return e.RPCMethod + ": " + e.Tool
	}
	return e.RPCMethod
}

func (e MCPAPIEntry) Format() string {
	op := e.Operation()
	if op == "" {
		op = "-"
	}
	return fmt.Sprintf("%s  %s %s %s  [%s]  %d  %s  %s",
		e.Time.Local().Format("15:04:05.000"), e.Server, e.Method, e.URL, op, e.Status, e.Duration, humanBytes(e.Bytes))
}

// pendingCall is a tool_use awaiting its matching tool_result. We snapshot
// timing, the call label, and the size of the input arguments at issuance
// time; the matching tool_result supplies the output payload size. Tokens are
// rough text-length estimates (~4 chars/token), matching the convention used
// by `captain history`.
type pendingCall struct {
	Time        time.Time
	ToolName    string
	Command     string
	InputTokens int
	IsKubectl   bool
}

// estimateTokens approximates token count from text length using the same
// 4-chars-per-token heuristic as pkg/claude/cost.EstimateTokens.
func estimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len(s) + 3) / 4
}

// trackToolCalls walks an event's content blocks: assistant tool_use opens an
// inflight call; user tool_result closes one and appends a finalized
// ToolCallEntry to the summary.
func trackToolCalls(ev Event, inflight map[string]*pendingCall, summary *Summary) {
	switch ev.Type {
	case "assistant":
		for _, c := range ev.Content {
			if c.Type != "tool_use" || c.ID == "" {
				continue
			}
			cmd := ""
			isKube := false
			if c.Name == "Bash" {
				cmd = strings.TrimSpace(bashCommandFrom(c.Input))
				isKube = isKubectlCommand(cmd)
			}
			inflight[c.ID] = &pendingCall{
				Time:        time.Now().UTC(),
				ToolName:    c.Name,
				Command:     cmd,
				InputTokens: estimateTokens(string(c.Input)),
				IsKubectl:   isKube,
			}
		}
	case "user":
		for _, c := range ev.Content {
			if c.Type != "tool_result" || c.ToolUseID == "" {
				continue
			}
			pc, ok := inflight[c.ToolUseID]
			if !ok {
				continue
			}
			delete(inflight, c.ToolUseID)
			end := time.Now().UTC()
			dur := end.Sub(pc.Time)
			output := ToolResultText(c.Content)
			summary.ToolCallLog = append(summary.ToolCallLog, ToolCallEntry{
				Time:         pc.Time,
				EndTime:      end,
				ToolName:     pc.ToolName,
				Command:      pc.Command,
				Duration:     dur.Round(time.Millisecond).String(),
				DurationMS:   float64(dur) / float64(time.Millisecond),
				InputTokens:  pc.InputTokens,
				OutputTokens: estimateTokens(output),
				Output:       output,
				IsKubectl:    pc.IsKubectl,
			})
		}
	}
}

// flushPendingCalls finalizes any tool_uses that never got a matching
// tool_result (claude exited mid-call, malformed stream, etc.) so we don't
// silently drop them from the per-run log. Duration and output are unknown.
func flushPendingCalls(inflight map[string]*pendingCall, summary *Summary) {
	for _, pc := range inflight {
		summary.ToolCallLog = append(summary.ToolCallLog, ToolCallEntry{
			Time:        pc.Time,
			EndTime:     pc.Time,
			ToolName:    pc.ToolName,
			Command:     pc.Command,
			Duration:    "-",
			InputTokens: pc.InputTokens,
			IsKubectl:   pc.IsKubectl,
		})
	}
	clear(inflight)
}

// correlateKubectlNetworkRequests counts proxy API requests whose timestamps
// fall inside each kubectl Bash call's tool_use→tool_result window.
func correlateKubectlNetworkRequests(calls []ToolCallEntry, api []KubectlAPIEntry) {
	for i := range calls {
		c := &calls[i]
		if !c.IsKubectl {
			continue
		}
		for _, req := range api {
			if !req.Time.Before(c.Time) && !req.Time.After(c.EndTime) {
				c.NetworkRequests++
			}
		}
	}
}

// correlateMCPNetworkRequests counts MCP proxy requests whose timestamps fall
// inside each MCP tool_use→tool_result window. Marks the call as proxied so
// the report knows to render a number (rather than "-") in the Net column.
func correlateMCPNetworkRequests(calls []ToolCallEntry, api []MCPAPIEntry) {
	for i := range calls {
		c := &calls[i]
		if !strings.HasPrefix(c.ToolName, "mcp__") {
			continue
		}
		c.IsMCPProxy = true
		for _, req := range api {
			if !req.Time.Before(c.Time) && !req.Time.After(c.EndTime) {
				c.NetworkRequests++
			}
		}
	}
}
