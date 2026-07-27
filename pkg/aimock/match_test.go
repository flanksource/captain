package aimock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func userRequest(model, text string) Request {
	return Request{Model: model, Messages: []Message{{Role: RoleUser, Content: text}}}
}

func TestMatchPredicatesAND(t *testing.T) {
	req := Request{
		Model:   "claude-sonnet-5",
		System:  "You are a helpful assistant",
		Headers: map[string]string{"x-api-key": "secret"},
		Messages: []Message{
			{Role: RoleUser, Content: "please list the files"},
			{Role: RoleAssistant, Content: "running ls"},
			{Role: RoleUser, Content: "here is the output", ToolResults: []string{"Bash"}},
		},
	}

	tests := []struct {
		name  string
		match Match
		want  bool
	}{
		{"empty matches anything", Match{}, true},
		{"prompt_contains hits the last user turn", Match{PromptContains: "here is the output"}, true},
		{"prompt_contains ignores earlier turns", Match{PromptContains: "please list the files"}, false},
		{"prompt_regex", Match{PromptRegex: `^here is`}, true},
		{"system_contains", Match{SystemContains: "helpful"}, true},
		{"model exact", Match{Model: "claude-sonnet-5"}, true},
		{"model glob", Match{Model: "claude-*-5"}, true},
		{"model bare name", Match{Model: "sonnet"}, true},
		{"model mismatch", Match{Model: "gpt-5"}, false},
		{"tool_result_for is case-insensitive", Match{ToolResultFor: "bash"}, true},
		{"tool_result_for wrong tool", Match{ToolResultFor: "Read"}, false},
		{"header", Match{Header: map[string]string{"X-Api-Key": "secret"}}, true},
		{"header mismatch", Match{Header: map[string]string{"X-Api-Key": "other"}}, false},
		{"all set predicates must hold", Match{PromptContains: "here is", Model: "gpt-5"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, tt.match.Validate())
			assert.Equal(t, tt.want, tt.match.Matches(req))
		})
	}
}

func TestMatchValidateRejectsBadRegex(t *testing.T) {
	match := Match{PromptRegex: "([unclosed"}
	require.ErrorContains(t, match.Validate(), "prompt_regex")
}

// Consumption is the property mockllm lacks and an agent loop needs: request N
// must get reply N even when every rule matches every request.
func TestRulesConsumeInOrder(t *testing.T) {
	rules := NewRules([]Rule[string]{
		{Match: Match{PromptContains: "count"}, Respond: "one"},
		{Match: Match{PromptContains: "count"}, Respond: "two"},
		{Respond: "default", Repeat: Unlimited},
	}, nil)

	req := userRequest("m", "count please")
	for _, want := range []string{"one", "two", "default", "default"} {
		got, err := rules.Next(req)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestRulesRepeatBoundsFiring(t *testing.T) {
	rules := NewRules([]Rule[string]{
		{Respond: "twice", Repeat: 2},
	}, nil)

	req := userRequest("m", "anything")
	for range 2 {
		got, err := rules.Next(req)
		require.NoError(t, err)
		assert.Equal(t, "twice", got)
	}

	_, err := rules.Next(req)
	require.Error(t, err)
	assert.Empty(t, rules.Remaining(), "an exhausted rule should not be listed as remaining")
}

// The miss diagnostic is the whole point of failing loud: it has to say what was
// asked for and what was still on offer.
func TestRulesMissNamesRequestAndRemaining(t *testing.T) {
	rules := NewRules([]Rule[string]{
		{Match: Match{PromptContains: "expected prompt"}, Respond: "never fires"},
	}, nil)

	_, err := rules.Next(userRequest("claude-sonnet-5", "something else entirely"))
	require.Error(t, err)

	var noMatch *ErrNoMatch
	require.ErrorAs(t, err, &noMatch)
	assert.Contains(t, err.Error(), "claude-sonnet-5")
	assert.Contains(t, err.Error(), "something else entirely")
	assert.Contains(t, err.Error(), `prompt_contains="expected prompt"`)
}

func TestRulesFallbackAnswersAMiss(t *testing.T) {
	fallback := "bland"
	rules := NewRules([]Rule[string]{
		{Match: Match{PromptContains: "never"}, Respond: "unused"},
	}, &fallback)

	got, err := rules.Next(userRequest("m", "no rule matches this"))
	require.NoError(t, err)
	assert.Equal(t, "bland", got)
	assert.Len(t, rules.Remaining(), 1, "a fallback must not consume a rule")
}

func TestRequestNormalizedAccessors(t *testing.T) {
	req := Request{Messages: []Message{
		{Role: RoleUser, Content: "first"},
		{Role: RoleAssistant, Content: "middle"},
		{Role: RoleUser, Content: "second", ToolResults: []string{"Bash"}},
		{Role: RoleAssistant, Content: "last"},
	}}

	assert.Equal(t, "second", req.LastUserText())
	assert.Equal(t, "first\nmiddle\nsecond\nlast", req.AllText())
	assert.Equal(t, []string{"Bash"}, req.ToolResultNames())
}

func TestChunkTextRoundTrips(t *testing.T) {
	tests := []struct {
		text string
		want []string
	}{
		{"", nil},
		{"one", []string{"one"}},
		{"one two three", []string{"one ", "two ", "three"}},
	}
	for _, tt := range tests {
		chunks := ChunkText(tt.text)
		assert.Equal(t, tt.want, chunks)

		var joined string
		for _, chunk := range chunks {
			joined += chunk
		}
		assert.Equal(t, tt.text, joined, "chunks must reassemble into the original text")
	}
}
