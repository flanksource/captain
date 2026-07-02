package cli

import "testing"

func TestParseAgentProcessLine(t *testing.T) {
	line := "12345  2.5  1.2 S+   Mon Jun  1 10:00:00 2026 /usr/local/bin/codex --model gpt-5"
	proc, ok := parseAgentProcessLine(line)
	if !ok {
		t.Fatal("expected codex process")
	}
	if proc.Source != "codex" || proc.PID != 12345 || proc.Status != "sleeping" || !proc.Active {
		t.Fatalf("process = %+v", proc)
	}
	if proc.CPUPercent != 2.5 || proc.MemoryPercent != 1.2 {
		t.Fatalf("usage = %v/%v", proc.CPUPercent, proc.MemoryPercent)
	}
	if proc.StartedAt == nil {
		t.Fatal("missing start time")
	}
}

func TestParseAgentProcessLineIgnoresCaptain(t *testing.T) {
	line := "12345  0.0  0.1 S+   Mon Jun  1 10:00:00 2026 captain sessions live"
	if _, ok := parseAgentProcessLine(line); ok {
		t.Fatal("captain process should not be reported as an agent process")
	}
}

func TestParseLsofCWDs(t *testing.T) {
	out := []byte("p123\nn/Users/moshe/project-a\np456\nn/Users/moshe/project-b\n")
	cwds := parseLsofCWDs(out)
	if cwds[123] != "/Users/moshe/project-a" {
		t.Fatalf("pid 123 cwd = %q", cwds[123])
	}
	if cwds[456] != "/Users/moshe/project-b" {
		t.Fatalf("pid 456 cwd = %q", cwds[456])
	}
}
