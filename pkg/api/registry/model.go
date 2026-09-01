package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/collections"
	"github.com/flanksource/commons/merge"
)

// CodexAutoReviewModel is the internal model used by Codex approval reviewers.
// Its transcripts are implementation noise rather than user sessions.
const CodexAutoReviewModel = "codex-auto-review"

// Model identifies which LLM serves a request plus the per-request inference
// knobs. Maps onto the legacy ai.Config.Model + ai.Request.{Temperature,
// ReasoningEffort}.
type Model struct {
	// Name is the catalog model slug, e.g. "claude-sonnet-4-6"; it drives provider
	// inference and pricing lookup.
	Name string `json:"model,omitempty" yaml:"model,omitempty" jsonschema:"required" pretty:"label=Model"`

	// ID is the fully-qualified provider id when it differs from Name, e.g. the
	// genkit "anthropic/claude-sonnet-4-6" or a codex slug. Empty means use Name.
	ID string `json:"id,omitempty" yaml:"id,omitempty" pretty:"label=ID"`

	// Temperature is the sampling temperature in [0,2]. A pointer so an explicit
	// 0.0 is distinguishable from "unset" (fail loud, not a silent default).
	Temperature *float64 `json:"temperature,omitempty" yaml:"temperature,omitempty" pretty:"label=Temp"`

	// Effort is the reasoning effort for thinking-capable models.
	Effort Effort `json:"effort,omitempty" yaml:"effort,omitempty" jsonschema:"enum=,enum=low,enum=medium,enum=high,enum=xhigh,enum=max,enum=ultra" pretty:"label=Effort"`

	// NoCache disables model response caching for this run.
	NoCache bool `json:"noCache,omitempty" yaml:"noCache,omitempty" pretty:"label=No Cache"`

	// Fallbacks are alternative models tried in order when the primary fails with a
	// transient/unavailable error or its provider cannot be constructed. Each is a
	// full Model (own mode/effort/temperature); a fallback's own nested Fallbacks
	// are ignored. Populate it directly, via a comma-separated Name (see ExpandCSV),
	// or via the --fallback flag / fallbacks: frontmatter. Each entry may be a
	// compact string ("agent:opus:high") or the object form (see ModelList).
	Fallbacks ModelList `json:"fallbacks,omitempty" yaml:"fallbacks,omitempty" pretty:"label=Fallbacks"`

	// Mode is the runtime mechanism: api | agent | cli | cmux. It is the only half
	// of the runtime a caller authors — provider identity comes from Name — and the
	// compact model prefix takes precedence over this sibling field.
	Mode RuntimeMode `json:"mode,omitempty" yaml:"mode,omitempty" jsonschema:"enum=api,enum=agent,enum=cli,enum=cmux" pretty:"label=Mode"`

	// The fields below are capabilities of the resolved provider×mode, filled in
	// by Resolve. They are outputs: writing them in a spec does not change what
	// the adapter can do, so Validate rejects any value that contradicts the
	// resolved truth rather than letting a spec claim a capability it lacks.
	// The runtime interface assertions remain authoritative at execution time.

	// Streaming reports that the adapter streams incremental events.
	Streaming bool `json:"streaming,omitempty" yaml:"streaming,omitempty" jsonschema:"readOnly" pretty:"label=Streaming"`
	// MediaTypes are the attachment types this model accepts on this adapter —
	// the model's own declared types clamped by the adapter's ceiling.
	MediaTypes []string `json:"mediaTypes,omitempty" yaml:"mediaTypes,omitempty" jsonschema:"readOnly" pretty:"label=Media Types"`
	// Resume reports that a prior session can be continued by id.
	Resume bool `json:"resume,omitempty" yaml:"resume,omitempty" jsonschema:"readOnly" pretty:"label=Resume"`
	// Interrupt reports that a running turn can be interrupted.
	Interrupt bool `json:"interrupt,omitempty" yaml:"interrupt,omitempty" jsonschema:"readOnly" pretty:"label=Interrupt"`
	// Steer reports that a running turn accepts mid-flight steering.
	Steer bool `json:"steer,omitempty" yaml:"steer,omitempty" jsonschema:"readOnly" pretty:"label=Steer"`
	// CallerTools reports that the runtime can expose caller-supplied tools.
	CallerTools bool `json:"callerTools,omitempty" yaml:"callerTools,omitempty" jsonschema:"readOnly" pretty:"label=Caller Tools"`

	// Provider is the descriptor that owns this model. Never serialized: it holds
	// the whole catalog, so emitting it would inline the registry into every spec.
	// Resolve fills it from the model name; a decoded Model has it nil until then.
	Provider *Provider `json:"-" yaml:"-"`
}

