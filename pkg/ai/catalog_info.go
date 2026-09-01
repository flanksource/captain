package ai

import (
	"encoding/json"
	"os/exec"
	"slices"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

// ModelInfo is the JSON shape served at GET /api/chat/models so a client model
// selector can be data-driven. Configured reports whether the model is
// selectable (its API provider has a key, or its agent backend is installed).
type ModelInfo struct {
	ID           string           `json:"id"`
	Provider     string           `json:"provider"`
	Label        string           `json:"label"`
	Runtime      api.Model        `json:"runtime"`
	Reasoning    bool             `json:"reasoning"`
	Temperature  bool             `json:"temperature"`
	Configured   bool             `json:"configured"`
	Availability api.Availability `json:"availability"`
	// Default marks captain's declared default model, so a client seeds its
	// picker from the menu instead of hardcoding an id that rots on the next
	// release. At most one row carries it, and none does when that model is
	// disabled — the client then falls back to the first configured row.
	Default         bool     `json:"default,omitempty"`
	ContextWindow   int      `json:"contextWindow"`
	InputMediaTypes []string `json:"inputMediaTypes"`
}

// Runtime is published as an api.RuntimeIdentity so the row keeps naming its
// resolved adapter. Marshalling api.Model directly would emit the runtime mode
// under `backend` instead, collapsing every provider's modes into "api"/"agent".
type modelInfoWire struct {
	modelInfoAlias
	Runtime api.RuntimeIdentity `json:"runtime"`
}

type modelInfoAlias ModelInfo

func (m ModelInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(modelInfoWire{modelInfoAlias(m), api.RuntimeIdentityOf(m.Runtime)})
}

func (m *ModelInfo) UnmarshalJSON(data []byte) error {
	var wire modelInfoWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*m = ModelInfo(wire.modelInfoAlias)
	m.Runtime = wire.Runtime.ToModel()
	return nil
}

// CatalogPrefixOf is the id prefix a provider's API-mode catalog rows carry
// ("googleai/gemini-3.5-flash"). It is a model-id detail, not the provider
// identity on the wire — that is providerName, which every surface agrees on.
func CatalogPrefixOf(p *ModelProvider) string {
	if p == nil {
		return ""
	}
	return p.CatalogPrefix
}

// providerName is the one provider token that crosses the wire: the descriptor's
// Name. Google's catalog prefix ("googleai") differs from its name ("google"),
// so a surface that mixed the two silently failed to match its own rows.
func providerName(p *ModelProvider) string {
	if p == nil {
		return ""
	}
	return p.Name
}

// CatalogInfo returns the static model menu annotated with selectability. API
// models are configured when their provider is among configuredProviders (the
// keys the caller resolved); agent/CLI models are configured when their local
// backend binary is installed. Order mirrors the catalog so the client renders a
// stable, grouped menu.
func CatalogInfo(configuredProviders []string) []ModelInfo {
	return catalogInfoFrom(Catalog(), catalogInfoOptions{ConfiguredProviders: configuredProviders})
}

type catalogInfoOptions struct {
	ConfiguredProviders []string
	Adapters            []AdapterStatus
}

// catalogInfoFrom annotates an arbitrary model list with selectability, shared
// by CatalogInfo (static catalog) and LiveCatalogInfo (whoami-probed catalog).
//
// Static execution menus arrive filtered; live descriptive menus may retain
// disabled rows so this seam also supplies their reason and remediation.
func catalogInfoFrom(models []Model, options catalogInfoOptions) []ModelInfo {
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		availability := modelAvailability(m, options)
		// A catalog row already names its provider and mode, so this enriches
		// rather than resolves. A row whose pair the provider does not serve is
		// listed without capability flags instead of being dropped: the menu's
		// job is to show what exists, and its selectability is answered
		// separately by modelAvailability.
		runtime, err := api.Model{
			Name:     m.BareID(),
			Provider: m.Provider,
			Mode:     m.Mode,
		}.WithCapabilities()
		if err != nil {
			runtime = api.Model{Name: m.BareID(), Provider: m.Provider, Mode: m.Mode}
		}
		if m.ID != runtime.Name {
			runtime.ID = m.ID
		}
		out = append(out, ModelInfo{
			ID:              m.ID,
			Provider:        providerName(m.Provider),
			Label:           m.Label,
			Runtime:         runtime,
			Reasoning:       m.Reasoning,
			Temperature:     m.Temperature,
			Configured:      availability.IsAvailable(),
			Availability:    availability,
			Default:         m.ID == registry.DefaultModelID && availability.State != api.AvailabilityDisabled,
			ContextWindow:   m.ContextWindow,
			InputMediaTypes: append([]string(nil), m.InputMediaTypes...),
		})
	}
	return out
}

func modelAvailability(model Model, options catalogInfoOptions) api.Availability {
	disabled := Disabled()
	if disabled.Model(model.Provider, model.Mode, model.BareID()) {
		reason := disabled.Reason(model.Provider, model.Mode)
		if reason == "" {
			reason = "model " + model.ID
		}
		return api.Availability{State: api.AvailabilityDisabled,
			Reason:      "Disabled by " + reason + " in Captain configuration.",
			Remediation: "Enable " + reason + " on the Whoami page, then refresh."}
	}
	if slices.Contains(options.ConfiguredProviders, providerName(model.Provider)) {
		return api.Available()
	}
	for _, adapter := range options.Adapters {
		if adapter.Provider == providerName(model.Provider) && adapter.Mode == string(model.Mode) {
			return AvailabilityForAdapter(adapter)
		}
	}
	status := AdapterStatus{
		Provider: providerName(model.Provider),
		Mode:     string(model.Mode),
		Type:     model.Mode.Kind(),
	}
	if !model.IsAgent() {
		return AvailabilityForAdapter(status)
	}
	binary := requiredBinary(model.Provider, model.Mode)
	if binary != "" && binaryOnPath(binary) {
		return api.Available()
	}
	status.BinaryMissing = binary
	return AvailabilityForAdapter(status)
}

// requiredBinary is the executable a local transport needs on PATH, declared per
// provider×mode cell. It is read rather than switched on: the Anthropic agent SDK
// runs under tsx, which no rule derived from the family or the mode would produce.
func requiredBinary(p *ModelProvider, mode RuntimeMode) string {
	if p == nil {
		return ""
	}
	caps, ok := p.Caps(mode)
	if !ok {
		return ""
	}
	return caps.RequiredBinary
}

func binaryOnPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
