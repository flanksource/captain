// ABOUTME: Drives the Claude Agent SDK (TypeScript) bridge against the Anthropic mock.
// ABOUTME: The one surface that reads the endpoint from process env rather than the request.

package aimock_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/ai/provider/claudeagent"
	"github.com/flanksource/captain/pkg/api"
)

// TestE2EClaudeAgent covers the TS SDK bridge. It needs npm to install the
// pinned SDK and a claude binary underneath, so it gates on both.
func TestE2EClaudeAgent(t *testing.T) {
	requireBinary(t, "npm")
	requireBinary(t, "claude")
	srv := startAnthropic(t, "text-only.yaml")

	// The bridge is a supervised tsx process that inherits os.Environ(); unlike
	// the CLI providers it never reads Setup.Env, so the endpoint has to be
	// exported into the test process.
	exportEnv(t, srv.Env())

	agent, err := claudeagent.New(api.Config{Model: api.Model{Name: "sonnet", Backend: api.BackendClaudeAgent}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = agent.Close() })

	events, err := agent.ExecuteStream(context.Background(), api.Spec{Prompt: api.Prompt{User: capitalPrompt}})
	require.NoError(t, err)

	assert.Contains(t, report(events), capitalAnswer)
	servedPromptContaining(t, srv.Requests, "/v1/messages", capitalPrompt)
	assert.Empty(t, srv.Remaining(), "the scenario must be played out")
}
