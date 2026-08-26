// ABOUTME: Drives the real `claude` binary against the Anthropic mock.
// ABOUTME: Proves the argv captain builds is accepted and the stream-json it parses is genuine.

package aimock_test

import (
	"context"
	"testing"

	"github.com/flanksource/commons-db/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/ai/provider"
	"github.com/flanksource/captain/pkg/api"
)

// hermetic is the request shape every CLI e2e uses: Bare drops hooks, skills,
// memory and ambient settings, and disabling MCP stops the CLI dialling the
// user's configured servers. Both keep the run reproducible on any machine —
// without them the spec's timing and its served prompt depend on whatever the
// developer happens to have configured.
func hermetic(prompt string, env []string) api.Spec {
	spec := api.Spec{
		Prompt: api.Prompt{User: prompt},
		Memory: api.Memory{Bare: true},
		Setup:  &shell.Setup{Env: env},
	}
	spec.Permissions.MCP.Disabled = true
	return spec
}

// TestE2EClaudeCLI is the wire-level counterpart to the buildClaudeCLIArgs unit
// tests: those assert the argv shape, this asserts a real claude accepts it and
// that captain's parser reads a stream the binary actually produced.
func TestE2EClaudeCLI(t *testing.T) {
	requireBinary(t, "claude")
	srv := startAnthropic(t, "text-only.yaml")

	events, err := provider.NewClaudeCLI("sonnet").
		ExecuteStream(context.Background(), hermetic(capitalPrompt, srv.Env()))
	require.NoError(t, err)

	text, result := drain(t, events)
	assert.Equal(t, capitalAnswer, text)
	require.NotNil(t, result, "the run must end with a result event")
	require.NotNil(t, result.Usage)
	assert.Equal(t, 14, result.Usage.InputTokens)
	assert.Equal(t, 8, result.Usage.OutputTokens)
	assert.NotEmpty(t, result.SessionID, "a real claude run always reports a session id")

	servedPromptContaining(t, srv.Requests, "/v1/messages", capitalPrompt)
	assert.Empty(t, srv.Remaining(), "the scenario must be played out")
}

// A scenario gap has to surface as an immediate, readable failure. This is the
// spec that would catch a regression from aimock.MissStatus back to a 5xx:
// claude retries 5xx ten times over roughly a minute, so the run would hang
// rather than report.
func TestE2EClaudeCLIReportsAScenarioMiss(t *testing.T) {
	requireBinary(t, "claude")
	srv := startAnthropic(t, "text-only.yaml")

	events, err := provider.NewClaudeCLI("sonnet").
		ExecuteStream(context.Background(), hermetic("Name a river in Peru.", srv.Env()))
	require.NoError(t, err)

	assert.Contains(t, report(events), "no scenario rule matched")
	assert.NotEmpty(t, srv.Remaining(), "an unmatched request must leave its rule unconsumed")
}
