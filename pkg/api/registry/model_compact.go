package registry

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

// backendForPrefix resolves an element's prefix plus its model name to a concrete
// backend: the provider that claims the model, combined with the mechanism the
// prefix names.
func backendForPrefix(prefix, modelName string) (Backend, error) {
	p, _, _, ok := ProviderForToken(modelName)
	if !ok {
		return "", fmt.Errorf("mode %q: %w: %s (pass an explicit backend: %s)", prefix, ErrUnknownModel, modelName, BackendList())
	}
	mode, err := resolveMode(prefix, ModeAPI, p, modelName)
	if err != nil {
		return "", err
	}
	return p.BackendFor(mode)
}

// parseCompactElement parses one `[prefix:]model[:effort]` element.
//
// It resolves the BACKEND but deliberately leaves Name as written: a decoded
// spec keeps the model the user asked for ("opus"), and only the later resolve
// step (ResolveModel) maps it onto an exact catalog id. The spec is a request,
// not a resolution, so decoding must not bake today's catalog snapshot into it.
func parseCompactElement(s string) (Model, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Model{}, fmt.Errorf("empty model")
	}
	prefix, name, effort, err := splitElement(s, "")
	if err != nil {
		return Model{}, err
	}
	if name == "" {
		return Model{}, fmt.Errorf("model name required in %q", s)
	}
	m := Model{Name: name, Effort: effort}
	if prefix == "" {
		return m, nil
	}
	if prefix == "*" {
		return Model{}, fmt.Errorf("wildcard selector %q is only valid for --multi-models", s)
	}
	backend, err := backendForPrefix(prefix, name)
	if err != nil {
		return Model{}, err
	}
	m.Backend = backend
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
