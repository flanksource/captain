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
	// Provider narrows to one family; nil means every provider.
	Provider *ModelProvider
	// Mode narrows to one mechanism; empty means every mode.
	Mode        RuntimeMode
	Filter      string // substring filter on id/label; non-empty also reveals legacy ids
	UseTokens   bool   // when true, augment the API mode (where a key exists) with live /v1/models
	Refresh     bool   // bypass the persisted cache and re-resolve
	Credentials CredentialSnapshot
	APIURL      string // optional provider base URL; valid only for one selected API provider
}

// liveModelFetcher fetches a provider's live model list. It is a package var so
// tests can stub it without hitting the network.
var liveModelFetcher = func(ctx context.Context, p *ModelProvider, token, endpoint string) ([]ModelDef, error) {
	return listModelsWithAPIKeyAtEndpoint(ctx, p, token, endpoint)
}

// apiProviders are the providers the resolver can list live over HTTP.
var apiProviders = []*ModelProvider{Anthropic, OpenAI, Google, DeepSeek}

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
	if strings.TrimSpace(opts.APIURL) != "" && (opts.Provider == nil || opts.Mode.Kind() != "api") {
		return CredentialSnapshot{}, fmt.Errorf("APIURL requires one selected provider on the api mode")
	}
	if opts.Credentials.supplied {
		return opts.Credentials.clone(), nil
	}
	resolved := make(map[string]api.ResolvedAPIKey)
	if opts.UseTokens {
		for _, p := range selectedAPIProviders(opts) {
			credential, err := ResolveAPIKey(p, ModeAPI)
			if err != nil {
				return CredentialSnapshot{}, err
			}
			resolved[p.Name] = credential
		}
	}
	return NewCredentialSnapshot(resolved), nil
}

