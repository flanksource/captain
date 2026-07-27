package ai

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/captain/pkg/api"
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
	fp, err := resolveFingerprint(opts)
	if err != nil {
		return nil, err
	}

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
		resolved, err := ResolveAPIKey(b)
		if err != nil {
			return err
		}
		if resolved.Token == "" {
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

// resolveSchemaVersion invalidates the on-disk model cache when the meaning of a
// cached row changes rather than its inputs. The fingerprint otherwise covers
// only options and API keys, so a resolution change would keep serving rows
// resolved by the old rules until the TTL expired.
//
// Bump this when catalog ids, capability fields, or pricing resolution change.
//   - v2: model identity unified on the provider descriptors; catalog pricing now
//     resolves through the same prefixed-first path as billing, so cached Claude
//     prices from the static fallback table are stale.
const resolveSchemaVersion = "v2"
const resolveFingerprintSalt = "captain/model-cache/api-key"

func resolveFingerprint(opts ResolveOptions) (string, error) {
	var present []string
	for _, b := range apiBackends {
		resolved, err := ResolveAPIKey(b)
		if err != nil {
			return "", err
		}
		if resolved.Token != "" {
			fingerprint, err := pbkdf2.Key(sha512.New, resolved.Token, []byte(resolveFingerprintSalt), 4096, 8)
			if err != nil {
				return "", fmt.Errorf("fingerprint %s API key: %w", b, err)
			}
			present = append(present, string(b)+":"+hex.EncodeToString(fingerprint))
		}
	}
	sort.Strings(present)
	return fmt.Sprintf("v=%s|b=%s|tok=%v|keys=%s", resolveSchemaVersion, opts.Backend, opts.UseTokens, strings.Join(present, ",")), nil
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
		out = append(out, ModelDef{
			ID:                id,
			Name:              label,
			Backend:           b,
			ReleaseDate:       m.ReleaseDate,
			CapabilitiesKnown: true,
			Reasoning:         m.Reasoning,
			Temperature:       m.Temperature,
			SupportedEfforts:  append([]api.Effort(nil), m.SupportedEfforts...),
			DefaultEffort:     m.DefaultEffort,
			Priority:          m.Priority,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// lookupPricing tries the bare model id first, then the OpenRouter
// "provider/model" key the registry uses.
//
// The prefix comes from the provider descriptor's PricingPrefix, which is
// separate from CatalogPrefix for exactly one reason: Gemini's catalog namespace
// is "googleai" while OpenRouter keys it under "google". Deriving one from the
// other makes every Gemini price silently resolve to nothing. This used to be
// three hand-written maps (PricingIDs, orPrefix, pricingModelID) that disagreed
// about whether CLI/agent backends get a prefix at all — they do; a codex-run
// model costs what the model costs.
func lookupPricing(backend Backend, id string) (pricing.ModelInfo, bool) {
	for _, candidate := range PricingIDs(backend, id) {
		if info, ok := pricing.GetModelInfo(candidate); ok {
			return info, true
		}
	}
	return pricing.ModelInfo{}, false
}
