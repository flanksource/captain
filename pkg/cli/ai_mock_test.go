package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mockScenario = "../aimock/testdata/scenarios/text-only.yaml"

func TestRunAIMockEnvRendersBothProtocols(t *testing.T) {
	_, err := RunAIMock(context.Background(), AIMockOptions{
		Scenario: mockScenario,
		Addr:     "127.0.0.1:18081",
		OpenAddr: ":18082",
		Env:      true,
	})
	require.NoError(t, err)

	env, err := mockEnv(AIMockOptions{Addr: "127.0.0.1:18081", OpenAddr: ":18082"}, "")
	require.NoError(t, err)
	assert.Contains(t, env, "ANTHROPIC_BASE_URL=http://127.0.0.1:18081")
	assert.Contains(t, env, "OPENAI_BASE_URL=http://127.0.0.1:18082/v1", "a bare port must resolve to loopback")
}

func TestRunAIMockEnvOnlyServesOneProtocol(t *testing.T) {
	env, err := mockEnv(AIMockOptions{OpenAddr: "127.0.0.1:18082"}, mockOnlyOpenAI)
	require.NoError(t, err)
	assert.Contains(t, env, "OPENAI_BASE_URL=http://127.0.0.1:18082/v1")
	for _, item := range env {
		assert.NotContains(t, item, "ANTHROPIC_", "--only openai must not export anthropic settings")
	}
}

// An ephemeral port would stop listening the moment the command exits, so the
// export lines would name a dead server.
func TestRunAIMockEnvRequiresAnExplicitAddress(t *testing.T) {
	_, err := RunAIMock(context.Background(), AIMockOptions{Scenario: mockScenario, Env: true})
	require.ErrorContains(t, err, "--env needs --addr")

	_, err = mockEnv(AIMockOptions{Addr: "127.0.0.1:18081"}, "")
	require.ErrorContains(t, err, "--env needs --openai-addr")
}

func TestRunAIMockRejectsUnknownProtocol(t *testing.T) {
	_, err := RunAIMock(context.Background(), AIMockOptions{Scenario: mockScenario, Only: "gemini"})
	require.ErrorContains(t, err, `--only must be "anthropic" or "openai"`)
}

func TestMockEnvRejectsAMalformedAddress(t *testing.T) {
	_, err := mockEnv(AIMockOptions{Addr: "127.0.0.1"}, mockOnlyAnthropic)
	require.ErrorContains(t, err, "is not a host:port address")
}

// Both servers journalling to one path would interleave two protocols into a
// single stream; a single-protocol run keeps the path the user asked for.
func TestJournalPathSplitsPerProtocolOnlyWhenBothServe(t *testing.T) {
	assert.Equal(t, "run.anthropic.jsonl", journalPath("run.jsonl", mockOnlyAnthropic, ""))
	assert.Equal(t, "run.openai.jsonl", journalPath("run.jsonl", mockOnlyOpenAI, ""))
	assert.Equal(t, "run.jsonl", journalPath("run.jsonl", mockOnlyOpenAI, mockOnlyOpenAI))
	assert.Empty(t, journalPath("", mockOnlyAnthropic, ""))
}

func TestRunAIMockServesUntilTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	raw, err := RunAIMock(ctx, AIMockOptions{Scenario: mockScenario})
	require.NoError(t, err)
	result, ok := raw.(AIMockResult)
	require.True(t, ok)
	assert.Equal(t, "text-only", result.Scenario)
	assert.NotEmpty(t, result.Anthropic)
	assert.NotEmpty(t, result.OpenAI)
	assert.Contains(t, result.Env, "OPENAI_BASE_URL="+result.OpenAI)
}
