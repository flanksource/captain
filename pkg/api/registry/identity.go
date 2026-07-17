package registry

import (
	"sort"
	"strings"
)

// ModelIdentity is captain's parsed model key. It is deliberately provider
// native: aliases and mode prefixes are input conveniences only, never the value
// sent to a backend.
type ModelIdentity struct {
	Provider string
	Family   string
	Version  string
}

// ProviderForToken returns the provider that claims a model token, along with
// the token stripped of any provider namespace and the mode its own prefix
// implies. It is the single claim step: what InferBackend, splitModelProvider,
// and selectorModelFamily each used to answer differently.
//
// An explicit namespace ("anthropic/…", "googleai/…") wins. Otherwise the
// providers are tried in canonical order and the first claim wins; failing that,
// a multi-segment id is retried on its last segment so proxied names such as
// "openrouter/anthropic/claude-x" still resolve.
func ProviderForToken(token string) (*Provider, string, RuntimeMode, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, "", "", false
	}
	// Claim on the alias' target: a codename such as "sol" names no family by
	// itself. Doing this here rather than deep in the resolver is what makes
	// `--model sol` and `--model agent:sol` agree — the selector grammar used to
	// resolve aliases before claiming while the compact grammar did not, so the
	// two disagreed about whether the model existed at all.
	if aliased := resolveAlias(token); aliased != token {
		token = aliased
	}
	if i := strings.IndexByte(token, '/'); i >= 0 {
		if p, ok := ProviderByName(token[:i]); ok {
			rest := strings.TrimPrefix(token[i+1:], "models/")
			mode, claimed := p.claim(rest)
			if !claimed {
				mode = ModeAPI
			}
			return p, rest, mode, true
		}
	}
	canonical := canonicalModelToken(token)
	for _, p := range Providers() {
		if mode, ok := p.claim(canonical); ok {
			return p, strings.TrimPrefix(token, "models/"), mode, true
		}
	}
	// Retry on the last path segment: an id proxied through another namespace
	// ("openrouter/anthropic/claude-x") keeps its full name but resolves off the
	// family it actually names.
	if i := strings.LastIndexByte(token, '/'); i >= 0 {
		for _, p := range Providers() {
			if mode, ok := p.claim(canonicalModelToken(token[i+1:])); ok {
				return p, token, mode, true
			}
		}
	}
	return nil, "", "", false
}

// StripProviderPrefix removes any known provider namespace from a model id.
func StripProviderPrefix(model string) string {
	p, token, _, ok := ProviderForToken(model)
	if !ok {
		return strings.TrimPrefix(strings.TrimSpace(model), "models/")
	}
	return strings.TrimPrefix(strings.TrimSpace(p.bareID(token)), "models/")
}

func canonicalModelToken(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimPrefix(model, "models/")
	model = strings.ReplaceAll(model, "_", "-")
	model = strings.ReplaceAll(model, " ", "-")
	return model
}

// resolveAlias maps a codename onto its exact model id ("sol" → "gpt-5.6-sol").
// Aliases are registry data, so every entry point sees them.
func resolveAlias(model string) string {
	needle := canonicalModelToken(model)
	for _, m := range knownModels {
		for _, alias := range m.Aliases {
			if canonicalModelToken(alias) == needle {
				return m.ID
			}
		}
	}
	return strings.TrimSpace(model)
}

// ParseIdentity parses a model token into this provider's family/version tuple.
// It is deliberately permissive about input prefixes.
func (p *Provider) ParseIdentity(model string) (ModelIdentity, bool) {
	token := canonicalModelToken(p.bareID(model))
	for _, trim := range p.identityTrim {
		token = strings.TrimPrefix(token, trim)
	}
	for _, empty := range p.emptyTokens {
		if token == empty && p.emptyFamily != "" {
			return ModelIdentity{Provider: p.Name, Family: p.emptyFamily}, true
		}
	}
	family, version, ok := splitKnownFamily(token, p.families)
	return ModelIdentity{Provider: p.Name, Family: family, Version: version}, ok
}

func splitKnownFamily(token string, families []string) (family, version string, ok bool) {
	for _, candidate := range families {
		if token == candidate {
			return candidate, "", true
		}
		prefix := candidate + "-"
		if strings.HasPrefix(token, prefix) {
			return candidate, normalizeModelVersion(strings.TrimPrefix(token, prefix)), true
		}
	}
	return "", "", false
}

