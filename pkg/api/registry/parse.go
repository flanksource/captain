package registry

import (
	"errors"
	"fmt"
	"strings"
)

// The model grammar
//
//	sonnet                   → {model: claude-sonnet-5, mode: api}
//	sonnet:high              → + effort high
//	agent:sonnet:high        → + mode agent
//	*:fable                  → every mode of the claiming family (multi only)
//	opus:high, sonnet:medium → primary opus:high with a sonnet:medium fallback
//
// One element is `[prefix:]model[:effort]`, where prefix is a runtime mode
// (api | agent | cli | cmux) or "*". Provider names, composite adapter ids, and
// old aliases are rejected rather than translated.
// A two-segment element is disambiguated by content: a known effort tail is
// model:effort, a known prefix head is prefix:model.
//
// This is the only implementation of the grammar. It previously existed twice —
// parseCompactElement in pkg/api and resolveSelectorPart in pkg/ai — over two
// different claim tables, so `--model agent:sol` and `model: agent:sol` in
// frontmatter disagreed about whether the model existed.

// ParseOptions tunes one parse.
type ParseOptions struct {
	// Mode forces the runtime mechanism when the element carries no prefix of its
	// own — the object form of "agent:sonnet". An explicit prefix still wins.
	Mode RuntimeMode
	// BaseName supplies the model for a bare prefix ("cli:" with --model sonnet).
	BaseName string
	// AllowWildcard permits "*" fan-out. Only --multi-models sets it.
	AllowWildcard bool
	// Provider is the family a previous resolution already recorded. It is the
	// fallback when the name alone claims none, which is what makes Resolve a
	// fixed point: a namespaced id ("openai/private-model") resolves to a bare
	// name its own family no longer claims, so re-resolving the result would
	// otherwise fail where the first pass succeeded.
	Provider *Provider
}

// ParseModel parses one compact element into a concrete model. A wildcard, or
// anything that resolves to more than one model, is an error here — use
// ParseModelMulti.
func ParseModel(s string) (Model, error) {
	models, err := ParseModelElement(s, ParseOptions{})
	if err != nil {
		return Model{}, err
	}
	if len(models) != 1 {
		return Model{}, fmt.Errorf("wildcard selector %q is only valid for --multi-models", s)
	}
	return models[0], nil
}

// ParseModelMulti expands one element, fanning a "*" prefix out across every
// mode of the claiming family that actually offers the model.
func ParseModelMulti(s string, opts ParseOptions) ([]Model, error) {
	opts.AllowWildcard = true
	return ParseModelElement(s, opts)
}

// ParseModelElement parses one `[prefix:]model[:effort]` element.
func ParseModelElement(raw string, opts ParseOptions) ([]Model, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty model")
	}

	prefix, name, effort, err := splitElement(raw, opts.BaseName)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("runtime selector %q needs a model, or --model must provide a base model", raw)
	}

	if prefix == "*" {
		if !opts.AllowWildcard {
			return nil, fmt.Errorf("wildcard selector %q is only valid for --multi-models", raw)
		}
		return expandWildcard(raw, name, effort)
	}

	p, _, ok := ProviderForToken(name)
	if !ok {
		// A name no provider claims cannot name a runtime unless a previous
		// resolution already recorded one. Otherwise fail loud rather than
		// guessing a family: the provider is derived from the model, so there is
		// no second field left to supply it.
		p = opts.Provider
		if p == nil {
			return nil, fmt.Errorf("%w: %s (known providers: %s)", ErrUnknownModel, name, ProviderList())
		}
	}

	mode, err := resolveMode(prefix, opts.Mode, p)
	if err != nil {
		return nil, err
	}
	if _, err := p.RequireMode(mode); err != nil {
		return nil, err
	}

	resolved, err := resolveOn(p, mode, name)
	if err != nil {
		return nil, fmt.Errorf("runtime selector %q: %w", raw, err)
	}
	if err := effort.Validate(); err != nil {
		return nil, fmt.Errorf("runtime selector %q: %w", raw, err)
	}
	enriched, err := Model{Name: resolved, Provider: p, Mode: mode, Effort: effort}.WithCapabilities()
	if err != nil {
		return nil, fmt.Errorf("runtime selector %q: %w", raw, err)
	}
	return []Model{enriched}, nil
}

