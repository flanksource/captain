package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Compact model syntax
//
// A Model can be written as a compact string instead of the object form, which
// is handy for fallback chains and terse config:
//
//	opus                     → {model: opus}
//	opus:high                → {model: opus, effort: high}
//	agent:opus:high          → {model: opus, effort: high, backend: claude-agent}
//	opus:high, sonnet:medium → primary opus:high with a sonnet:medium fallback
//
// The grammar per comma-separated element is `[mode:]model[:effort]`, where mode
// is a mechanism keyword (api | cli | agent | cmux; sdk is an alias for agent)
// combined with the model's inferred family to a concrete backend. A two-segment
// element is disambiguated by content: a known effort tail is model:effort, a
// known mode head is mode:model.

// mechanismModes are the compact `mode:` keywords, mapped (with the model family)
// to a concrete backend by backendForMode.
func isMode(s string) bool {
	switch s {
	case "api", "cli", "agent", "sdk", "cmux":
		return true
	}
	return false
}

func isEffort(s string) bool {
	switch Effort(s) {
	case EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortUltra:
		return true
	}
	return false
}

// Family returns the model family a backend serves (claude | codex | gemini |
// deepseek), or "" for an unrecognised backend.
func (b Backend) Family() string {
	switch b {
	case BackendAnthropic, BackendClaudeCLI, BackendClaudeAgent, BackendClaudeCmux:
		return "claude"
	case BackendOpenAI, BackendCodexCLI, BackendCodexAgent, BackendCodexCmux:
		return "codex"
	case BackendGemini, BackendGeminiCLI:
		return "gemini"
	case BackendDeepSeek:
		return "deepseek"
	}
	return ""
}

// backendForMode resolves a compact `mode` keyword plus a model name to a concrete
// backend: the model's family (inferred from its name) combined with the mechanism.
func backendForMode(mode, modelName string) (Backend, error) {
	if mode == "sdk" {
		mode = "agent"
	}
	inferred, err := InferBackend(modelName)
	if err != nil {
		return "", fmt.Errorf("mode %q: %w", mode, err)
	}
	family := inferred.Family()
	table := map[string]map[string]Backend{
		"api":   {"claude": BackendAnthropic, "codex": BackendOpenAI, "gemini": BackendGemini, "deepseek": BackendDeepSeek},
		"cli":   {"claude": BackendClaudeCLI, "codex": BackendCodexCLI, "gemini": BackendGeminiCLI},
		"agent": {"claude": BackendClaudeAgent, "codex": BackendCodexAgent},
		"cmux":  {"claude": BackendClaudeCmux, "codex": BackendCodexCmux},
	}
	byFamily := table[mode]
	if byFamily == nil {
		return "", fmt.Errorf("unknown mode %q (valid: api, cli, agent, cmux)", mode)
	}
	backend, ok := byFamily[family]
	if !ok {
		return "", fmt.Errorf("mode %q is not supported for %s models (%q)", mode, family, modelName)
	}
	return backend, nil
}

// parseCompactElement parses one `[mode:]model[:effort]` element.
func parseCompactElement(s string) (Model, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Model{}, fmt.Errorf("empty model")
	}
	tokens := strings.Split(s, ":")
	for i := range tokens {
		tokens[i] = strings.TrimSpace(tokens[i])
	}
	var m Model
	switch len(tokens) {
	case 1:
		m.Name = tokens[0]
	case 2:
		switch {
		case isEffort(tokens[1]):
			m.Name, m.Effort = tokens[0], Effort(tokens[1])
		case isMode(tokens[0]):
			backend, err := backendForMode(tokens[0], tokens[1])
			if err != nil {
				return Model{}, err
			}
			m.Name, m.Backend = tokens[1], backend
		default:
			return Model{}, fmt.Errorf("ambiguous compact model %q (expected model:effort or mode:model)", s)
		}
	case 3:
		if !isMode(tokens[0]) {
			return Model{}, fmt.Errorf("invalid mode %q in %q (valid: api, cli, agent, cmux)", tokens[0], s)
		}
		if !isEffort(tokens[2]) {
			return Model{}, fmt.Errorf("invalid effort %q in %q", tokens[2], s)
		}
		backend, err := backendForMode(tokens[0], tokens[1])
		if err != nil {
			return Model{}, err
		}
		m.Name, m.Effort, m.Backend = tokens[1], Effort(tokens[2]), backend
	default:
		return Model{}, fmt.Errorf("invalid compact model %q (too many ':' segments)", s)
	}
	if m.Name == "" {
		return Model{}, fmt.Errorf("model name required in %q", s)
	}
	return m, nil
}

