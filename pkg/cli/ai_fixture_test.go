package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai/fixture"
)

const fakeClaudeScript = `#!/bin/sh
set -eu
model=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--model" ]; then
    model="$arg"
  fi
  prev="$arg"
done

if [ "$model" = "direct-model" ]; then
  cat <<'EOF'
{"type":"assistant","session_id":"sess-direct","message":{"role":"assistant","content":[{"type":"tool_use","id":"1","name":"Bash"},{"type":"tool_use","id":"2","name":"Bash"}],"usage":{"input_tokens":1000,"output_tokens":150}}}
{"type":"result","subtype":"success","session_id":"sess-direct","cost_usd":0.08,"duration_ms":4000}
EOF
  exit 0
fi

cat <<'EOF'
{"type":"assistant","session_id":"sess-mcp","message":{"role":"assistant","content":[{"type":"tool_use","id":"1","name":"mcp__mc__query"}],"usage":{"input_tokens":500,"output_tokens":120}}}
{"type":"result","subtype":"success","session_id":"sess-mcp","cost_usd":0.01,"duration_ms":1000}
EOF
`

func TestRunAIFixture_WritesReportAndArtifacts(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "claude"), []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureYAML := `name: mc-bench
description: MC vs direct bash
prompt: Which cluster is unhealthy?
baseline: direct
defaults:
  timeout: 5s
runs:
  - name: direct
    model: direct-model
  - name: mcp
    model: mcp-model
`
	fixturePath := filepath.Join(tmp, "fixture.yaml")
	if err := os.WriteFile(fixturePath, []byte(fixtureYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	reportPath := filepath.Join(tmp, "out", "report.md")
	raw, err := RunAIFixture(AIFixtureOptions{File: fixturePath, Report: reportPath})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := raw.(fixture.Result)
	if !ok {
		t.Fatalf("result type %T", raw)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(result.Rows))
	}
	if result.Rows[1].Speedup != "4.00x" || result.Rows[1].Cheaper != "8.00x" {
		t.Errorf("mcp ratios: %s / %s", result.Rows[1].Speedup, result.Rows[1].Cheaper)
	}

	md, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "## Headline") || !strings.Contains(string(md), "4.00x faster") {
		t.Errorf("report missing headline/speedup:\n%s", md)
	}

	artifacts, err := filepath.Glob(filepath.Join(tmp, ".captain", "fixtures", "mc-bench", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 {
		t.Errorf("got %d artifacts, want 2: %v", len(artifacts), artifacts)
	}
}

func TestRunAIFixture_RepeatOverride(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "claude"), []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(tmp, "fixture.yaml")
	if err := os.WriteFile(fixturePath, []byte(`prompt: hi
runs:
  - name: a
    model: direct-model
    timeout: 5s
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	raw, err := RunAIFixture(AIFixtureOptions{File: fixturePath, Repeat: 3})
	if err != nil {
		t.Fatal(err)
	}
	result := raw.(fixture.Result)
	if result.Rows[0].Repeat != 3 {
		t.Errorf("Repeat = %d, want 3 (override)", result.Rows[0].Repeat)
	}
}
