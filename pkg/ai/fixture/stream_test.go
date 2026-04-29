package fixture

import "testing"

func TestParseStream_CountsToolsAndUsage(t *testing.T) {
	data := []byte(`{"type":"assistant","session_id":"s1","message":{"role":"assistant","content":[{"type":"tool_use","id":"1","name":"Bash"},{"type":"tool_use","id":"2","name":"mcp__mc__query"}],"usage":{"input_tokens":500,"output_tokens":100,"cache_read_input_tokens":50,"cache_creation_input_tokens":10}}}
{"type":"result","subtype":"success","session_id":"s1","cost_usd":0.03,"duration_ms":1200}`)

	s := ParseStream(data)
	if s.SessionID != "s1" {
		t.Errorf("SessionID = %q, want s1", s.SessionID)
	}
	if !s.Success {
		t.Errorf("Success = false, want true")
	}
	if s.BashCalls != 1 {
		t.Errorf("BashCalls = %d, want 1", s.BashCalls)
	}
	if s.MCPCalls != 1 {
		t.Errorf("MCPCalls = %d, want 1", s.MCPCalls)
	}
	if s.ToolCalls != 2 {
		t.Errorf("ToolCalls = %d, want 2", s.ToolCalls)
	}
	if s.Input != 500 || s.Output != 100 || s.CacheRead != 50 || s.CacheWrite != 10 {
		t.Errorf("usage mismatch: %+v", s)
	}
	if s.CostUSD != 0.03 || s.DurationMS != 1200 {
		t.Errorf("cost/duration mismatch: cost=%v dur=%v", s.CostUSD, s.DurationMS)
	}
}

func TestParseStream_ErrorResult(t *testing.T) {
	data := []byte(`{"type":"result","subtype":"error","is_error":true,"error":"boom","duration_ms":500}`)
	s := ParseStream(data)
	if s.Success {
		t.Error("Success = true, want false")
	}
	if s.Error != "boom" {
		t.Errorf("Error = %q, want boom", s.Error)
	}
}

func TestParseStream_IgnoresMalformedLines(t *testing.T) {
	data := []byte(`not json
{"type":"result","subtype":"success","cost_usd":0.01,"duration_ms":100}`)
	s := ParseStream(data)
	if !s.Success {
		t.Error("expected success despite bad line")
	}
}