// splitElement pulls the optional prefix and effort suffix off an element.
func splitElement(raw, baseName string) (prefix, name string, effort Effort, err error) {
	tokens := strings.Split(raw, ":")
	for i := range tokens {
		tokens[i] = strings.TrimSpace(tokens[i])
	}
	switch len(tokens) {
	case 1:
		// A bare prefix ("cli") borrows the base model.
		if isPrefix(tokens[0]) && baseName != "" {
			return strings.ToLower(tokens[0]), strings.TrimSpace(baseName), EffortNone, nil
		}
		return "", tokens[0], EffortNone, nil
	case 2:
		if !isPrefix(tokens[0]) {
			return "", "", EffortNone, fmt.Errorf("invalid model configuration: mode %q in %q (valid: %s)", tokens[0], raw, RuntimeModeList())
		}
		return strings.ToLower(tokens[0]), tokens[1], EffortNone, nil
	case 3:
		if !isPrefix(tokens[0]) {
			return "", "", EffortNone, fmt.Errorf("invalid model configuration: mode %q in %q (valid: %s)", tokens[0], raw, RuntimeModeList())
		}
		if !Effort(tokens[2]).Valid() || tokens[2] == "" {
			return "", "", EffortNone, fmt.Errorf("invalid effort %q in %q", tokens[2], raw)
		}
		return strings.ToLower(tokens[0]), tokens[1], Effort(tokens[2]), nil
	default:
		return "", "", EffortNone, fmt.Errorf("invalid compact model %q (too many ':' segments)", raw)
	}
}

// resolveMode picks the mechanism for one element. The order is the whole
// contract: an explicit compact prefix ("agent:opus") wins, then the sibling
// mode field — which by this point already carries the user's configured
// default, applied at the boundary that owns it — and finally the provider's
// declared default.
//
// There is deliberately no fourth source. The model name used to supply one,
// which meant "claude-agent-opus" could override a mode the caller had
// explicitly selected.
func resolveMode(prefix string, authored RuntimeMode, p *Provider) (RuntimeMode, error) {
	if prefix != "" {
		mode, ok := ParseRuntimeMode(prefix)
		if !ok {
			return "", invalidRuntimeMode(RuntimeMode(prefix))
		}
		return mode, nil
	}
	if authored != "" {
		mode, ok := ParseRuntimeMode(string(authored))
		if !ok {
			return "", invalidRuntimeMode(authored)
		}
		return mode, nil
	}
	return p.DefaultMode, nil
}

func expandWildcard(raw, name string, effort Effort) ([]Model, error) {
	p, _, ok := ProviderForToken(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s (known providers: %s)", ErrUnknownModel, name, ProviderList())
	}
	out := make([]Model, 0, len(p.Modes()))
	for _, mode := range p.Modes() {
		resolved, err := resolveOn(p, mode, name)
		if err != nil {
			continue
		}
		enriched, err := Model{Name: resolved, Provider: p, Mode: mode, Effort: effort}.WithCapabilities()
		if err != nil {
			continue
		}
		out = append(out, enriched)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("runtime selector %q is not available on any runtime", raw)
	}
	return out, nil
}

// resolveOn maps a model token onto the exact id a provider's mode accepts,
// failing loud when the catalog knows the model but not on that mode.
func resolveOn(p *Provider, mode RuntimeMode, name string) (string, error) {
	if _, err := p.RequireMode(mode); err != nil {
		return "", err
	}
	if known, available := p.Availability(mode, name); known && !available {
		return "", fmt.Errorf("model %q is not available on %s", name, RuntimeOf(p, mode))
	}
	resolved, _ := p.ResolveExact(mode, name)
	return resolved, nil
}

// isPrefix reports whether a token can lead an element: a runtime mode or the
// wildcard. Provider names and old composite adapter ids are not prefixes.
func isPrefix(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "*" {
		return true
	}
	if _, ok := ParseRuntimeMode(s); ok {
		return true
	}
	return false
}

