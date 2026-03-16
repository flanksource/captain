package dod

import (
	"strings"
	"testing"
)

func TestRunCommandsPass(t *testing.T) {
	dod := &DodFile{
		Commands: []string{"echo hello", "echo world"},
		Workdir:  t.TempDir(),
		Timeout:  10,
	}

	run := RunCommands(dod)
	if len(run.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(run.Results))
	}
	for i, r := range run.Results {
		if !r.Passed {
			t.Errorf("result[%d] %q failed: exit=%d stderr=%q", i, r.Command, r.ExitCode, r.Stderr)
		}
	}
}

func TestRunCommandsFailFast(t *testing.T) {
	dod := &DodFile{
		Commands: []string{"false", "echo should-not-run"},
		Workdir:  t.TempDir(),
		Timeout:  10,
	}

	run := RunCommands(dod)
	if len(run.Results) != 1 {
		t.Fatalf("expected 1 result (fail-fast), got %d", len(run.Results))
	}
	if run.Results[0].Passed {
		t.Error("expected first command to fail")
	}
}

func TestRunCommandsCapturesOutput(t *testing.T) {
	dod := &DodFile{
		Commands: []string{"echo out-text && echo err-text >&2 && exit 1"},
		Workdir:  t.TempDir(),
		Timeout:  10,
	}

	run := RunCommands(dod)
	if len(run.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(run.Results))
	}
	r := run.Results[0]
	if !strings.Contains(r.Stdout, "out-text") {
		t.Errorf("stdout = %q, want containing 'out-text'", r.Stdout)
	}
	if !strings.Contains(r.Stderr, "err-text") {
		t.Errorf("stderr = %q, want containing 'err-text'", r.Stderr)
	}
}

func TestRunCommandsTimeout(t *testing.T) {
	dod := &DodFile{
		Commands: []string{"sleep 60"},
		Workdir:  t.TempDir(),
		Timeout:  1,
	}

	run := RunCommands(dod)
	if len(run.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(run.Results))
	}
	if run.Results[0].Passed {
		t.Error("expected timeout to cause failure")
	}
}

func TestTruncateOutput(t *testing.T) {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = "line"
	}
	input := strings.Join(lines, "\n")
	result := truncateOutput(input)
	if !strings.Contains(result, "truncated") {
		t.Error("expected truncation notice")
	}

	short := "just a few lines"
	if truncateOutput(short) != short {
		t.Error("short output should not be truncated")
	}
}

func TestFormatFailureMessage(t *testing.T) {
	run := &LastRun{
		Results: []CommandResult{
			{Command: "make test", ExitCode: 0, Passed: true},
			{Command: "make lint", ExitCode: 1, Passed: false, Stderr: "lint error"},
		},
	}
	msg := FormatFailureMessage(run)
	if !strings.Contains(msg, "PASS: make test") {
		t.Error("missing pass line")
	}
	if !strings.Contains(msg, "FAIL: make lint") {
		t.Error("missing fail line")
	}
	if !strings.Contains(msg, "lint error") {
		t.Error("missing stderr content")
	}
}
