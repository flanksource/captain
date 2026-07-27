package registry

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnknownModel marks "no provider claims this model name". Callers enrich it
// with "did you mean" suggestions, so it stays a wrapped sentinel.
var ErrUnknownModel = ErrInferBackend

// The model grammar
//
//	sonnet                   → {model: claude-sonnet-5, backend: anthropic}
//	sonnet:high              → + effort high
//	agent:sonnet:high        → + backend claude-agent
//	claude-cmux:opus         → an explicit backend name works as a prefix too
//	*:fable                  → every backend of the claiming family (multi only)
//	opus:high, sonnet:medium → primary opus:high with a sonnet:medium fallback
//
// One element is `[prefix:]model[:effort]`, where prefix is a runtime mode
// (api | cli | agent | cmux; sdk aliases agent), a concrete backend name, or "*".
// A two-segment element is disambiguated by content: a known effort tail is
// model:effort, a known prefix head is prefix:model.
//
// This is the only implementation of the grammar. It previously existed twice —
// parseCompactElement in pkg/api and resolveSelectorPart in pkg/ai — over two
// different claim tables, so `--model agent:sol` and `model: agent:sol` in
// frontmatter disagreed about whether the model existed.

// ParseOptions tunes one parse.
type ParseOptions struct {
	// Backend forces a backend. A prefix that resolves elsewhere is an error.
	Backend Backend
	// Mode forces the runtime mechanism when the element carries no prefix of its
	// own — the object form of "agent:sonnet". An explicit prefix still wins.
	Mode RuntimeMode
	// BaseName supplies the model for a bare prefix ("cli:" with --model sonnet).
	BaseName string
	// AllowWildcard permits "*" fan-out. Only --multi-models sets it.
	AllowWildcard bool
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
// backend of the claiming family that actually offers the model.
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

	p, _, tokenMode, ok := ProviderForToken(name)
	if !ok {
		// An unclaimed model name is fine as long as the caller named a backend:
		// that is how a brand-new provider model works before the catalog
		// snapshot knows about it. Without one, fail loud.
		if opts.Backend == "" {
			return nil, fmt.Errorf("%w: %s (pass an explicit backend: %s)", ErrUnknownModel, name, BackendList())
		}
		forced, mode, found := ProviderFor(opts.Backend)
		if !found {
			return nil, fmt.Errorf("invalid backend %q (valid: %s)", opts.Backend, BackendList())
		}
		p, tokenMode = forced, mode
	}

	// An explicit prefix wins over Model.Mode, which wins over the mode the model
	// name itself implies.
	if prefix == "" && opts.Mode != "" {
		tokenMode = opts.Mode
	}
	mode, err := resolveMode(prefix, tokenMode, p, name)
	if err != nil {
		return nil, err
	}
	backend, err := p.BackendFor(mode)
	if err != nil {
		return nil, err
	}
	if opts.Backend != "" && backend != opts.Backend {
		if prefix == "" {
			// A bare model whose family does not match an explicitly requested
			// backend: report the family clash, not the mode.
			forced, _, ok := ProviderFor(opts.Backend)
			if !ok {
				return nil, fmt.Errorf("invalid backend %q (valid: %s)", opts.Backend, BackendList())
			}
			if forced != p {
				return nil, fmt.Errorf("model %q belongs to the %s family and cannot use backend %q (%s family)",
					name, p.AgentName, opts.Backend, forced.AgentName)
			}
			// Same family, different mode: honour the explicit backend.
			backend = opts.Backend
			mode = modeOf(opts.Backend)
		} else {
			return nil, fmt.Errorf("runtime selector %q resolves to backend %q but --backend is %q", raw, backend, opts.Backend)
		}
	}

	resolved, err := resolveOn(p, mode, name)
	if err != nil {
		return nil, fmt.Errorf("runtime selector %q: %w", raw, err)
	}
	if err := ValidateEffort(backend, resolved, effort); err != nil {
		return nil, fmt.Errorf("runtime selector %q: %w", raw, err)
	}
	return []Model{Model{Name: resolved, Backend: backend, Effort: effort}.Capabilities()}, nil
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
		switch {
		case Effort(tokens[1]).Valid() && tokens[1] != "":
			return "", tokens[0], Effort(tokens[1]), nil
		case isPrefix(tokens[0]):
			return strings.ToLower(tokens[0]), tokens[1], EffortNone, nil
		default:
			return "", "", EffortNone, fmt.Errorf("ambiguous compact model %q (expected model:effort or mode:model)", raw)
		}
	case 3:
		if !isPrefix(tokens[0]) {
			return "", "", EffortNone, fmt.Errorf("invalid mode %q in %q (valid: %s)", tokens[0], raw, RuntimeModeList())
		}
		if !Effort(tokens[2]).Valid() || tokens[2] == "" {
			return "", "", EffortNone, fmt.Errorf("invalid effort %q in %q", tokens[2], raw)
		}
		return strings.ToLower(tokens[0]), tokens[1], Effort(tokens[2]), nil
	default:
		return "", "", EffortNone, fmt.Errorf("invalid compact model %q (too many ':' segments)", raw)
	}
}

