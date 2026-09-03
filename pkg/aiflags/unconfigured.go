package aiflags

import (
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// ErrUnconfigured marks the "nothing chose a model or a mode" failure so callers
// can recognise it without matching on message text.
var ErrUnconfigured = errors.New("no model configured")

// UnconfiguredError reports that a run reached the execution boundary without a
// model or a mechanism, and names the commands that fix it.
//
// It exists because the alternative is guessing. captain used to fill the gap
// from a compiled-in table — Provider.DefaultMode and the DefaultModelFor list —
// so an unconfigured `--ai-model haiku` silently became `agent:claude-haiku-4-5`
// and no configuration file said so. Those tables now seed `captain configure`
// only; a run that nothing configured stops here.
type UnconfiguredError struct {
	// Field is "model" or "mode": which half is missing.
	Field string
	// Provider is the family the caller named, when it is known. A missing model
	// often means no family is known either, in which case it is nil.
	Provider *registry.Provider
	// Model is whatever the caller did supply, for context in the message.
	Model string
}

func (e *UnconfiguredError) Unwrap() error { return ErrUnconfigured }

func (e *UnconfiguredError) Error() string {
	var b strings.Builder
	if e.Field == "mode" && e.Provider != nil {
		// AgentName, not Name: users speak in families ("gemini models"), not
		// provider keys ("google models").
		fmt.Fprintf(&b, "no runtime mode configured for %s models", e.Provider.AgentName)
	} else if e.Field == "mode" {
		b.WriteString("no runtime mode configured")
	} else {
		b.WriteString("no model configured")
	}
	if e.Model != "" {
		fmt.Fprintf(&b, " (selecting %q)", e.Model)
	}
	b.WriteString("\n  set one with either:")
	b.WriteString("\n    captain configure")
	if e.Provider != nil {
		fmt.Fprintf(&b, " %s", e.Provider.Name)
	}
	b.WriteString("\n    gavel configure                 (per-repo, writes .gavel.yaml)")
	b.WriteString("\n  or name one inline, e.g. --model agent:claude-sonnet-5")
	if e.Field == "mode" && e.Provider != nil {
		fmt.Fprintf(&b, "\n  modes available for %s: %s", e.Provider.AgentName, modeList(e.Provider.Modes()))
	}
	return b.String()
}

func modeList(modes []registry.RuntimeMode) string {
	parts := make([]string, len(modes))
	for i, m := range modes {
		parts[i] = string(m)
	}
	return strings.Join(parts, ", ")
}

// IsUnconfigured reports whether err is the "nothing configured a model" failure.
func IsUnconfigured(err error) bool { return errors.Is(err, ErrUnconfigured) }

// ResolveForRun is the execution boundary: it applies the saved ~/.captain.yaml
// defaults, refuses to proceed when the selection is still incomplete, and then
// resolves against the catalog.
//
// The refusal happens BEFORE registry.ResolveModel deliberately. The registry is
// the grammar — it parses "api:haiku" into a concrete triple and is used to
// render catalogs, validate prompts and replay recorded history, all of which
// legitimately resolve names they never intend to run. Leaving its provider
// fallback intact and gating here means only runs are strict.
//
// Every path that is about to execute goes through this rather than bare
// ResolveModel; that is what makes configuration the single source of a model.
func ResolveForRun(model registry.Model) (registry.Model, error) {
	saved, err := LoadDefaults()
	if err != nil {
		return registry.Model{}, err
	}
	return ResolveForRunWith(model, saved)
}

// ResolveForRunWith is ResolveForRun against an already-loaded config, for
// callers that read ~/.captain.yaml once and resolve many models.
func ResolveForRunWith(model registry.Model, saved captainconfig.AIDefaults) (registry.Model, error) {
	applied, err := ApplyDefaults(model, saved)
	if err != nil {
		return registry.Model{}, err
	}
	if err := requireConfigured(applied); err != nil {
		return registry.Model{}, err
	}
	return registry.ResolveModel(applied)
}

func requireConfigured(model registry.Model) error {
	if strings.TrimSpace(model.Name) == "" {
		return &UnconfiguredError{Field: "model", Provider: model.Provider}
	}
	if model.Mode == "" {
		provider := model.Provider
		if provider == nil {
			if p, _, ok := registry.ProviderForToken(model.Name); ok {
				provider = p
			}
		}
		return &UnconfiguredError{Field: "mode", Provider: provider, Model: model.Name}
	}
	return nil
}