// availableFor reports whether a catalog row is offered on a mode. The registry
// annotates rows with "api" or "codex" availability; the codex modes are the
// only ones that read the codex list.
func (p *Provider) availableFor(m KnownModel, mode RuntimeMode) bool {
	if len(m.Availability) == 0 {
		return true
	}
	target := "api"
	if p == OpenAI && mode != ModeAPI {
		target = "codex"
	}
	for _, available := range m.Availability {
		if strings.EqualFold(strings.TrimSpace(available), target) {
			return true
		}
	}
	return false
}

func (p *Provider) lookupExact(model string) (KnownModel, bool) {
	needle := canonicalModelToken(StripProviderPrefix(model))
	for _, m := range knownModels {
		if m.Provider != p.Name {
			continue
		}
		if canonicalModelToken(m.ID) == needle {
			return m, true
		}
	}
	return KnownModel{}, false
}

// ResolveExact resolves a user-written model token into the exact id this
// provider's mode should receive. It accepts aliases, namespaces, superseded
// ids, and family shorthands, and never returns any of them.
//
// ok is false when the token could not be resolved to a catalog row; the token
// is still returned (namespace-stripped) so an unknown-but-explicit model still
// reaches its backend — that is how new provider models work before the catalog
// snapshot catches up.
func (p *Provider) ResolveExact(mode RuntimeMode, model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	// A bare coding-agent sentinel ("codex") means "this provider's current
	// model". It is not honoured on the API mode, where "claude" stays a literal.
	if mode != ModeAPI && p.AgentName != "" && strings.EqualFold(model, p.AgentName) {
		if m, ok := p.latestModel(mode, ""); ok {
			return m.ID, true
		}
		return model, false
	}
	model = resolveAlias(model)
	if m, ok := p.lookupExact(model); ok {
		if m.SupersededBy != "" {
			if successor, ok := p.lookupExact(m.SupersededBy); ok && p.availableFor(successor, mode) {
				return successor.ID, true
			}
		} else if p.availableFor(m, mode) {
			return m.ID, true
		}
	}
	identity, ok := p.ParseIdentity(model)
	if !ok {
		return StripProviderPrefix(model), false
	}
	if shouldResolveLatestVersionLine(identity.Version) {
		if m, ok := p.latestModelForVersionLine(mode, identity); ok {
			return m.ID, true
		}
	}
	if m, ok := p.resolveIdentity(mode, identity); ok {
		return m.ID, true
	}
	if identity.Version != "" {
		return StripProviderPrefix(model), false
	}
	if m, ok := p.latestModel(mode, identity.Family); ok {
		return m.ID, true
	}
	return StripProviderPrefix(model), false
}

// Availability distinguishes an unknown model from a catalog model that is
// intentionally not offered on a mode.
func (p *Provider) Availability(mode RuntimeMode, model string) (known, available bool) {
	m, ok := p.lookupExact(resolveAlias(model))
	if !ok {
		return false, false
	}
	return true, p.availableFor(m, mode)
}

// Lookup returns the catalog row for a model id on this provider, accepting
// aliases and provider namespaces.
func (p *Provider) Lookup(model string) (KnownModel, bool) {
	return p.lookupExact(resolveAlias(model))
}

func (p *Provider) resolveIdentity(mode RuntimeMode, identity ModelIdentity) (KnownModel, bool) {
	candidates := make([]KnownModel, 0)
	fallback := make([]KnownModel, 0)
	for _, m := range knownModels {
		if m.Provider != identity.Provider || m.Family != identity.Family || !p.availableFor(m, mode) {
			continue
		}
		if identity.Version == "" || modelVersionMatches(m.Version, identity.Version) {
			if m.Preferred {
				candidates = append(candidates, m)
			} else if identity.Version != "" {
				fallback = append(fallback, m)
			}
		}
	}
	if len(candidates) == 0 {
		candidates = fallback
	}
	if len(candidates) == 0 {
		return KnownModel{}, false
	}
	sortModels(candidates)
	return candidates[0], true
}

func (p *Provider) latestModel(mode RuntimeMode, family string) (KnownModel, bool) {
	candidates := make([]KnownModel, 0)
	for _, m := range knownModels {
		if !m.Preferred || m.Provider != p.Name || !p.availableFor(m, mode) {
			continue
		}
		if family != "" && m.Family != family {
			continue
		}
		candidates = append(candidates, m)
	}
	if len(candidates) == 0 {
		return KnownModel{}, false
	}
	sortModels(candidates)
	return candidates[0], true
}