// WithCapabilities fills in Provider and the capability flags for this model's
// provider×mode cell. Resolve applies it; callers holding a model whose name and
// mode are already concrete (a catalog row, a supported-models listing) use it
// directly.
//
// It errors rather than returning the model unchanged. The silent version of
// this was a real trap: a model whose provider could not be claimed, or whose
// mode the provider does not serve, came back looking resolved but with every
// capability flag false — so an adapter that streams was reported as
// non-streaming, and the caller had no way to tell that from a genuine answer.
func (m Model) WithCapabilities() (Model, error) {
	p := m.Provider
	if p == nil {
		var err error
		if p, err = ProviderFor(m.Name); err != nil {
			return Model{}, err
		}
	}
	caps, err := p.RequireMode(m.Mode)
	if err != nil {
		return Model{}, err
	}
	m.Provider = p
	m.Streaming = caps.Streaming
	m.Resume = caps.Resume
	m.Interrupt = caps.Interrupt
	m.Steer = caps.Steer
	m.CallerTools = caps.CallerTools
	m.MediaTypes = p.MediaTypesFor(m.Mode, m.Name)
	return m, nil
}

// WithMode applies one runtime mechanism to the primary model and every
// fallback. A model that already names a different mechanism stays a
// contradiction rather than being silently rewritten.
func (m Model) WithMode(mode RuntimeMode) (Model, error) {
	if mode == "" {
		return m, nil
	}
	if _, ok := ParseRuntimeMode(string(mode)); !ok {
		return Model{}, invalidRuntimeMode(mode)
	}

	var err error
	if m, err = m.Expand(); err != nil {
		return Model{}, err
	}
	apply := func(candidate Model) (Model, error) {
		expanded, expandErr := candidate.Expand()
		if expandErr != nil {
			return Model{}, expandErr
		}
		candidate = expanded
		if candidate.Mode != "" && candidate.Mode != mode {
			return Model{}, fmt.Errorf("invalid model configuration: mode %q contradicts requested mode %q", candidate.Mode, mode)
		}
		candidate.Mode = mode
		if err := candidate.validateMode(); err != nil {
			return Model{}, err
		}
		return candidate, nil
	}

	if m, err = apply(m); err != nil {
		return Model{}, err
	}
	for i := range m.Fallbacks {
		if m.Fallbacks[i], err = apply(m.Fallbacks[i]); err != nil {
			return Model{}, fmt.Errorf("fallback[%d]: %w", i, err)
		}
	}
	return m, nil
}

// Runtime returns the provider that owns this model and the mode that serves
// it. Both are read off the model, not re-derived: after Resolve the pair is a
// fact the model carries. A model that has not been resolved fails loud rather
// than quietly re-inferring a second answer — that duplicate inference is how
// the same selection could mean two different runtimes at two call sites.
func (m Model) Runtime() (*Provider, RuntimeMode, error) {
	if m.Provider == nil {
		return nil, "", fmt.Errorf("model %q has no provider: resolve it before asking for its runtime", m.Name)
	}
	if _, err := m.Provider.RequireMode(m.Mode); err != nil {
		return nil, "", err
	}
	return m.Provider, m.Mode, nil
}