// parseCompactModel parses a full compact string: comma-separated elements where
// the first is the primary and the rest become its Fallbacks.
func parseCompactModel(s string) (Model, error) {
	parts := splitCSV(s)
	if len(parts) == 0 {
		return Model{}, fmt.Errorf("empty model %q", s)
	}
	primary, err := parseCompactElement(parts[0])
	if err != nil {
		return Model{}, err
	}
	for _, part := range parts[1:] {
		fb, err := parseCompactElement(part)
		if err != nil {
			return Model{}, err
		}
		primary.Fallbacks = append(primary.Fallbacks, fb)
	}
	return primary, nil
}

// Expand parses a compact Name (`[mode:]model[:effort]`, optionally with a
// comma-separated fallback tail) into concrete Name/Effort/Backend + Fallbacks,
// preserving any fields already set on the receiver that the compact form does
// not specify. It is idempotent: a plain model name is returned unchanged.
// Errors on a malformed compact string (unknown mode/effort, ambiguous form).
func (m Model) Expand() (Model, error) {
	if !strings.ContainsAny(m.Name, ":,") {
		m.Name = strings.TrimSpace(m.Name)
		return m, nil
	}
	parsed, err := parseCompactModel(m.Name)
	if err != nil {
		return Model{}, err
	}
	if parsed.Effort == "" {
		parsed.Effort = m.Effort
	}
	if parsed.Backend == "" {
		parsed.Backend = m.Backend
	}
	parsed.ID = m.ID
	parsed.Temperature = m.Temperature
	if !parsed.NoCache {
		parsed.NoCache = m.NoCache
	}
	parsed.Fallbacks = append(parsed.Fallbacks, m.Fallbacks...)
	return parsed, nil
}

// ModelList is the type of Model.Fallbacks. Each entry may be written as a
// compact string ("agent:opus:high") or the object form. It is a named slice
// rather than a method on Model itself because Model is inline-embedded in Spec:
// a value-level Unmarshaler on Model would hijack the whole Spec object and break
// field promotion. A Spec-level `model:` scalar lands in Model.Name (a string) —
// Model.Expand parses that.
type ModelList []Model

func (l *ModelList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*l = nil
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return err
	}
	out := make(ModelList, 0, len(raw))
	for _, elem := range raw {
		e := bytes.TrimSpace(elem)
		if len(e) > 0 && e[0] == '"' {
			var s string
			if err := json.Unmarshal(e, &s); err != nil {
				return err
			}
			m, err := parseCompactElement(s)
			if err != nil {
				return err
			}
			out = append(out, m)
			continue
		}
		var m Model
		if err := json.Unmarshal(e, &m); err != nil {
			return err
		}
		out = append(out, m)
	}
	*l = out
	return nil
}

func (l *ModelList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("fallbacks must be a list, got %v", value.Kind)
	}
	out := make(ModelList, 0, len(value.Content))
	for _, node := range value.Content {
		if node.Kind == yaml.ScalarNode && node.Tag != "!!null" {
			m, err := parseCompactElement(node.Value)
			if err != nil {
				return err
			}
			out = append(out, m)
			continue
		}
		var m Model
		if err := node.Decode(&m); err != nil {
			return err
		}
		out = append(out, m)
	}
	*l = out
	return nil
}
