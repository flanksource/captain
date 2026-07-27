// ABOUTME: Shared scaffolding for the wire-level e2e specs that drive real agent binaries.
// ABOUTME: Every helper here is binary-agnostic; the per-binary gating lives in requireBinary.

package aimock_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/aimock"
	"github.com/flanksource/captain/pkg/aimock/anthropicmock"
	"github.com/flanksource/captain/pkg/aimock/openaimock"
	"github.com/flanksource/captain/pkg/api"
)

// requireBinary skips rather than fails when an agent CLI is absent, so the
// suite stays green on a machine (or CI runner) without it. The skip is the
// documented gating for these specs — `go test -run E2E ./pkg/aimock/...` on a
// developer machine is where they are expected to actually run.
func requireBinary(t *testing.T, name string) string {
	t.Helper()
	bin, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed; skipping wire-level e2e", name)
	}
	return bin
}

func startAnthropic(t *testing.T, scenario string) *anthropicmock.Server {
	t.Helper()
	srv, err := anthropicmock.Start(anthropicmock.Options{Scenario: loadScenario(t, scenario)})
	require.NoError(t, err)
	t.Cleanup(srv.Close)
	return srv
}

func startOpenAI(t *testing.T, scenario string) *openaimock.Server {
	t.Helper()
	srv, err := openaimock.Start(openaimock.Options{Scenario: loadScenario(t, scenario)})
	require.NoError(t, err)
	t.Cleanup(srv.Close)
	return srv
}

// exportEnv applies a mock's Env() to the test process. The claude-agent
// provider inherits os.Environ() rather than reading Setup.Env, so it is the one
// surface that needs this; the CLI providers take the same values per-request.
func exportEnv(t *testing.T, env []string) {
	t.Helper()
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		require.True(t, ok, "malformed env entry %q", item)
		t.Setenv(key, value)
	}
}

// servedPromptContaining asserts the mock saw a generation on path whose prompt
// carries want, and reports the whole served text. An agent CLI wraps the user's
// question in system reminders and project context, so the served prompt is a
// superset of what the caller asked for — never an exact match.
func servedPromptContaining(t *testing.T, served []aimock.Recorded, path, want string) string {
	t.Helper()
	prompt := servedPrompt(t, served, path)
	require.Contains(t, prompt, want, "the mock must have seen the caller's question")
	return prompt
}

// report drains a stream into everything it said, without failing on an error
// event — the miss specs assert on the failure itself, and each CLI surfaces a
// rejected request differently (assistant text for claude, an error item for
// codex).
func report(events <-chan api.Event) string {
	var sb strings.Builder
	for ev := range events {
		switch ev.Kind {
		case api.EventText, api.EventThinking:
			sb.WriteString(ev.Text)
		case api.EventError:
			sb.WriteString(ev.Error)
		}
	}
	return sb.String()
}
