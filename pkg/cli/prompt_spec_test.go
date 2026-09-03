package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// TestPromptSpecRuntimeTimeout pins the parse of a declared budget.timeout.
// runtimeTimeout used to discard time.ParseDuration's error and hand back 120s,
// so `budget.timeout: "2 minutes"` — a plausible typo — ran to a two-minute
// deadline the author never asked for and never learned about. A declared
// ceiling that quietly does nothing reads as enforced.
func TestPromptSpecRuntimeTimeout(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr string
	}{
		{name: "a valid duration parses", raw: "90s", want: 90 * time.Second},
		{name: "hours parse", raw: "2h", want: 2 * time.Hour},
		{
			name:    "a humanized duration is a loud error",
			raw:     "2 minutes",
			wantErr: `invalid budget timeout "2 minutes"`,
		},
		{
			name:    "a non-positive duration is a loud error",
			raw:     "0s",
			wantErr: `invalid budget timeout "0s" (must be > 0)`,
		},
		{
			name:    "a negative duration is a loud error",
			raw:     "-5s",
			wantErr: `invalid budget timeout "-5s" (must be > 0)`,
		},
		{name: "an absent timeout declares no bound", raw: "", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runtimeTimeout(tt.raw)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("runtimeTimeout(%q) = %s, want error %q", tt.raw, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("runtimeTimeout(%q) error = %q, want it to name the field and the raw value (%q)", tt.raw, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("runtimeTimeout(%q) err = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("runtimeTimeout(%q) = %s, want %s", tt.raw, got, tt.want)
			}
		})
	}
}

// TestPromptSpecRenderedTimeoutDefault pins where the CLI's own 120s lives: at
// the call site that needs a deadline, not inside the parser. A spec that
// declares none gets it; a spec that declares one gets exactly that.
func TestPromptSpecRenderedTimeoutDefault(t *testing.T) {
	absent, err := renderedTimeout(PromptRenderResult{})
	if err != nil {
		t.Fatalf("renderedTimeout(absent) err = %v", err)
	}
	if absent != defaultRunTimeout {
		t.Fatalf("renderedTimeout(absent) = %s, want the CLI default %s", absent, defaultRunTimeout)
	}

	declared, err := renderedTimeout(PromptRenderResult{
		Input: ai.Request{Budget: api.Budget{Timeout: "90s"}},
	})
	if err != nil {
		t.Fatalf("renderedTimeout(declared) err = %v", err)
	}
	if declared != 90*time.Second {
		t.Fatalf("renderedTimeout(declared) = %s, want 90s", declared)
	}

	if _, err := renderedTimeout(PromptRenderResult{
		Input: ai.Request{Budget: api.Budget{Timeout: "2 minutes"}},
	}); err == nil {
		t.Fatal("renderedTimeout accepted an unparseable budget.timeout; the run would silently use a default deadline")
	}
}