// ContainsSelector reports whether s carries a prefix selector such as
// "cmux:gpt-5.6-sol" or "*:sonnet-5". Comma-separated lists are scanned per item.
func ContainsSelector(s string) bool {
	for _, part := range splitCSV(s) {
		prefix, _, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		if isPrefix(prefix) {
			return true
		}
	}
	return false
}

// ResolveModel resolves a Model in place: its Name is parsed as a compact
// element and its fallbacks each resolved independently. It is the entry point
// for both `--model` and spec/frontmatter decoding, which is the whole point —
// they used to run different parsers.
func ResolveModel(m Model) (Model, error) {
	m = m.ExpandCSV()
	if strings.TrimSpace(m.Name) == "" {
		return m, nil
	}
	resolved, err := ParseModelElement(m.Name, ParseOptions{Mode: m.Mode, Provider: m.Provider})
	if err != nil {
		if strings.Contains(err.Error(), "invalid model configuration") {
			return Model{}, err
		}
		return Model{}, fmt.Errorf("invalid model configuration: %w", err)
	}
	if len(resolved) != 1 {
		return Model{}, fmt.Errorf("wildcard selector %q is only valid for --multi-models", m.Name)
	}
	out, err := mergeResolved(m, resolved[0])
	if err != nil {
		return Model{}, fmt.Errorf("invalid model configuration: %w", err)
	}
	out.Fallbacks = make(ModelList, 0, len(m.Fallbacks))
	for _, fb := range m.Fallbacks {
		if strings.TrimSpace(fb.Name) == "" {
			out.Fallbacks = append(out.Fallbacks, fb)
			continue
		}
		rfb, err := ParseModelElement(fb.Name, ParseOptions{Mode: fb.Mode, Provider: fb.Provider})
		if err != nil {
			if strings.Contains(err.Error(), "invalid model configuration") {
				return Model{}, err
			}
			return Model{}, fmt.Errorf("invalid model configuration: fallback: %w", err)
		}
		if len(rfb) != 1 {
			return Model{}, fmt.Errorf("wildcard selector %q is only valid for --multi-models", fb.Name)
		}
		merged, err := mergeResolved(fb, rfb[0])
		if err != nil {
			return Model{}, fmt.Errorf("invalid model configuration: fallback: %w", err)
		}
		out.Fallbacks = append(out.Fallbacks, merged)
	}
	return out, nil
}

// ResolveMulti expands --multi-models values into concrete model/mode pairs. Values may be repeated and/or comma-separated, a bare prefix ("cmux")
// borrows the base model, and duplicates are dropped. Each result inherits the
// base model's temperature/cache settings, and its effort when the element does
// not set one.
func ResolveMulti(values []string, base Model) ([]Model, error) {
	base, err := ResolveModel(base)
	if err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		for _, part := range splitCSV(value) {
			models, err := ParseModelMulti(part, ParseOptions{BaseName: base.Name})
			if err != nil {
				return nil, err
			}
			for _, m := range models {
				m.Temperature = base.Temperature
				if m.Effort == EffortNone {
					m.Effort = base.Effort
				}
				m.NoCache = base.NoCache
				key := m.RuntimeKey()
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, m)
			}
		}
	}
	return out, nil
}

func mergeResolved(original, resolved Model) (Model, error) {
	out := original
	if strings.TrimSpace(resolved.Name) != "" {
		out.Name = resolved.Name
	}
	if resolved.ID != "" {
		out.ID = resolved.ID
	}
	if resolved.Mode != "" {
		out.Mode = resolved.Mode
	}
	// The provider is a resolution result, so the parser's answer wins over
	// whatever the caller had. Dropping it here left WithCapabilities to derive
	// one from the already-stripped name — which is a different question, and
	// fails outright for a namespaced id whose bare half claims no family.
	if resolved.Provider != nil {
		out.Provider = resolved.Provider
	}
	if resolved.Effort != EffortNone {
		out.Effort = resolved.Effort
	}
	// Capabilities are derived, never carried over from the request: whatever the
	// caller wrote for them is replaced by what the resolved adapter can do.
	return out.WithCapabilities()
}

// IsUnknownModel reports whether err is the "no provider claims this" failure.
func IsUnknownModel(err error) bool { return errors.Is(err, ErrUnknownModel) }