func (p *Provider) latestModelForVersionLine(mode RuntimeMode, identity ModelIdentity) (KnownModel, bool) {
	if identity.Version == "" {
		return KnownModel{}, false
	}
	major := strings.Split(normalizeModelVersion(identity.Version), ".")[0]
	if major == "" {
		return KnownModel{}, false
	}
	candidates := make([]KnownModel, 0)
	for _, m := range knownModels {
		if m.Provider != identity.Provider || m.Family != identity.Family || !p.availableFor(m, mode) {
			continue
		}
		if modelVersionMatches(m.Version, major) {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return KnownModel{}, false
	}
	sortModelsForResolution(candidates)
	return candidates[0], true
}

func shouldResolveLatestVersionLine(version string) bool {
	version = normalizeModelVersion(version)
	if version == "" {
		return false
	}
	for _, part := range strings.Split(version, ".") {
		if !isNumericVersionPart(part) {
			return false
		}
	}
	return true
}

func sortModels(models []KnownModel) {
	sort.SliceStable(models, func(i, j int) bool {
		left, right := models[i], models[j]
		if priority := comparePriority(left, right); priority != 0 {
			return priority < 0
		}
		if left.ReleaseDate == right.ReleaseDate {
			return compareModelVersions(left.ID, right.ID) > 0
		}
		return left.ReleaseDate > right.ReleaseDate
	})
}

func sortModelsForResolution(models []KnownModel) {
	sort.SliceStable(models, func(i, j int) bool {
		left, right := models[i], models[j]
		if priority := comparePriority(left, right); priority != 0 {
			return priority < 0
		}
		if left.ReleaseDate == right.ReleaseDate && left.Preferred != right.Preferred {
			return left.Preferred
		}
		if left.ReleaseDate == right.ReleaseDate {
			return compareModelVersions(left.ID, right.ID) > 0
		}
		return left.ReleaseDate > right.ReleaseDate
	})
}

// comparePriority orders two rows by explicit priority, returning <0 when left
// sorts first, >0 when right does, and 0 when priority does not decide. Priority
// 0 means "unset" and always loses to an explicit priority.
func comparePriority(left, right KnownModel) int {
	if left.Priority == right.Priority || (left.Priority == 0 && right.Priority == 0) {
		return 0
	}
	if left.Priority == 0 {
		return 1
	}
	if right.Priority == 0 {
		return -1
	}
	return left.Priority - right.Priority
}

func modelVersionMatches(registryVersion, requested string) bool {
	registryVersion = normalizeModelVersion(registryVersion)
	requested = normalizeModelVersion(requested)
	if requested == "" {
		return true
	}
	if registryVersion == requested {
		return true
	}
	return strings.HasPrefix(registryVersion, requested+".") || strings.HasPrefix(registryVersion, requested+"-")
}

func normalizeModelVersion(version string) string {
	version = strings.Trim(strings.ToLower(strings.TrimSpace(version)), "-.")
	if version == "" {
		return ""
	}
	parts := strings.Split(version, "-")
	if len(parts) == 1 {
		return parts[0]
	}
	i := 0
	out := ""
	if isNumericVersionPart(parts[0]) {
		out = parts[0]
		i = 1
		if len(parts) > 1 && isNumericVersionPart(parts[1]) {
			out += "." + parts[1]
			i = 2
		}
	}
	if i < len(parts) {
		if out != "" {
			out += "-"
		}
		out += strings.Join(parts[i:], "-")
	}
	return out
}

func isNumericVersionPart(part string) bool {
	if part == "" {
		return false
	}
	for _, r := range part {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// compareModelVersions orders two model ids by their embedded numeric version
// tokens ("claude-sonnet-4-6" > "claude-sonnet-4-5").
func compareModelVersions(left, right string) int {
	lv := modelVersionNumbers(left)
	rv := modelVersionNumbers(right)
	if len(lv) == 0 || len(rv) == 0 {
		return 0
	}
	maxLen := len(lv)
	if len(rv) > maxLen {
		maxLen = len(rv)
	}
	for i := 0; i < maxLen; i++ {
		l, r := 0, 0
		if i < len(lv) {
			l = lv[i]
		}
		if i < len(rv) {
			r = rv[i]
		}
		if l != r {
			return l - r
		}
	}
	return 0
}

func modelVersionNumbers(id string) []int {
	id = StripProviderPrefix(id)
	parts := strings.Split(strings.ToLower(id), "-")
	var out []int
	for _, part := range parts {
		if !isModelVersionToken(part) {
			continue
		}
		for _, piece := range strings.Split(part, ".") {
			if piece == "" {
				continue
			}
			n := 0
			for _, r := range piece {
				n = n*10 + int(r-'0')
			}
			out = append(out, n)
		}
	}
	return out
}

func isModelVersionToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}