// resolveMode reconciles the element's prefix with the mode the model name
// itself implies. An explicit prefix always wins.
func resolveMode(prefix string, tokenMode RuntimeMode, p *Provider, name string) (RuntimeMode, error) {
	if prefix == "" {
		return tokenMode, nil
	}
	if b := Backend(prefix); b.Valid() {
		owner, mode, _ := ProviderFor(b)
		if owner != p {
			return "", fmt.Errorf("model %q belongs to the %s family and cannot use backend %q (%s family)",
				name, p.AgentName, b, owner.AgentName)
		}
		return mode, nil
	}
	mode, ok := ParseRuntimeMode(prefix)
	if !ok {
		return "", fmt.Errorf("unknown runtime selector prefix %q (valid: %s, a backend name, or *)", prefix, RuntimeModeList())
	}
	return mode, nil
}

func expandWildcard(raw, name string, effort Effort) ([]Model, error) {
	p, _, _, ok := ProviderForToken(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s (pass an explicit backend: %s)", ErrUnknownModel, name, BackendList())
	}
	out := make([]Model, 0, len(p.Modes()))
	for _, mode := range p.Modes() {
		resolved, err := resolveOn(p, mode, name)
		if err != nil {
			continue
		}
		backend, err := p.BackendFor(mode)
		if err != nil {
			continue
		}
		if err := ValidateEffort(backend, resolved, effort); err != nil {
			continue
		}
		out = append(out, Model{Name: resolved, Backend: backend, Effort: effort}.Capabilities())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("runtime selector %q is not available on any backend", raw)
	}
	return out, nil
}

// resolveOn maps a model token onto the exact id a provider's mode accepts,
// failing loud when the catalog knows the model but not on that mode.
func resolveOn(p *Provider, mode RuntimeMode, name string) (string, error) {
	backend, err := p.BackendFor(mode)
	if err != nil {
		return "", err
	}
	if known, available := p.Availability(mode, name); known && !available {
		return "", fmt.Errorf("model %q is not available on backend %q", name, backend)
	}
	resolved, _ := p.ResolveExact(mode, name)
	return resolved, nil
}

// isPrefix reports whether a token can lead an element: a runtime mode, a
// concrete backend name, or the wildcard.
func isPrefix(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "*" {
		return true
	}
	if _, ok := ParseRuntimeMode(s); ok {
		return true
	}
	return Backend(s).Valid()
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

func modeOf(b Backend) RuntimeMode {
	_, mode, _ := ProviderFor(b)
	return mode
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
	if err := m.validateMode(); err != nil {
		return Model{}, err
	}
	resolved, err := ParseModelElement(m.Name, ParseOptions{Backend: m.Backend, Mode: m.Mode})
	if err != nil {
		return Model{}, err
	}
	if len(resolved) != 1 {
		return Model{}, fmt.Errorf("wildcard selector %q is only valid for --multi-models", m.Name)
	}
	out := mergeResolved(m, resolved[0])
	out.Fallbacks = make(ModelList, 0, len(m.Fallbacks))
	for _, fb := range m.Fallbacks {
		if strings.TrimSpace(fb.Name) == "" {
			out.Fallbacks = append(out.Fallbacks, fb)
			continue
		}
		rfb, err := ParseModelElement(fb.Name, ParseOptions{Backend: fb.Backend})
		if err != nil {
			return Model{}, err
		}
		if len(rfb) != 1 {
			return Model{}, fmt.Errorf("wildcard selector %q is only valid for --multi-models", fb.Name)
		}
		out.Fallbacks = append(out.Fallbacks, mergeResolved(fb, rfb[0]))
	}
	return out, nil
}

// ResolveMulti expands --multi-models values into concrete runtime model/backend
// pairs. Values may be repeated and/or comma-separated, a bare prefix ("cmux")
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
				key := string(m.Backend) + "\x00" + m.Name + "\x00" + string(m.Effort)
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

func mergeResolved(original, resolved Model) Model {
	out := original
	if strings.TrimSpace(resolved.Name) != "" {
		out.Name = resolved.Name
	}
	if resolved.ID != "" {
		out.ID = resolved.ID
	}
	if resolved.Backend != "" {
		out.Backend = resolved.Backend
	}
	if resolved.Effort != EffortNone {
		out.Effort = resolved.Effort
	}
	// Capabilities are derived, never carried over from the request: whatever the
	// caller wrote for them is replaced by what the resolved adapter can do.
	return out.Capabilities()
}

// IsUnknownModel reports whether err is the "no provider claims this" failure.
func IsUnknownModel(err error) bool { return errors.Is(err, ErrUnknownModel) }
