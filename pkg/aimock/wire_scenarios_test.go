// ABOUTME: Exercises the consumption and structured-output scenarios over both wire protocols.
// ABOUTME: Consumption is the property that lets one scenario drive a multi-turn agent loop.

package aimock_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/aimock"
	"github.com/flanksource/captain/pkg/aimock/anthropicmock"
	"github.com/flanksource/captain/pkg/aimock/openaimock"
	"github.com/flanksource/captain/pkg/api"
)

// wireTarget is one protocol viewed through captain's own API backends, so these
// specs need no agent binary and run in CI. Each server keeps its own base-URL
// shape and its own generation route, which is why this is a table of closures
// rather than a loop over aimock.Server.
type wireTarget struct {
	name  string
	model api.Model
	start func(t *testing.T, scenario string) (apiURL string, remaining func() []string)
}

func wireTargets() []wireTarget {
	return []wireTarget{{
		name:  "anthropic",
		model: api.Model{Name: "claude-sonnet-4-5", Backend: api.BackendAnthropic},
		start: func(t *testing.T, scenario string) (string, func() []string) {
			t.Helper()
			srv, err := anthropicmock.Start(anthropicmock.Options{Scenario: loadScenario(t, scenario)})
			require.NoError(t, err)
			t.Cleanup(srv.Close)
			return srv.APIURL(), srv.Remaining
		},
	}, {
		name:  "openai",
		model: api.Model{Name: "gpt-5", Backend: api.BackendOpenAI},
		start: func(t *testing.T, scenario string) (string, func() []string) {
			t.Helper()
			srv, err := openaimock.Start(openaimock.Options{Scenario: loadScenario(t, scenario)})
			require.NoError(t, err)
			t.Cleanup(srv.Close)
			return srv.APIURL(), srv.Remaining
		},
	}}
}

// Consumption is what separates these mocks from a plain request/response stub:
// an agent loop asks the same question every turn and must get a different
// answer each time. Every rule in multi-turn.yaml matches the same prompt, so
// only ordered consumption can produce this sequence — and the trailing
// repeat: -1 rule must keep answering once the finite ones are spent.
func TestScenarioRulesAreConsumedInOrder(t *testing.T) {
	const countPrompt = "Please count."
	want := []string{"one", "two", "three", "done counting", "done counting"}

	for _, target := range wireTargets() {
		t.Run(target.name, func(t *testing.T) {
			apiURL, remaining := target.start(t, "multi-turn.yaml")
			provider := mockedProvider(t, target.model, apiURL)

			var got []string
			for range want {
				events, err := provider.ExecuteStream(context.Background(),
					api.Spec{Prompt: api.Prompt{User: countPrompt}})
				require.NoError(t, err)
				text, _ := drain(t, events)
				got = append(got, text)
			}

			assert.Equal(t, want, got)
			assert.Len(t, remaining(), 1, "only the unlimited rule may survive being played out")
		})
	}
}

// A scripted reply is returned verbatim, JSON included: the mock deliberately
// does not validate against the caller's schema, so a structured-output test can
// assert on captain's own parsing — and can script a document that does not fit.
func TestScenarioServesAStructuredDocument(t *testing.T) {
	const notesPrompt = "Draft the release notes."

	for _, target := range wireTargets() {
		t.Run(target.name, func(t *testing.T) {
			apiURL, remaining := target.start(t, "structured-output.yaml")

			events, err := mockedProvider(t, target.model, apiURL).
				ExecuteStream(context.Background(), api.Spec{Prompt: api.Prompt{User: notesPrompt}})
			require.NoError(t, err)
			text, _ := drain(t, events)

			var notes struct {
				Title      string   `json:"title"`
				Highlights []string `json:"highlights"`
				Breaking   bool     `json:"breaking"`
			}
			require.NoError(t, json.Unmarshal([]byte(text), &notes), "served text must be the JSON document")
			assert.Equal(t, "v1.4.0", notes.Title)
			assert.Equal(t, []string{"Faster ingest", "Fewer retries"}, notes.Highlights)
			assert.False(t, notes.Breaking)

			assert.Empty(t, remaining(), "the scenario must be played out")
		})
	}
}

// A prompt no rule matches has to fail loudly and immediately. The status is
// load-bearing rather than incidental: both agent CLIs retry 5xx for a minute
// before reporting, so a miss served as a server error would read as a hang.
func TestScenarioMissIsAnUnretriedClientError(t *testing.T) {
	assert.Equal(t, 400, aimock.MissStatus,
		"a miss must be a client error; a 5xx turns a scenario gap into a retry storm")
}
