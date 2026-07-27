package aimock

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// textRespond stands in for a protocol's respond type: Section is generic over
// it precisely so neither server's schema has to live in this package.
type textRespond struct {
	Text string `yaml:"text"`
}

func TestParseRejectsUnknownTopLevelKey(t *testing.T) {
	_, err := Parse([]byte("anthropc:\n  - respond: {text: typo}\n"))
	require.ErrorContains(t, err, "unknown scenario keys")
	require.ErrorContains(t, err, "anthropc")
}

func TestParseRequiresASection(t *testing.T) {
	_, err := Parse([]byte("name: empty\ndescription: nothing here\n"))
	require.ErrorContains(t, err, "no anthropic or openai section")
}

func TestSectionDecodesOnlyItsOwnProtocol(t *testing.T) {
	scenario, err := Parse([]byte(`
name: both
anthropic:
  - match: {prompt_contains: hello}
    respond: {text: "hi from anthropic"}
openai:
  - match: {prompt_contains: hello}
    respond: {text: "hi from openai"}
`))
	require.NoError(t, err)
	assert.Equal(t, []string{SectionAnthropic, SectionOpenAI}, scenario.Sections())

	rules, err := Section[textRespond](scenario, SectionAnthropic, nil)
	require.NoError(t, err)

	got, err := rules.Next(userRequest("m", "hello there"))
	require.NoError(t, err)
	assert.Equal(t, "hi from anthropic", got.Text)
}

// A server started on a scenario silent about its protocol would miss every
// request; that is worth catching at startup rather than per request.
func TestSectionMissingIsAnError(t *testing.T) {
	scenario, err := Parse([]byte("anthropic:\n  - respond: {text: hi}\n"))
	require.NoError(t, err)

	_, err = Section[textRespond](scenario, SectionOpenAI, nil)
	require.ErrorContains(t, err, `has no "openai" section`)
	require.ErrorContains(t, err, "defines: anthropic")
}

func TestSectionRejectsEmptyRuleList(t *testing.T) {
	scenario, err := Parse([]byte("anthropic: []\n"))
	require.NoError(t, err)

	_, err = Section[textRespond](scenario, SectionAnthropic, nil)
	require.ErrorContains(t, err, "is empty")
}

// A bad regex must fail at load, naming the rule index, rather than on whichever
// request happens to reach it first.
func TestSectionValidatesMatchersAtLoad(t *testing.T) {
	scenario, err := Parse([]byte(`
anthropic:
  - respond: {text: ok}
  - match: {prompt_regex: "([unclosed"}
    respond: {text: never}
`))
	require.NoError(t, err)

	_, err = Section[textRespond](scenario, SectionAnthropic, nil)
	require.ErrorContains(t, err, "rule 1")
	require.ErrorContains(t, err, "prompt_regex")
}

// Every shipped scenario must load and expose both protocol sections — these are
// the files the e2e tests and the `captain ai mock` examples point at.
func TestShippedScenariosLoad(t *testing.T) {
	paths, err := filepath.Glob("testdata/scenarios/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, paths)

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			scenario, err := Load(path)
			require.NoError(t, err)
			assert.Equal(t, []string{SectionAnthropic, SectionOpenAI}, scenario.Sections())
			assert.NotEmpty(t, scenario.Name)
			assert.Equal(t, path, scenario.Source())
		})
	}
}
