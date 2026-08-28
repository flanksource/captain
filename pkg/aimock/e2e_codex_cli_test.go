// ABOUTME: Drives the real `codex` binary against the OpenAI mock.
// ABOUTME: The spec that proves the model_providers override actually redirects codex.

package aimock_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/ai/provider"
	"github.com/flanksource/captain/pkg/api"
)

// codexProvider builds the provider for a mock. APIURL rather than Env() is
// load-bearing: with a ChatGPT-account credential stored, codex routes to the
// ChatGPT backend and ignores OPENAI_BASE_URL entirely, so the redirection has
// to travel as the model_providers override captain renders from APIURL. Env()
// still goes on the request — the override names OPENAI_API_KEY as its env_key.
func codexProvider(apiURL string) *provider.CodexCLI {
	return provider.NewCodexCLI(api.Config{
		Model:  api.Model{Name: "gpt-5", Backend: api.BackendCodexCLI},
		APIURL: apiURL,
	})
}

func TestE2ECodexCLI(t *testing.T) {
	requireBinary(t, "codex")
	srv := startOpenAI(t, "text-only.yaml")

	events, err := codexProvider(srv.APIURL()).
		ExecuteStream(context.Background(), hermetic(capitalPrompt, srv.Env()))
	require.NoError(t, err)

	assert.Contains(t, report(events), capitalAnswer)

	// Codex and Captain's direct OpenAI backend both speak the Responses API;
	// this spec exercises the external Codex binary path.
	servedPromptContaining(t, srv.Requests, "/v1/responses", capitalPrompt)
	assert.Empty(t, srv.Remaining(), "the scenario must be played out")
}

// The counterpart to TestE2EClaudeCLIReportsAScenarioMiss: codex retries 5xx
// five times, so a miss must stay a 4xx to surface on the first attempt.
func TestE2ECodexCLIReportsAScenarioMiss(t *testing.T) {
	requireBinary(t, "codex")
	srv := startOpenAI(t, "text-only.yaml")

	events, err := codexProvider(srv.APIURL()).
		ExecuteStream(context.Background(), hermetic("Name a river in Peru.", srv.Env()))
	require.NoError(t, err)

	assert.Contains(t, report(events), "no scenario rule matched")
	assert.NotEmpty(t, srv.Remaining(), "an unmatched request must leave its rule unconsumed")
}
