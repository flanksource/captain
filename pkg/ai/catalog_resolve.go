package ai

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/commons/logger"
)

// catalogLog reports non-fatal resolver issues (e.g. an unwritable model cache).
var catalogLog = logger.GetLogger("ai")

// ResolvedModel is one merged catalog/live row joined to its pricing.
type ResolvedModel struct {
	Model
	// Live is true when the model was confirmed by a live /v1/models listing.
	Live bool
	// Price is the merged OpenRouter/static pricing for the model (zero when
	// unknown).
	Price pricing.ModelInfo
}

// RuntimeID is the model id a caller passes to a backend: the AgentModel slug
// for agent backends, the bare "model" (no provider/ prefix) for API backends.
func (r ResolvedModel) RuntimeID() string {
	if r.IsAgent() {
		if r.AgentModel != "" {
			return r.AgentModel
		}
		return r.ID
	}
	return r.BareID()
}

// Context returns the model's context window, preferring the priced value and
// falling back to the catalog's.
func (r ResolvedModel) Context() int {
	if r.Price.ContextWindow > 0 {
		return r.Price.ContextWindow
	}
	return r.ContextWindow
}

// ResolveOptions controls a ResolveModels query.
type ResolveOptions struct {
	Backend   Backend // empty = all backends
	Filter    string  // substring filter on id/label; non-empty also reveals legacy ids
	UseTokens bool    // when true, augment API backends (that have a key) with live /v1/models
	Refresh   bool    // bypass the persisted cache and re-resolve
}

// liveModelFetcher fetches a backend's live model list. It is a package var so
// tests can stub it without hitting the network.
var liveModelFetcher = func(ctx context.Context, b Backend) ([]ModelDef, error) {
	return ListModels(ctx, b)
}

// apiBackends are the direct-API backends the resolver can list live.
var apiBackends = []Backend{BackendAnthropic, BackendOpenAI, BackendGemini, BackendDeepSeek}

// ResolveModels returns the merged catalog ∪ live-API view for opts, joined to
// merged OpenRouter/static pricing and persisted to ~/.config/captain/models.json.
// A fresh, fingerprint-matching cache is reused (unless opts.Refresh).
func ResolveModels(ctx context.Context, opts ResolveOptions) ([]ResolvedModel, error) {
	fp := resolveFingerprint(opts)

	rows, ok := cachedRows(opts, fp)
	if !ok {
		var err error
		rows, err = resolveFresh(ctx, opts)
		if err != nil {
			return nil, err
		}
		if err := saveModelCache(fp, rows); err != nil {
			catalogLog.Warnf("failed to persist model cache: %v", err)
		}
	}
	return filterResolved(rows, opts.Filter), nil
}

// cachedRows returns the persisted rows when they are fresh and match the
// current fingerprint.
func cachedRows(opts ResolveOptions, fp string) ([]ResolvedModel, bool) {
	if opts.Refresh {
		return nil, false
	}
	c, err := loadModelCache()
	if err != nil || c == nil || c.expired() || c.KeyFingerprint != fp {
		return nil, false
	}
	return c.Models, true
}

// resolveFresh seeds the catalog filtered by backend, unions live API models
// when tokens are present, and joins each row to pricing.
func resolveFresh(ctx context.Context, opts ResolveOptions) ([]ResolvedModel, error) {
	rows, index := seedCatalog(opts.Backend)

	if opts.UseTokens {
		if err := unionLive(ctx, opts.Backend, &rows, index); err != nil {
			return nil, err
		}
	}

	pricing.EnsureLoaded()
	for i := range rows {
		if info, ok := lookupPricing(rows[i].Backend, rows[i].BareID()); ok {
			rows[i].Price = info
		}
	}
	return rows, nil
}

// modelKey dedups a (backend, bare-id) pair across catalog and live listings.
type modelKey struct {
	backend Backend
	bare    string
}

func seedCatalog(backend Backend) ([]ResolvedModel, map[modelKey]int) {
	rows := make([]ResolvedModel, 0)
	index := map[modelKey]int{}
	for _, m := range Catalog() {
		if !catalogBackendMatch(backend, m.Backend) {
			continue
		}
		index[modelKey{m.Backend, m.BareID()}] = len(rows)
		rows = append(rows, ResolvedModel{Model: m})
	}
	return rows, index
}

