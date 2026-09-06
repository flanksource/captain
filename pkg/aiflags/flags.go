// Package aiflags is captain's model-selection flag surface, extracted so any
// clicky-based CLI can embed it and get captain's exact model semantics.
//
// Its contract with clicky is the struct tags — there is no interface to satisfy;
// embed ModelFlags anonymously and the flags bind themselves.
//
// It is deliberately a leaf: registry (the model catalog) and captainconfig
// (~/.captain.yaml) are its only captain imports, and both are yaml-only. It must
// never import pkg/api, pkg/ai, or pkg/cli — pkg/cli alone pulls ~1000 packages
// (k8s, AWS, Postgres), which no downstream should inherit to parse `--model`.
// That is affordable because api.Model is a type ALIAS for registry.Model, so the
// Model returned here is interchangeable with api.Model at every call site.
//
// # The invariant
//
// Preserve authored selectors while layering, then apply saved defaults and
// resolve once at the complete request boundary. Compact selector pins retain
// precedence over sibling fields when the final model is expanded.
package aiflags

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// ModelFlags is the model-selection flag surface: the 1:1 flag projection of
// registry.Model and nothing else. Session ids, API keys and budgets are
// deliberately absent — they belong to the request/config, not to a model, and
// keeping them out is what lets any command embed this without inheriting flags
// it has no business owning.
//
// Embed it ANONYMOUSLY — clicky's binder only recurses anonymous fields (it does
// so at any depth), and a named field would silently bind nothing.
//
// Every field is string/[]string/bool on purpose: clicky's binder switches on the
// concrete type and registers NO flag at all for a named type, so Effort and Mode
// cannot be registry.Effort/registry.RuntimeMode here. They are parsed and
// validated in Resolve instead.
//
// Flag names are global to a command — clicky does not prefix embedded structs —
// so this struct owns --model, --fallback, --mode, --provider, --effort,
// --temperature and --no-cache outright. An embedder that redeclares any of them
// panics cobra at init.
//
// It deliberately claims NO single-letter shorthands. Shorthands are per-command
// UX and the letters are scarce: -m here meant `gavel commit` could not embed this
// at all, because -m is its --message (the git convention). A struct meant to be
// embedded everywhere cannot squat them.
//
// No field carries a `default:` tag: defaults only materialize through cobra
// binding, so a directly-constructed ModelFlags would disagree with a bound one.
// Explicit records changed flags whose zero values must override saved values.
type ModelFlags struct {
	Explicit    registry.FieldPresence `flag:"-" json:"-" yaml:"-"`
	Model       string                 `flag:"model" help:"Model name(s), e.g. claude-sonnet-5, a compact selector like agent:opus:high, or a comma-separated primary,fallback list (defaults to the value saved by 'captain configure')"`
	Fallback    []string               `flag:"fallback" help:"Model to try if the primary is unavailable (repeatable; comma-separated allowed)"`
	Mode        string                 `flag:"mode" help:"Runtime mechanism: api|cli|agent|cmux (sdk aliases agent). The provider comes from the model name; a mode prefix on --model wins, and contradicting it fails loud"`
	Effort      string                 `flag:"effort" help:"Reasoning effort: low|medium|high|xhigh|max|ultra (model-dependent)"`
	Temperature string                 `flag:"temperature" help:"Sampling temperature (0.0-2.0)"`
	NoCache     bool                   `flag:"no-cache" help:"Disable response caching"`
}

