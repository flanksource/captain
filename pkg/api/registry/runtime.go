package registry

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnknownModel marks "no provider claims this model name" so callers can
// enrich it (e.g. with "did you mean" model suggestions). Naming a model is the
// only question left here: a runtime is no longer inferred from the name.
var ErrUnknownModel = errors.New("unable to infer a provider from the model name")

// Runtime is the pair that decides which adapter serves a request: the provider
// family that owns the model, and the mechanism that runs it.
//
// It exists for the handful of places that need a map key or an ordered set. It
// is deliberately NOT a string enum: captain used to compress this pair into a
// composite id ("claude-agent", "anthropic"), which then meant two different
// things depending on which direction it travelled — outbound the adapter,
// inbound the mode — and clients echoed one back as the other. A runtime is
// always two fields, and Provider is always derived from the model rather than
// selected alongside it.
type Runtime struct {
	// Provider is the canonical provider key: anthropic | openai | google | deepseek.
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	// Mode is the mechanism: api | agent | cli | cmux.
	Mode RuntimeMode `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// Runtime pairs this provider with a mode. It reads better than RuntimeOf at a
// call site that already has the descriptor in hand.
func (p *Provider) Runtime(mode RuntimeMode) Runtime { return RuntimeOf(p, mode) }

// RuntimeOf pairs a provider descriptor with a mode.
func RuntimeOf(p *Provider, mode RuntimeMode) Runtime {
	if p == nil {
		return Runtime{Mode: mode}
	}
	return Runtime{Provider: p.Name, Mode: mode}
}

// ModelProvider resolves the descriptor this runtime names. ok is false when the
// provider is unknown or does not serve the mode.
func (r Runtime) ModelProvider() (*Provider, bool) {
	p, ok := ProviderByName(r.Provider)
	if !ok {
		return nil, false
	}
	if _, ok := p.Caps(r.Mode); !ok {
		return nil, false
	}
	return p, true
}

// Valid reports whether this provider serves this mode.
func (r Runtime) Valid() bool {
	_, ok := r.ModelProvider()
	return ok
}

// String renders the pair for logs and error text. It is a rendering, not an
// identifier: nothing parses it back, and no wire format carries it.
func (r Runtime) String() string {
	if r.Provider == "" {
		return string(r.Mode)
	}
	return r.Provider + " " + string(r.Mode)
}

// AllRuntimes lists every supported provider×mode pair. Providers come in claim
// order and modes in AllRuntimeModes order — the order a wildcard selector fans
// out over, which is the only one users ever see.
func AllRuntimes() []Runtime {
	var out []Runtime
	for _, p := range Providers() {
		for _, mode := range p.Modes() {
			out = append(out, RuntimeOf(p, mode))
		}
	}
	return out
}

// RuntimeList renders the supported pairs for help and error text, grouped by
// provider so the two axes stay legible: "anthropic: api, agent, cli, cmux;
// google: api, cli".
func RuntimeList() string {
	groups := make([]string, 0, len(Providers()))
	for _, p := range Providers() {
		groups = append(groups, p.Name+": "+modeListOf(p.Modes()))
	}
	return strings.Join(groups, "; ")
}

// ProviderList renders the provider keys for help and error text.
func ProviderList() string {
	names := make([]string, 0, len(Providers()))
	for _, p := range Providers() {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// AuthEnvVars returns the environment variables consulted for a runtime's API
// key, in priority order. Keyless modes ride the local CLI's own login and
// consult none.
func AuthEnvVars(p *Provider, mode RuntimeMode) []string {
	if p == nil {
		return nil
	}
	if caps, ok := p.Caps(mode); ok && caps.Keyless {
		return nil
	}
	return p.SupportedEnvVars()
}

// SupportsToolPolicy reports whether a runtime can carry Permissions.Tools to
// the agent it drives.
func SupportsToolPolicy(p *Provider, mode RuntimeMode) bool {
	if p == nil {
		return false
	}
	caps, ok := p.Caps(mode)
	return ok && caps.ToolPolicy
}

// SupportsCallerTools reports whether a runtime can expose caller-supplied tools
// rather than only its own built-in ecosystem. It is the axis that decides
// whether a per-tool policy is enforceable by omission — captain builds that tool
// list itself, so it can drop a denied tool without the agent's cooperation.
func SupportsCallerTools(p *Provider, mode RuntimeMode) bool {
	if p == nil {
		return false
	}
	caps, ok := p.Caps(mode)
	return ok && caps.CallerTools
}

// ToolPolicyRuntimes lists the runtimes that can carry a per-tool policy, in
// canonical order, for help and error text.
func ToolPolicyRuntimes() []Runtime {
	var out []Runtime
	for _, r := range AllRuntimes() {
		p, ok := r.ModelProvider()
		if !ok {
			continue
		}
		if SupportsToolPolicy(p, r.Mode) {
			out = append(out, r)
		}
	}
	return out
}

// RuntimesList renders a set of runtimes as comma-separated pairs for error text.
func RuntimesList(runtimes []Runtime) string {
	parts := make([]string, len(runtimes))
	for i, r := range runtimes {
		parts[i] = r.String()
	}
	return strings.Join(parts, ", ")
}

// ProviderFor resolves the family that owns a model name, failing loud when no
// provider claims it.
//
// The provider is the only thing a model name determines. A mode is never
// inferred here: this function used to return one alongside the provider, so a
// name like "claude-agent-opus" silently overrode the mode the caller had
// actually selected. Callers that need a mode take it from the selector, the
// spec, or Provider.DefaultMode.
//
// It reads the same claim table as the rest of the parser. It used to carry its
// own prefix switch, which knew about grok but not sora or the codex codenames,
// while pkg/ai's two claim tables each knew a different subset — so the answer
// depended on which entry point you came through.
func ProviderFor(model string) (*Provider, error) {
	p, _, ok := ProviderForToken(model)
	if !ok {
		return nil, fmt.Errorf("%w: %s (known providers: %s)", ErrUnknownModel, model, ProviderList())
	}
	return p, nil
}
