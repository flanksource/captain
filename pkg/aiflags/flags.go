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
// Expand, then Merge, then Resolve — once, at the end.
//
// Merging an unexpanded override onto a base silently keeps the base's backend:
// base{Backend: claude-agent}.Merge(Model{Name: "api:opus"}) still says
// claude-agent, and resolution then fails or, worse, runs the wrong runtime.
// Expand sets Backend only when the string carries a prefix, so a bare
// `--model opus` correctly inherits the base's backend while `api:opus`
// correctly overrides it. Every ladder in this package and its callers follows
// that order; departing from it is how a model string loses its mode again.
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
// so this struct owns --model/-m, --fallback, --backend/-b, --mode, --effort,
// --temperature and --no-cache outright. An embedder that redeclares any of them
// panics cobra at init.
//
// No field carries a `default:` tag: defaults only materialize through cobra
// binding, so a directly-constructed ModelFlags would disagree with a bound one.
// Zero means unset, everywhere.
type ModelFlags struct {
	Model       string   `flag:"model" short:"m" help:"Model name(s), e.g. claude-sonnet-5, a compact selector like agent:opus:high, or a comma-separated primary,fallback list (defaults to the value saved by 'captain configure')"`
	Fallback    []string `flag:"fallback" help:"Model to try if the primary is unavailable (repeatable; comma-separated allowed)"`
	Backend     string   `flag:"backend" short:"b" help:"Force backend: anthropic|gemini|openai|deepseek|claude-cli|claude-agent|claude-cmux|codex-cli|codex-agent|codex-cmux|gemini-cli (default: inferred from model or saved by 'captain configure')"`
	Mode        string   `flag:"mode" help:"Runtime mechanism: api|cli|agent|cmux (sdk aliases agent). Combined with the model's family to pick a backend; contradicting --backend or a mode prefix on --model fails loud"`
	Effort      string   `flag:"effort" help:"Reasoning effort: low|medium|high|xhigh|max|ultra (model-dependent)"`
	Temperature string   `flag:"temperature" help:"Sampling temperature (0.0-2.0)"`
	NoCache     bool     `flag:"no-cache" help:"Disable response caching"`
}

// ToModel projects the flags onto a Model and expands any compact selector, but
// does NOT resolve it against the catalog. That is the merge-safe form: callers
// layering flags over a spec must Merge before resolving (see the package doc).
func (f ModelFlags) ToModel() (registry.Model, error) {
	m := registry.Model{
		Name:    strings.TrimSpace(f.Model),
		Backend: registry.Backend(strings.TrimSpace(f.Backend)),
		Effort:  registry.Effort(strings.TrimSpace(f.Effort)),
		NoCache: f.NoCache,
	}
	if m.Backend != "" && !m.Backend.Valid() {
		return registry.Model{}, fmt.Errorf("invalid --backend %q (valid: %s)", f.Backend, registry.BackendList())
	}
	if err := m.Effort.Validate(); err != nil {
		return registry.Model{}, fmt.Errorf("invalid --effort %q: %w", f.Effort, err)
	}
	// Normalize the mode BEFORE it can reach Model.validateMode: that check
	// compares Mode against Backend.Mode() by equality, so an un-normalized "sdk"
	// alongside backend claude-agent would report a contradiction that isn't real.
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

	if m.Name != "" {
		if m, err = m.Expand(); err != nil {
			return registry.Model{}, err
		}
	}
	return m, nil
}

// Resolve is the one-call path: flags + ~/.captain.yaml → a fully resolved Model.
// A broken config surfaces as an error; callers that prefer to warn and carry on
// with zero defaults load their own and use ResolveWith.
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
	if !f.NoCache && saved.NoCache {
		m.NoCache = true
	}
	if m, err = ApplyDefaults(m, saved); err != nil {
		return registry.Model{}, err
	}
	return registry.ResolveModel(m)
}

// Overlay layers the flags over an already-structured base (a spec's model),
// flags winning field by field, and resolves the result once.
//
// This is the entry point for callers that have both a config/spec and flags —
// which is most of them. Doing it by hand is how a caller ends up merging an
// unexpanded name onto a populated backend and losing the mode.
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
	merged, err := ApplyDefaults(base.Merge(over), saved)
	if err != nil {
		return registry.Model{}, err
	}
	return registry.ResolveModel(merged)
}

// Temp parses --temperature. A nil result means unset: an explicit 0 and "unset"
// must stay distinguishable, so providers can tell "sample deterministically"
// from "use the model's default".
func (f ModelFlags) Temp() (*float64, error) {
	v, err := parseFloat("temperature", f.Temperature)
	if err != nil || v == 0 {
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