// ToModel projects authored flags without normalizing compact selectors.
func (f ModelFlags) ToModel() (registry.Model, error) {
	m := registry.Model{
		Explicit: f.Explicit.Clone(),
		Name:     strings.TrimSpace(f.Model),
		Effort:   registry.Effort(strings.TrimSpace(f.Effort)),
		NoCache:  f.NoCache,
	}
	if err := m.Effort.Validate(); err != nil {
		return registry.Model{}, fmt.Errorf("invalid --effort %q: %w", f.Effort, err)
	}
	// Normalize the mode BEFORE it can reach Model.validateMode, which only
	// accepts the four canonical tokens — "sdk" is an accepted input alias.
	if s := strings.TrimSpace(f.Mode); s != "" {
		mode, ok := registry.ParseRuntimeMode(s)
		if !ok {
			return registry.Model{}, fmt.Errorf("invalid --mode %q (valid: %s)", f.Mode, registry.RuntimeModeList())
		}
		m.Mode = mode
	}
	temp, err := f.Temp()
	if err != nil {
		return registry.Model{}, err
	}
	m.Temperature = temp
	m.Fallbacks = FallbackModels(f.Fallback)

	if _, err = m.Expand(); err != nil {
		return registry.Model{}, err
	}
	if strings.TrimSpace(f.Mode) != "" {
		if _, err := m.WithMode(m.Mode); err != nil {
			return registry.Model{}, err
		}
		m = m.ExpandCSV()
		for i := range m.Fallbacks {
			m.Fallbacks[i].Mode = m.Mode
		}
	}
	return m, nil
}

// WithExplicit records the JSON-pointer fields set by a flag binder, including
// --no-cache=false and an explicitly empty --fallback list.
func (f ModelFlags) WithExplicit(paths ...string) ModelFlags {
	f.Explicit = (registry.Model{Explicit: f.Explicit}).WithExplicit(paths...).Explicit
	return f
}

// Resolve is the one-call path: flags + ~/.captain.yaml → a fully resolved Model.
// A broken config surfaces as an error; callers with a captured snapshot use
// ResolveWith.
func (f ModelFlags) Resolve() (registry.Model, error) {
	saved, err := LoadDefaults()
	if err != nil {
		return registry.Model{}, err
	}
	return f.ResolveWith(saved)
}

// ResolveWith is the pure core: no ambient I/O, no globals. Saved defaults arrive
// as a parameter so tests and spec-overlaying callers can drive it directly.
func (f ModelFlags) ResolveWith(saved captainconfig.AIDefaults) (registry.Model, error) {
	m, err := f.ToModel()
	if err != nil {
		return registry.Model{}, err
	}
	applied, err := ApplyDefaults(DefaultOptions{Model: m, Saved: saved})
	if err != nil {
		return registry.Model{}, err
	}
	return registry.ResolveModel(applied.Model)
}

// Overlay layers the flags over an already-structured base (a spec's model),
// flags winning field by field, and resolves the result once.
//
// Model-only consumers use this helper. Full request pipelines preserve these
// authored flags as a layer and resolve their complete specification together.
func (f ModelFlags) Overlay(base registry.Model) (registry.Model, error) {
	saved, err := LoadDefaults()
	if err != nil {
		return registry.Model{}, err
	}
	return f.OverlayWith(base, saved)
}

// OverlayWith is Overlay with saved defaults injected.
func (f ModelFlags) OverlayWith(base registry.Model, saved captainconfig.AIDefaults) (registry.Model, error) {
	over, err := f.ToModel()
	if err != nil {
		return registry.Model{}, err
	}
	merged, err := ApplyDefaults(DefaultOptions{Model: base.Merge(over), Saved: saved})
	if err != nil {
		return registry.Model{}, err
	}
	return registry.ResolveModel(merged.Model)
}

// Temp parses --temperature. A nil result means unset: an explicit 0 and "unset"
// must stay distinguishable, so providers can tell "sample deterministically"
// from "use the model's default".
func (f ModelFlags) Temp() (*float64, error) {
	v, err := parseFloat("temperature", f.Temperature)
	if err != nil || strings.TrimSpace(f.Temperature) == "" {
		return nil, err
	}
	if v < 0 || v > 2 {
		return nil, fmt.Errorf("invalid --temperature %v (valid: 0.0-2.0)", v)
	}
	return &v, nil
}

// FallbackModels turns repeatable/comma-separated --fallback values into models.
func FallbackModels(flags []string) registry.ModelList {
	var out registry.ModelList
	if flags != nil {
		out = registry.ModelList{}
	}
	for _, flag := range flags {
		for _, name := range strings.Split(flag, ",") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, registry.Model{Name: name})
			}
		}
	}
	return out
}

// parseFloat parses a numeric string flag, failing loud rather than silently
// coercing malformed input to zero.
func parseFloat(name, val string) (float64, error) {
	if strings.TrimSpace(val) == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid --%s %q: %w", name, val, err)
	}
	return f, nil
}