// selectedAPIProviders narrows the live-listing set to the requested provider,
// or every provider when none was named. A request pinned to a local mode lists
// nothing live: those models come from the installed binary, not an HTTP call.
func selectedAPIProviders(opts ResolveOptions) []*ModelProvider {
	if opts.Mode != "" && opts.Mode.Kind() != "api" {
		return nil
	}
	if opts.Provider == nil {
		return append([]*ModelProvider(nil), apiProviders...)
	}
	for _, candidate := range apiProviders {
		if candidate == opts.Provider {
			return []*ModelProvider{candidate}
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
	rows, index := seedCatalog(opts.Provider, opts.Mode)

	if opts.UseTokens {
		if err := unionLive(ctx, opts, credentials, &rows, index); err != nil {
			return nil, err
		}
	}

	pricing.EnsureLoaded(pricing.LoadOptions{Refresh: opts.Refresh})
	for i := range rows {
		if info, ok := lookupPricing(rows[i].Provider, rows[i].BareID()); ok {
			rows[i].Price = info
		}
	}
	return rows, nil
}

// modelKey dedups a (provider, catalog-mode, bare-id) triple across catalog and
// live listings.
type modelKey struct {
	provider string
	mode     RuntimeMode
	bare     string
}

func seedCatalog(provider *ModelProvider, mode RuntimeMode) ([]ResolvedModel, map[modelKey]int) {
	rows := make([]ResolvedModel, 0)
	index := map[modelKey]int{}
	for _, m := range Catalog() {
		if provider != nil && m.Provider != provider {
			continue
		}
		if mode != "" && catalogMode(m.Mode) != catalogMode(mode) {
			continue
		}
		index[catalogKey(m)] = len(rows)
		rows = append(rows, ResolvedModel{Model: m})
	}
	return rows, index
}

func catalogKey(m Model) modelKey {
	name := ""
	if m.Provider != nil {
		name = m.Provider.Name
	}
	return modelKey{provider: name, mode: catalogMode(m.Mode), bare: m.BareID()}
}

// catalogMode is the catalog bucket a mode reads from. Every local transport —
// cli, agent, cmux — drives the same installed binary, so one per-family local
// catalog serves all three; only the API mode has its own listing.
func catalogMode(mode RuntimeMode) RuntimeMode {
	if mode.Kind() == "api" {
		return ModeAPI
	}
	return ModeAgent
}

// unionLive fetches live /v1/models for every selected provider that has a key
// set, and merges them into rows. A fetch error fails loud.
func unionLive(ctx context.Context, opts ResolveOptions, credentials CredentialSnapshot, rows *[]ResolvedModel, index map[modelKey]int) error {
	for _, p := range selectedAPIProviders(opts) {
		resolved := credentials.APIKey(p)
		if strings.TrimSpace(resolved.Token) == "" {
			continue
		}
		endpoint, err := modelListEndpoint(p, opts.APIURL)
		if err != nil {
			return err
		}
		live, err := liveModelFetcher(ctx, p, resolved.Token, endpoint)
		if err != nil {
			return fmt.Errorf("%s: %w", p.Name, err)
		}
		for _, d := range live {
			key := modelKey{provider: p.Name, mode: ModeAPI, bare: bareModelID(d.ID)}
			if i, ok := index[key]; ok {
				(*rows)[i].Live = true
				continue
			}
			index[key] = len(*rows)
			*rows = append(*rows, ResolvedModel{
				Model: Model{ID: d.ID, Provider: p, Mode: ModeAPI, Label: d.Name, ReleaseDate: d.ReleaseDate},
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
		} else if IsLegacyModelIDForRuntime(r.ID, r.Provider, r.Mode) {
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
	// Provider names the family whose endpoint was listed. A live listing is an
	// API-mode call, so the mode adds nothing to the identity here.
	Provider     string `json:"provider"`
	EndpointHash string `json:"endpointHash"`
	TokenHMAC    string `json:"tokenHMAC"`
}

type resolveCacheDescriptor struct {
	Schema      string               `json:"schema"`
	Provider    string               `json:"provider"`
	Mode        RuntimeMode          `json:"mode"`
	UseTokens   bool                 `json:"useTokens"`
	LiveSources []resolveCacheSource `json:"liveSources,omitempty"`
}

// resolveFingerprint returns a canonical, non-secret cache identity. A secure
// HMAC-key failure disables token-bearing disk caching rather than failing live
// model discovery or falling back to a comparable unkeyed token hash.
func resolveFingerprint(opts ResolveOptions, credentials CredentialSnapshot) (string, bool, error) {
	descriptor := resolveCacheDescriptor{
		Schema:    resolveSchemaVersion,
		Mode:      opts.Mode,
		UseTokens: opts.UseTokens,
	}
	if opts.Provider != nil {
		descriptor.Provider = opts.Provider.Name
	}
	var hmacKey []byte
	for _, p := range selectedAPIProviders(opts) {
		if !opts.UseTokens {
			break
		}
		resolved := credentials.APIKey(p)
		if strings.TrimSpace(resolved.Token) == "" {
			continue
		}
		endpoint, err := modelListEndpoint(p, opts.APIURL)
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
			Provider:     p.Name,
			EndpointHash: fmt.Sprintf("%x", endpointHash),
			TokenHMAC:    fmt.Sprintf("%x", mac.Sum(nil)),
		})
	}
	sort.Slice(descriptor.LiveSources, func(i, j int) bool {
		return descriptor.LiveSources[i].Provider < descriptor.LiveSources[j].Provider
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

// AgentCatalogModels returns the model list for a local transport from the
// catalog — the key-free source of truth shared with the chat menu and shell
// completion. Returned IDs are exact provider model IDs; legacy AgentModel is
// still honored for externally registered old entries.
func AgentCatalogModels(p *ModelProvider, mode RuntimeMode) []ModelDef {
	wantMode := catalogMode(mode)

	out := []ModelDef{}
	for _, m := range Catalog() {
		if !m.IsAgent() || m.Provider != p || catalogMode(m.Mode) != wantMode {
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
			Provider:          p.Name,
			Mode:              mode,
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
// about whether the local transports get a prefix at all — they do; a codex-run
// model costs what the model costs.
func lookupPricing(p *ModelProvider, id string) (pricing.ModelInfo, bool) {
	for _, candidate := range PricingIDs(p, id) {
		if info, ok := pricing.GetModelInfo(candidate); ok {
			return info, true
		}
	}
	return pricing.ModelInfo{}, false
}
