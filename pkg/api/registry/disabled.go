package registry

import (
	"strings"
	"sync"
)

// DisabledSet is the user's opt-out list: runtime modes, providers, backends,
// models and effort tiers taken out of circulation from the whoami page and
// persisted in ~/.captain.yaml under ai.disabled.
//
// It lives here rather than in captainconfig because the resolution paths that
// must honour it (ModelEfforts, ResolveEffort, Model.Candidates) are in this
// leaf package and cannot import config. captainconfig owns the file format and
// installs the parsed set with SetDisabled.
//
// The zero value disables nothing, so every read path is safe before install.
type DisabledSet struct {
	modes     map[string]struct{}
	providers map[string]struct{}
	backends  map[string]struct{}
	models    map[string]struct{}
	efforts   map[string]struct{}
}

// NewDisabledSet normalizes the raw config lists into lookup sets. Tokens are
// trimmed and lowercased; empty tokens are dropped.
//
// Model entries are keyed by backend, not by webapp menu id: "gemini/gemini-3.5-flash"
// even though that model's menu id is "googleai/gemini-3.5-flash". A bare entry
// with no slash ("claude-opus-4-7") disables that model on every backend.
func NewDisabledSet(modes, providers, backends, models, efforts []string) DisabledSet {
	return DisabledSet{
		modes:     tokenSet(modes),
		providers: tokenSet(providers),
		backends:  tokenSet(backends),
		models:    tokenSet(models),
		efforts:   tokenSet(efforts),
	}
}

func tokenSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		if token := strings.ToLower(strings.TrimSpace(v)); token != "" {
			out[token] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Empty reports whether the set disables nothing at all, letting hot paths skip
// the per-entry checks entirely.
func (d DisabledSet) Empty() bool {
	return len(d.modes) == 0 && len(d.providers) == 0 && len(d.backends) == 0 &&
		len(d.models) == 0 && len(d.efforts) == 0
}

// Mode reports whether a runtime mode is disabled.
func (d DisabledSet) Mode(m RuntimeMode) bool {
	return contains(d.modes, string(m))
}

// Provider reports whether a provider family is disabled. It accepts any
// backend and resolves it to its API provider first, so Provider(claude-cmux)
// answers for anthropic.
func (d DisabledSet) Provider(b Backend) bool {
	return contains(d.providers, string(b.Provider()))
}

// Backend reports whether a backend cannot be used, whether it was disabled
// directly or through its runtime mode or provider family.
func (d DisabledSet) Backend(b Backend) bool {
	return contains(d.backends, string(b)) || d.Mode(b.Mode()) || d.Provider(b)
}

// Model reports whether a model is unusable on a backend: either the backend
// itself is disabled, or the model is listed as "<backend>/<id>" or as a bare
// "<id>" that applies to every backend.
func (d DisabledSet) Model(b Backend, id string) bool {
	if d.Backend(b) {
		return true
	}
	return contains(d.models, id) || contains(d.models, string(b)+"/"+id)
}

// Effort reports whether an effort tier is disabled. EffortNone is never
// disabled: it means "use the backend default" rather than naming a tier.
func (d DisabledSet) Effort(e Effort) bool {
	if e == EffortNone {
		return false
	}
	return contains(d.efforts, string(e))
}

// Efforts drops the disabled tiers from a supported-effort list, preserving
// order. It returns nil for an empty result so callers can test with len().
func (d DisabledSet) Efforts(in []Effort) []Effort {
	if len(d.efforts) == 0 {
		return in
	}
	out := make([]Effort, 0, len(in))
	for _, e := range in {
		if !d.Effort(e) {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// EnabledEfforts is AllEfforts minus the disabled tiers — the tiers still
// selectable for a model the catalog does not describe.
func (d DisabledSet) EnabledEfforts() []Effort {
	return d.Efforts(AllEfforts())
}

// Reason explains why a backend is unusable, for UI hints on the whoami page.
// It returns "" when the backend is enabled. A backend disabled in its own
// right reports "backend", so the UI can tell a directly-toggled card apart
// from one switched off by its mode or provider.
func (d DisabledSet) Reason(b Backend) string {
	switch {
	case contains(d.backends, string(b)):
		return "backend " + string(b)
	case d.Mode(b.Mode()):
		return "mode " + string(b.Mode())
	case d.Provider(b):
		return "provider " + string(b.Provider())
	default:
		return ""
	}
}

func contains(set map[string]struct{}, value string) bool {
	if len(set) == 0 {
		return false
	}
	_, ok := set[strings.ToLower(strings.TrimSpace(value))]
	return ok
}

var (
	disabledMu  sync.RWMutex
	disabledSet DisabledSet
)

// SetDisabled installs the process-wide opt-out set. captainconfig calls it at
// startup and after every write to ~/.captain.yaml; tests restore the zero
// value in cleanup.
func SetDisabled(d DisabledSet) {
	disabledMu.Lock()
	defer disabledMu.Unlock()
	disabledSet = d
}

// Disabled returns the installed opt-out set.
func Disabled() DisabledSet {
	disabledMu.RLock()
	defer disabledMu.RUnlock()
	return disabledSet
}
