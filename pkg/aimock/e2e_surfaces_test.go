// ABOUTME: One scenario, three perspectives — API backend, agent loop, and spawned CLI.
// ABOUTME: The deliverable: every surface captain exposes reaches the same mock and agrees.

package aimock_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/provider"
	"github.com/flanksource/captain/pkg/aimock/anthropicmock"
	"github.com/flanksource/captain/pkg/api"
)

// surface is one way to reach a model. Each gets its own mock, because the
// scenario's rules are consumed as they fire — sharing one server would make the
// specs order-dependent.
type surface struct {
	name string
	// binary is the executable this surface spawns, or "" when it runs in-process.
	binary string
	run    func(t *testing.T, srv *anthropicmock.Server) string
}

// The three perspectives the mocks exist to unlock. They differ in how the
// endpoint reaches the model — Config.APIURL for the in-process SDK, Setup.Env
// for a spawned CLI — which is exactly the wiring worth pinning down.
var surfaces = []surface{
	{
		name: "api",
		run: func(t *testing.T, srv *anthropicmock.Server) string {
			model := api.Model{Name: "claude-sonnet-4-5", Backend: api.BackendAnthropic}
			events, err := mockedProvider(t, model, srv.APIURL()).
				ExecuteStream(context.Background(), api.Spec{Prompt: api.Prompt{User: capitalPrompt}})
			require.NoError(t, err)
			return report(events)
		},
	},
	{
		name: "agent",
		run: func(t *testing.T, srv *anthropicmock.Server) string {
			model := api.Model{Name: "claude-sonnet-4-5", Backend: api.BackendAnthropic}
			runner := agent.Runner[string]{
				Provider: mockedProvider(t, model, srv.APIURL()),
				Request:  api.Spec{Prompt: api.Prompt{User: capitalPrompt}},
			}
			result, err := runner.Run(context.Background())
			require.NoError(t, err)
			require.NotNil(t, result.Response)
			return result.Response.Text
		},
	},
	{
		name:   "cli",
		binary: "claude",
		run: func(t *testing.T, srv *anthropicmock.Server) string {
			events, err := provider.NewClaudeCLI("sonnet").
				ExecuteStream(context.Background(), hermetic(capitalPrompt, srv.Env()))
			require.NoError(t, err)
			return report(events)
		},
	},
}

// TestE2ESurfacesAgree runs one scenario through every surface. A surface that
// answered from anywhere but the mock would either miss the scripted text or
// leave the scenario unconsumed, so the two assertions together prove the run
// was served locally and spent nothing.
func TestE2ESurfacesAgree(t *testing.T) {
	for _, s := range surfaces {
		t.Run(s.name, func(t *testing.T) {
			if s.binary != "" {
				requireBinary(t, s.binary)
			}
			srv := startAnthropic(t, "text-only.yaml")

			assert.Contains(t, s.run(t, srv), capitalAnswer)
			servedPromptContaining(t, srv.Requests, "/v1/messages", capitalPrompt)
			assert.Empty(t, srv.Remaining(), "the scenario must be played out")
		})
	}
}