// catalogBackendMatch reports whether a catalog model's backend satisfies the
// requested filter. cli/cmux backends share their agent catalog entries.
func catalogBackendMatch(want, modelBackend Backend) bool {
	if want == "" {
		return true
	}
	switch want {
	case BackendClaudeCLI, BackendClaudeCmux:
		want = BackendClaudeAgent
	case BackendCodexCLI, BackendCodexCmux:
		want = BackendCodexAgent
	}
	return modelBackend == want
}

// unionLive fetches live /v1/models for every API backend (matching the filter)
// that has a key set, and merges them into rows. A fetch error fails loud.
func unionLive(ctx context.Context, backend Backend, rows *[]ResolvedModel, index map[modelKey]int) error {
	for _, b := range apiBackends {
		if backend != "" && b != backend {
			continue
		}
		if GetAPIKeyFromEnv(b) == "" {
			continue
		}
		live, err := liveModelFetcher(ctx, b)
		if err != nil {
			return fmt.Errorf("%s: %w", b, err)
		}
		for _, d := range live {
			key := modelKey{b, bareModelID(d.ID)}
			if i, ok := index[key]; ok {
				(*rows)[i].Live = true
				continue
			}
			index[key] = len(*rows)
			*rows = append(*rows, ResolvedModel{
				Model: Model{ID: d.ID, Backend: b, Label: d.Name, ReleaseDate: d.ReleaseDate},
				Live:  true,
			})
		}
	}
	return nil
}

// filterResolved applies the substring filter; with no filter it also hides
// legacy/non-chat ids (an explicit filter reveals them, matching `ai models`).
func filterResolved(rows []ResolvedModel, filter string) []ResolvedModel {
	fl := strings.ToLower(filter)
	out := make([]ResolvedModel, 0, len(rows))
	for _, r := range rows {
		if filter != "" {
			if !strings.Contains(strings.ToLower(r.ID), fl) && !strings.Contains(strings.ToLower(r.Label), fl) {
				continue
			}
		} else if IsLegacyModelIDForBackend(r.ID, r.Backend) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func resolveFingerprint(opts ResolveOptions) string {
	var present []string
	for _, b := range apiBackends {
		if GetAPIKeyFromEnv(b) != "" {
			present = append(present, string(b))
		}
	}
	sort.Strings(present)
	return fmt.Sprintf("b=%s|tok=%v|keys=%s", opts.Backend, opts.UseTokens, strings.Join(present, ","))
}

func bareModelID(id string) string {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		return id[i+1:]
	}
	return id
}

// AgentCatalogModels returns the model list for a CLI/agent backend from the
// catalog — the key-free source of truth shared with the chat menu and shell
// completion. Returned IDs are exact provider model IDs; legacy AgentModel is
// still honored for externally registered old entries.
func AgentCatalogModels(b Backend) []ModelDef {
	want := b
	switch want {
	case BackendClaudeCLI, BackendClaudeCmux:
		want = BackendClaudeAgent
	case BackendCodexCLI, BackendCodexCmux:
		want = BackendCodexAgent
	}

	out := []ModelDef{}
	for _, m := range Catalog() {
		if !m.IsAgent() || m.Backend != want {
			continue
		}
		id := m.ID
		if m.AgentModel != "" {
			id = m.AgentModel
		}
		label := m.Label
		if label == "" {
			label = id
		}
		out = append(out, ModelDef{ID: id, Name: label, Backend: b, ReleaseDate: m.ReleaseDate})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// lookupPricing tries the bare model id first, then the OpenRouter
// "provider/model" key the registry uses for the major API providers.
func lookupPricing(backend Backend, id string) (pricing.ModelInfo, bool) {
	if info, ok := pricing.GetModelInfo(id); ok {
		return info, true
	}
	prefix := orPrefix(backend)
	if prefix == "" {
		return pricing.ModelInfo{}, false
	}
	if info, ok := pricing.GetModelInfo(prefix + "/" + id); ok {
		return info, true
	}
	return pricing.ModelInfo{}, false
}

// orPrefix is the OpenRouter id prefix for a backend. Gemini's catalog prefix is
// "googleai" but OpenRouter keys it under "google" — get this right or pricing
// silently misses.
func orPrefix(backend Backend) string {
	switch backend {
	case BackendOpenAI:
		return "openai"
	case BackendAnthropic:
		return "anthropic"
	case BackendGemini:
		return "google"
	case BackendDeepSeek:
		return "deepseek"
	}
	return ""
}