// RuntimeKey is a comparable identity for deduplicating resolved selections:
// which mode runs which model at which effort. It is an in-memory key only —
// nothing parses it back, and no wire format carries it.
func (m Model) RuntimeKey() string {
	return string(m.Mode) + "\x00" + m.Name + "\x00" + string(m.Effort)
}

// Temp returns the temperature and whether it was explicitly set (non-nil), so
// providers can distinguish an intentional 0.0 from "unset, use the default".
func (m Model) Temp() (float64, bool) {
	if m.Temperature == nil {
		return 0, false
	}
	return *m.Temperature, true
}

// Validate checks the model name is present, the knobs are in range, and every
// fallback is itself a well-formed model.
func (m Model) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("model name is required")
	}
	if err := m.validateKnobs(); err != nil {
		return err
	}
	for i, fb := range m.Fallbacks {
		if fb.Name == "" {
			return fmt.Errorf("fallback[%d]: model name is required", i)
		}
		if err := fb.validateKnobs(); err != nil {
			return fmt.Errorf("fallback[%d] %q: %w", i, fb.Name, err)
		}
	}
	return nil
}

// validateKnobs range-checks the per-request inference knobs shared by a primary
// model and each fallback (mode/temperature/effort), independent of Name.
func (m Model) validateKnobs() error {
	if err := m.validateMode(); err != nil {
		return err
	}
	if m.Temperature != nil && (*m.Temperature < 0 || *m.Temperature > 2) {
		return fmt.Errorf("invalid temperature %v (valid: 0.0-2.0)", *m.Temperature)
	}
	return m.Effort.Validate()
}

// validateMode rejects a mode outside the four mechanisms. Provider identity
// comes from the model name, so there is no sibling field left for a mode to
// contradict.
func (m Model) validateMode() error {
	if m.Mode == "" {
		return nil
	}
	if _, ok := ParseRuntimeMode(string(m.Mode)); !ok {
		return invalidRuntimeMode(m.Mode)
	}
	return nil
}

func invalidRuntimeMode(value RuntimeMode) error {
	return fmt.Errorf("invalid model configuration: mode %q (valid: %s)", value, RuntimeModeList())
}

// MergePolicy is the structural-merge policy for a Model, exported so a
// container's policy (api.Spec's) can compose it rather than restate it.
//
// Temperature is a pointer precisely so an explicit 0.0 is distinguishable from
// unset; merging *through* the pointer would read the pointed-at 0.0 as empty
// and drop it, reintroducing the bug the pointer exists to prevent. Provider
// holds the whole catalog and is shared by identity, so it is referenced rather
// than walked.
func MergePolicy() merge.Policy {
	return merge.Policy{
		Replace: []any{(*float64)(nil)},
		Shared:  []any{(*Provider)(nil)},
	}
}

// Merge overlays o's set fields onto m, returning the result. It backs Spec
// merging in pkg/api, where a later layer overrides an earlier one field by field.
//
// An override that names a different model drops the inherited Provider. The
// provider is a resolution result derived from the name, never a field a layer
// authors, so carrying the base's descriptor across a rename would report the
// wrong family for the new model — a prompt pinning `model: claude` overridden
// with `--model codex` came out as an anthropic runtime running "codex".
func (m Model) Merge(o Model) Model {
	merged := merge.Apply(m, o, MergePolicy())
	if name := strings.TrimSpace(o.Name); name != "" && !strings.EqualFold(name, strings.TrimSpace(m.Name)) {
		merged.Provider = o.Provider
		merged.Streaming, merged.MediaTypes = o.Streaming, o.MediaTypes
		merged.Resume, merged.Interrupt = o.Resume, o.Interrupt
		merged.Steer, merged.CallerTools = o.Steer, o.CallerTools
	}
	return merged
}

