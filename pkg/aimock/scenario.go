// ABOUTME: YAML scenario loading. One file may carry an `anthropic:` section, an `openai:` one, or both.
// ABOUTME: Sections stay undecoded until a server asks for its own, so each keeps its protocol-shaped respond schema.

package aimock

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Protocol section names. A scenario file keys its rule lists by these.
const (
	SectionAnthropic = "anthropic"
	SectionOpenAI    = "openai"
)

// Scenario is a parsed scenario file. Rule lists are held as raw YAML until
// Section decodes one into the requesting server's response type — that is what
// lets a single file describe both protocols without either server importing
// the other's types.
type Scenario struct {
	Name        string
	Description string

	sections map[string]*yaml.Node
	source   string
}

// scenarioFile holds each section as a yaml.Node so it stays undecoded until a
// server asks for it. The nodes are values, not pointers: yaml.v3 only
// special-cases the yaml.Node type itself, so a *yaml.Node field falls through
// to ordinary struct decoding and fails on any non-mapping section.
type scenarioFile struct {
	Name        string               `yaml:"name"`
	Description string               `yaml:"description"`
	Anthropic   yaml.Node            `yaml:"anthropic"`
	OpenAI      yaml.Node            `yaml:"openai"`
	Extra       map[string]yaml.Node `yaml:",inline"`
}

// Load reads and parses a scenario file.
func Load(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario %s: %w", path, err)
	}
	scenario, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	scenario.source = path
	return scenario, nil
}

// Parse parses scenario YAML. Unknown top-level keys are rejected so a typo in
// a section name surfaces at load rather than as a silently empty rule set.
func Parse(data []byte) (*Scenario, error) {
	var file scenarioFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse scenario: %w", err)
	}
	if len(file.Extra) > 0 {
		keys := make([]string, 0, len(file.Extra))
		for key := range file.Extra {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("unknown scenario keys %v; expected name, description, %s, %s",
			keys, SectionAnthropic, SectionOpenAI)
	}

	scenario := &Scenario{Name: file.Name, Description: file.Description, sections: map[string]*yaml.Node{}}
	for name, node := range map[string]*yaml.Node{SectionAnthropic: &file.Anthropic, SectionOpenAI: &file.OpenAI} {
		if !node.IsZero() {
			scenario.sections[name] = node
		}
	}
	if len(scenario.sections) == 0 {
		return nil, fmt.Errorf("scenario has no %s or %s section", SectionAnthropic, SectionOpenAI)
	}
	return scenario, nil
}

// Sections lists the protocol sections this scenario defines.
func (s *Scenario) Sections() []string {
	out := make([]string, 0, len(s.sections))
	for name := range s.sections {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Has reports whether the scenario defines a section.
func (s *Scenario) Has(section string) bool {
	_, ok := s.sections[section]
	return ok
}

// Source is the file the scenario was loaded from, or "" when parsed in memory.
func (s *Scenario) Source() string { return s.source }

// Section decodes one protocol section into typed rules. Each server calls it
// with its own respond type. A missing section is an error: a server started on
// a scenario that says nothing about its protocol would answer every request
// with a miss, which is worth catching at startup.
//
// A trailing rule with no `match:` and `repeat: -1` acts as the section default,
// since an empty matcher matches everything and an unlimited rule never exhausts.
func Section[T any](scenario *Scenario, name string, fallback *T) (*Rules[T], error) {
	node, ok := scenario.sections[name]
	if !ok {
		return nil, fmt.Errorf("scenario %q has no %q section (defines: %s)",
			scenario.label(), name, strings.Join(scenario.Sections(), ", "))
	}
	var rules []Rule[T]
	if err := node.Decode(&rules); err != nil {
		return nil, fmt.Errorf("scenario %q section %q: %w", scenario.label(), name, err)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("scenario %q section %q is empty", scenario.label(), name)
	}
	for i := range rules {
		if err := rules[i].Match.Validate(); err != nil {
			return nil, fmt.Errorf("scenario %q section %q rule %d: %w", scenario.label(), name, i, err)
		}
	}
	return NewRules(rules, fallback), nil
}

func (s *Scenario) label() string {
	if s.Name != "" {
		return s.Name
	}
	if s.source != "" {
		return s.source
	}
	return "<inline>"
}
