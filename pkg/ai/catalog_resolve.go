package ai

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
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
	Backend     Backend // empty = all backends
	Filter      string  // substring filter on id/label; non-empty also reveals legacy ids
	UseTokens   bool    // when true, augment API backends (that have a key) with live /v1/models
	Refresh     bool    // bypass the persisted cache and re-resolve
	Credentials CredentialSnapshot
	APIURL      string // optional provider base URL; valid only for a selected API backend
}

// liveModelFetcher fetches a backend's live model list. It is a package var so
// tests can stub it without hitting the network.
var liveModelFetcher = func(ctx context.Context, b Backend, token, endpoint string) ([]ModelDef, error) {
	return listModelsWithAPIKeyAtEndpoint(ctx, b, token, endpoint)
}

// apiBackends are the direct-API backends the resolver can list live.
var apiBackends = []Backend{BackendAnthropic, BackendOpenAI, BackendGemini, BackendDeepSeek}

// ResolveModels returns the merged catalog ∪ live-API view for opts, joined to
// merged OpenRouter/static pricing and persisted in an auth-scoped cache entry.
// A fresh, fingerprint-matching cache is reused (unless opts.Refresh).
func ResolveModels(ctx context.Context, opts ResolveOptions) ([]ResolvedModel, error) {
	credentials, err := credentialSnapshotForOptions(opts)
	if err != nil {
		return nil, err
	}
	fp, cacheable, err := resolveFingerprint(opts, credentials)
	if err != nil {
		return nil, err
	}

	if !cacheable {
		rows, err := resolveFresh(ctx, opts, credentials)
		return filterResolved(rows, opts.Filter), err
	}

	unlock, err := lockModelCache(ctx, fp)
	if err != nil {
		catalogLog.Warnf("model disk cache disabled: failed to lock cache entry: %v", err)
		rows, resolveErr := resolveFresh(ctx, opts, credentials)
		return filterResolved(rows, opts.Filter), resolveErr
	}
	defer unlock()

	if rows, ok := cachedRows(opts, fp); ok {
		return filterResolved(rows, opts.Filter), nil
	}
	rows, err := resolveFresh(ctx, opts, credentials)
	if err != nil {
		return nil, err
	}
	if err := saveModelCache(fp, rows); err != nil {
		catalogLog.Warnf("failed to persist model cache: %v", err)
	}
	return filterResolved(rows, opts.Filter), nil
}

func credentialSnapshotForOptions(opts ResolveOptions) (CredentialSnapshot, error) {
	if strings.TrimSpace(opts.APIURL) != "" && (opts.Backend == "" || opts.Backend.Kind() != "api") {
		return CredentialSnapshot{}, fmt.Errorf("APIURL requires one selected API backend")
	}
	if opts.Credentials.supplied {
		return opts.Credentials.clone(), nil
	}
	resolved := make(map[Backend]api.ResolvedAPIKey)
	if opts.UseTokens {
		for _, backend := range selectedAPIBackends(opts.Backend) {
			credential, err := ResolveAPIKey(backend)
			if err != nil {
				return CredentialSnapshot{}, err
			}
			resolved[backend] = credential
		}
	}
	return NewCredentialSnapshot(resolved), nil
}

func selectedAPIBackends(backend Backend) []Backend {
	if backend == "" {
		return append([]Backend(nil), apiBackends...)
	}
	if backend.Kind() != "api" {
		return nil
	}
	for _, candidate := range apiBackends {
		if candidate == backend {
			return []Backend{backend}
		}
	}
	return nil
}

// cachedRows returns the persisted rows when they are fresh and match the
// current fingerprint. Callers hold the entry lock while reading.
func cachedRows(opts ResolveOptions, fp string) ([]ResolvedModel, bool) {
	if opts.Refresh {
		return nil, false
	}
	c, err := loadModelCache(fp)
	if err != nil || c == nil || c.expired() || c.KeyFingerprint != fp {
		return nil, false
	}
	return c.Models, true
}

// resolveFresh seeds the catalog filtered by backend, unions live API models
// when tokens are present, and joins each row to pricing. opts.Refresh reaches
// the pricing snapshot too: bypassing the model cache while still pricing from a
// day-old OpenRouter snapshot would only half-honour --no-cache.
func resolveFresh(ctx context.Context, opts ResolveOptions, credentials CredentialSnapshot) ([]ResolvedModel, error) {
	rows, index := seedCatalog(opts.Backend)

	if opts.UseTokens {
		if err := unionLive(ctx, opts, credentials, &rows, index); err != nil {
			return nil, err
		}
	}

	pricing.EnsureLoaded(pricing.LoadOptions{Refresh: opts.Refresh})
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
func unionLive(ctx context.Context, opts ResolveOptions, credentials CredentialSnapshot, rows *[]ResolvedModel, index map[modelKey]int) error {
	for _, b := range selectedAPIBackends(opts.Backend) {
		resolved := credentials.APIKey(b)
		if strings.TrimSpace(resolved.Token) == "" {
			continue
		}
		endpoint, err := modelListEndpoint(b, opts.APIURL)
		if err != nil {
			return err
		}
		live, err := liveModelFetcher(ctx, b, resolved.Token, endpoint)
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
//   - v3: live sources are namespaced by exact credential and model endpoint,
//     using a machine-local keyed HMAC instead of a comparable fixed-salt hash.
const resolveSchemaVersion = "v3"

type resolveCacheSource struct {
	Backend      Backend `json:"backend"`
	EndpointHash string  `json:"endpointHash"`
	TokenHMAC    string  `json:"tokenHMAC"`
}

type resolveCacheDescriptor struct {
	Schema      string               `json:"schema"`
	Backend     Backend              `json:"backend"`
	UseTokens   bool                 `json:"useTokens"`
	LiveSources []resolveCacheSource `json:"liveSources,omitempty"`
}

// resolveFingerprint returns a canonical, non-secret cache identity. A secure
// HMAC-key failure disables token-bearing disk caching rather than failing live
// model discovery or falling back to a comparable unkeyed token hash.
func resolveFingerprint(opts ResolveOptions, credentials CredentialSnapshot) (string, bool, error) {
	descriptor := resolveCacheDescriptor{
		Schema:    resolveSchemaVersion,
		Backend:   opts.Backend,
		UseTokens: opts.UseTokens,
	}
	var hmacKey []byte
	for _, backend := range selectedAPIBackends(opts.Backend) {
		if !opts.UseTokens {
			break
		}
		resolved := credentials.APIKey(backend)
		if strings.TrimSpace(resolved.Token) == "" {
			continue
		}
		endpoint, err := modelListEndpoint(backend, opts.APIURL)
		if err != nil {
			return "", false, err
		}
		if hmacKey == nil {
			hmacKey, err = modelCacheHMACKey()
			if err != nil {
				catalogLog.Warnf("model disk cache disabled: secure identity key unavailable")
				return "", false, nil
			}
		}
		endpointHash := sha256.Sum256([]byte(endpoint))
		mac := hmac.New(sha256.New, hmacKey)
		_, _ = mac.Write([]byte(resolved.Token))
		descriptor.LiveSources = append(descriptor.LiveSources, resolveCacheSource{
			Backend:      backend,
			EndpointHash: fmt.Sprintf("%x", endpointHash),
			TokenHMAC:    fmt.Sprintf("%x", mac.Sum(nil)),
		})
	}
	sort.Slice(descriptor.LiveSources, func(i, j int) bool {
		return descriptor.LiveSources[i].Backend < descriptor.LiveSources[j].Backend
	})
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return "", false, err
	}
	fingerprint := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", fingerprint), true, nil
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