// ExpandCSV moves any comma-separated tail of Name into name-only Fallbacks
// (prepended, so CSV order is preserved ahead of an explicit Fallbacks list),
// leaving Name as the single primary. It is idempotent: a single-model Name is
// returned with only whitespace trimmed, and a second call is a no-op.
func (m Model) ExpandCSV() Model {
	names := splitCSV(m.Name)
	if len(names) == 0 {
		return m
	}
	m.Name = names[0]
	if len(names) == 1 {
		return m
	}
	tail := make(ModelList, 0, len(names)-1)
	for _, n := range names[1:] {
		tail = append(tail, Model{Name: n})
	}
	m.Fallbacks = append(tail, m.Fallbacks...)
	return m
}

// Candidates returns the ordered models to try: the ExpandCSV primary first, then
// each fallback. Fallbacks inherit the primary's Temperature/NoCache when unset,
// and Effort only when they belong to the same provider family. They keep their
// own Name/Backend (an empty Backend is inferred at construction from the fallback's
// own Name), clear ID, and have nested Fallbacks dropped. A length of 1 means "no fallback".
//
// Candidates the user disabled are dropped. If that empties the chain, a
// substitute on an enabled backend takes its place; if nothing at all is left
// enabled the unfiltered chain is returned, so the caller's own error path — not
// a silent empty list — reports the dead end.
func (m Model) Candidates() []Model {
	all := m.candidates()
	disabled := Disabled()
	if disabled.Empty() {
		return all
	}
	out := make([]Model, 0, len(all))
	for _, candidate := range all {
		// An unresolved candidate is kept: dropping it here would turn a "no such
		// model" error into a silent disappearance.
		p, mode, err := candidate.Runtime()
		if err != nil || !disabled.Model(p, mode, candidate.Name) {
			out = append(out, candidate)
		}
	}
	if len(out) > 0 {
		return out
	}
	if substitute, ok := substituteModel(all[0], disabled); ok {
		return []Model{substitute}
	}
	return all
}

func (m Model) candidates() []Model {
	m = m.ExpandCSV()
	primary := m
	primary.Fallbacks = nil
	out := make([]Model, 0, collections.SafeAdd(1, len(m.Fallbacks)))
	out = append(out, primary)
	for _, fb := range m.Fallbacks {
		fb.Fallbacks = nil
		fb.ID = ""
		if fb.Temperature == nil {
			fb.Temperature = m.Temperature
		}
		if fb.Effort == "" && modelProvider(fb) == modelProvider(m) {
			fb.Effort = m.Effort
		}
		if !fb.NoCache {
			fb.NoCache = m.NoCache
		}
		out = append(out, fb)
	}
	return out
}

// substituteModel picks a stand-in for a chain the user disabled entirely: the
// catalog's top preferred model on an enabled runtime, favouring the original's
// own provider family before crossing to another one. The per-request knobs are
// carried over, with the effort re-resolved against the substitute's own catalog
// entry so an unsupported tier does not travel with it.
func substituteModel(m Model, disabled DisabledSet) (Model, bool) {
	family := modelProvider(m)
	runtimes := AllRuntimes()
	sort.SliceStable(runtimes, func(i, j int) bool {
		return runtimes[i].Provider == family && runtimes[j].Provider != family
	})
	for _, runtime := range runtimes {
		p, ok := runtime.ModelProvider()
		if !ok || disabled.Runtime(p, runtime.Mode) {
			continue
		}
		pick, ok := p.latestModel(runtime.Mode, "")
		if !ok {
			continue
		}
		effort, err := ResolveEffort(p, runtime.Mode, pick.ID, m.Effort)
		if err != nil {
			effort = EffortNone
		}
		return Model{
			Name:        pick.ID,
			Mode:        runtime.Mode,
			Provider:    p,
			Effort:      effort,
			Temperature: m.Temperature,
			NoCache:     m.NoCache,
		}, true
	}
	return Model{}, false
}

// modelProvider names the provider family a model belongs to, or "" when no
// provider claims the name.
func modelProvider(model Model) string {
	if model.Provider != nil {
		return model.Provider.Name
	}
	p, err := ProviderFor(model.Name)
	if err != nil {
		return ""
	}
	return p.Name
}

// splitCSV splits a comma-separated string into trimmed, non-empty parts.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
