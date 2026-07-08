package ai

import (
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

// ModelIdentity is Captain's parsed model key. It is intentionally provider
// native: aliases and backend prefixes are input conveniences only, never the
// value sent to a backend.
type ModelIdentity struct {
	Provider string
	Family   string
	Version  string
}

type registryModel struct {
	ID            string
	Provider      string
	Family        string
	Version       string
	Label         string
	ReleaseDate   string
	Reasoning     bool
	ContextWindow int
	Preferred     bool
}

const (
	modelProviderAnthropic = "anthropic"
	modelProviderOpenAI    = "openai"
	modelProviderGoogle    = "google"
	modelProviderDeepSeek  = "deepseek"
)

func registryProviderForBackend(backend api.Backend) string {
	switch backend {
	case api.BackendAnthropic, api.BackendClaudeAgent, api.BackendClaudeCLI, api.BackendClaudeCmux:
		return modelProviderAnthropic
	case api.BackendOpenAI, api.BackendCodexAgent, api.BackendCodexCLI, api.BackendCodexCmux:
		return modelProviderOpenAI
	case api.BackendGemini, api.BackendGeminiCLI:
		return modelProviderGoogle
	case api.BackendDeepSeek:
		return modelProviderDeepSeek
	default:
		return ""
	}
}

func registryBackendForProvider(provider string) Backend {
	switch provider {
	case modelProviderAnthropic:
		return BackendAnthropic
	case modelProviderOpenAI:
		return BackendOpenAI
	case modelProviderGoogle:
		return BackendGemini
	case modelProviderDeepSeek:
		return BackendDeepSeek
	default:
		return ""
	}
}

func registryProviderPrefix(provider string) string {
	switch provider {
	case modelProviderGoogle:
		return "googleai"
	default:
		return provider
	}
}

func registryCatalogModels() []Model {
	out := make([]Model, 0, len(exactModelRegistry)+5)
	for _, m := range exactModelRegistry {
		if !m.Preferred {
			continue
		}
		backend := registryBackendForProvider(m.Provider)
		if backend == "" {
			continue
		}
		out = append(out, Model{
			ID:            registryProviderPrefix(m.Provider) + "/" + m.ID,
			Backend:       backend,
			Label:         m.Label,
			Reasoning:     m.Reasoning,
			ContextWindow: m.ContextWindow,
			ReleaseDate:   m.ReleaseDate,
			Default:       m.Provider == modelProviderAnthropic && m.ID == "claude-sonnet-5",
		})
	}
	for _, m := range exactModelRegistry {
		if !m.Preferred {
			continue
		}
		switch m.Provider {
		case modelProviderAnthropic:
			if m.Family == "fable" {
				continue
			}
			out = append(out, Model{
				ID:            m.ID,
				Backend:       BackendClaudeAgent,
				Label:         "Claude Agent · " + strings.TrimPrefix(m.Label, "Claude "),
				Reasoning:     m.Reasoning,
				ContextWindow: m.ContextWindow,
				ReleaseDate:   m.ReleaseDate,
			})
		case modelProviderOpenAI:
			out = append(out, Model{
				ID:            m.ID,
				Backend:       BackendCodexAgent,
				Label:         "Codex Agent · " + m.Label,
				Reasoning:     m.Reasoning,
				ContextWindow: m.ContextWindow,
				ReleaseDate:   m.ReleaseDate,
			})
		}
	}
	return out
}

// RegistryModelDefs returns exact, provider-native model IDs for a backend. CLI
// and cmux backends are projected from their parent provider's model registry.
func RegistryModelDefs(backend Backend) []ModelDef {
	provider := registryProviderForBackend(backend)
	if provider == "" {
		return nil
	}
	out := make([]ModelDef, 0, len(exactModelRegistry))
	for _, m := range exactModelRegistry {
		if m.Provider != provider || !m.Preferred {
			continue
		}
		out = append(out, ModelDef{ID: m.ID, Name: m.Label, Backend: backend, ReleaseDate: m.ReleaseDate})
	}
	SortModelsByReleaseDateDesc(out)
	return out
}

// ResolveExactModelForBackend resolves a user/catalog model token into the exact
// model ID the selected backend should receive. It accepts old aliases for input
// compatibility but never returns an alias.
func ResolveExactModelForBackend(backend Backend, model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	if strings.EqualFold(model, backendAgentSentinel(backend)) {
		if m, ok := latestRegistryModel(registryProviderForBackend(backend), ""); ok {
			return m.ID, true
		}
		return model, false
	}
	provider := registryProviderForBackend(backend)
	if provider == "" {
		return stripModelProviderPrefix(model), false
	}
	if m, ok := lookupRegistryExact(provider, model); ok {
		return m.ID, true
	}
	identity, ok := ParseModelIdentity(provider, model)
	if !ok {
		return stripModelProviderPrefix(model), false
	}
	if identity.Provider != provider {
		return stripModelProviderPrefix(model), false
	}
	if m, ok := resolveRegistryIdentity(identity); ok {
		return m.ID, true
	}
	if m, ok := latestRegistryModelForVersionLine(identity); ok {
		return m.ID, true
	}
	if m, ok := latestRegistryModel(identity.Provider, identity.Family); ok {
		return m.ID, true
	}
	return stripModelProviderPrefix(model), false
}

func backendAgentSentinel(backend Backend) string {
	switch backend {
	case BackendClaudeAgent, BackendClaudeCLI, BackendClaudeCmux:
		return "claude"
	case BackendCodexAgent, BackendCodexCLI, BackendCodexCmux:
		return "codex"
	default:
		return ""
	}
}

func lookupRegistryExact(provider, model string) (registryModel, bool) {
	needle := canonicalModelToken(model)
	for _, m := range exactModelRegistry {
		if m.Provider != provider {
			continue
		}
		if canonicalModelToken(m.ID) == needle {
			return m, true
		}
	}
	return registryModel{}, false
}

// ParseModelIdentity parses a model token into the provider/family/version tuple
// used by the resolver. It is deliberately permissive on input prefixes.
func ParseModelIdentity(defaultProvider, model string) (ModelIdentity, bool) {
	provider, token := splitModelProvider(defaultProvider, model)
	token = canonicalModelToken(token)
	switch provider {
	case modelProviderAnthropic:
		token = strings.TrimPrefix(token, "claude-agent-")
		token = strings.TrimPrefix(token, "claude-code-")
		token = strings.TrimPrefix(token, "claude-")
		if token == "" {
			return ModelIdentity{Provider: provider, Family: "sonnet"}, true
		}
		family, version, ok := splitKnownFamily(token, []string{"fable", "opus", "sonnet", "haiku"})
		return ModelIdentity{Provider: provider, Family: family, Version: version}, ok
	case modelProviderOpenAI:
		token = strings.TrimPrefix(token, "codex-agent-")
		token = strings.TrimPrefix(token, "codex-")
		if token == "" || token == "codex" {
			return ModelIdentity{Provider: provider, Family: "gpt"}, true
		}
		family, version, ok := splitKnownFamily(token, []string{"gpt"})
		return ModelIdentity{Provider: provider, Family: family, Version: version}, ok
	case modelProviderGoogle:
		token = strings.TrimPrefix(token, "gemini-cli-")
		if token == "gemini" {
			return ModelIdentity{Provider: provider, Family: "gemini"}, true
		}
		family, version, ok := splitKnownFamily(token, []string{"gemini"})
		return ModelIdentity{Provider: provider, Family: family, Version: version}, ok
	case modelProviderDeepSeek:
		if strings.HasPrefix(token, "deepseek") {
			version := strings.TrimPrefix(token, "deepseek-")
			if version == token {
				version = ""
			}
			return ModelIdentity{Provider: provider, Family: "deepseek", Version: normalizeModelVersion(version)}, true
		}
	}
	return ModelIdentity{}, false
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

func resolveRegistryIdentity(identity ModelIdentity) (registryModel, bool) {
	candidates := make([]registryModel, 0)
	for _, m := range exactModelRegistry {
		if !m.Preferred || m.Provider != identity.Provider || m.Family != identity.Family {
			continue
		}
		if identity.Version == "" || modelVersionMatches(m.Version, identity.Version) {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return registryModel{}, false
	}
	sortRegistryModels(candidates)
	return candidates[0], true
}

func latestRegistryModel(provider, family string) (registryModel, bool) {
	candidates := make([]registryModel, 0)
	for _, m := range exactModelRegistry {
		if !m.Preferred || m.Provider != provider {
			continue
		}
		if family != "" && m.Family != family {
			continue
		}
		candidates = append(candidates, m)
	}
	if len(candidates) == 0 {
		return registryModel{}, false
	}
	sortRegistryModels(candidates)
	return candidates[0], true
}

func latestRegistryModelForVersionLine(identity ModelIdentity) (registryModel, bool) {
	if identity.Version == "" {
		return registryModel{}, false
	}
	major := strings.Split(normalizeModelVersion(identity.Version), ".")[0]
	if major == "" {
		return registryModel{}, false
	}
	candidates := make([]registryModel, 0)
	for _, m := range exactModelRegistry {
		if !m.Preferred || m.Provider != identity.Provider || m.Family != identity.Family {
			continue
		}
		if modelVersionMatches(m.Version, major) {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return registryModel{}, false
	}
	sortRegistryModels(candidates)
	return candidates[0], true
}

func sortRegistryModels(models []registryModel) {
	sort.SliceStable(models, func(i, j int) bool {
		left, right := models[i], models[j]
		if left.ReleaseDate == right.ReleaseDate {
			return compareModelVersions(left.ID, right.ID) > 0
		}
		return left.ReleaseDate > right.ReleaseDate
	})
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

func splitModelProvider(defaultProvider, model string) (string, string) {
	model = strings.TrimSpace(model)
	if i := strings.IndexByte(model, '/'); i >= 0 {
		prefix := strings.ToLower(strings.TrimSpace(model[:i]))
		token := model[i+1:]
		switch prefix {
		case "anthropic":
			return modelProviderAnthropic, token
		case "openai":
			return modelProviderOpenAI, token
		case "google", "googleai":
			return modelProviderGoogle, token
		case "deepseek":
			return modelProviderDeepSeek, token
		}
	}
	token := canonicalModelToken(model)
	switch {
	case strings.HasPrefix(token, "claude-") ||
		strings.HasPrefix(token, "opus") ||
		strings.HasPrefix(token, "sonnet") ||
		strings.HasPrefix(token, "haiku") ||
		strings.HasPrefix(token, "fable"):
		return modelProviderAnthropic, model
	case strings.HasPrefix(token, "codex-") ||
		strings.HasPrefix(token, "gpt-") ||
		token == "gpt" ||
		strings.HasPrefix(token, "o1") ||
		strings.HasPrefix(token, "o3") ||
		strings.HasPrefix(token, "o4") ||
		strings.HasPrefix(token, "sora"):
		return modelProviderOpenAI, model
	case strings.HasPrefix(token, "gemini"):
		return modelProviderGoogle, model
	case strings.HasPrefix(token, "deepseek"):
		return modelProviderDeepSeek, model
	}
	return defaultProvider, model
}

func stripModelProviderPrefix(model string) string {
	_, token := splitModelProvider("", model)
	return strings.TrimPrefix(strings.TrimSpace(token), "models/")
}

func canonicalModelToken(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimPrefix(model, "models/")
	model = strings.ReplaceAll(model, "_", "-")
	model = strings.ReplaceAll(model, " ", "-")
	return model
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
	if isRegistryNumericVersionPart(parts[0]) {
		out = parts[0]
		i = 1
		if len(parts) > 1 && isRegistryNumericVersionPart(parts[1]) {
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

func isRegistryNumericVersionPart(part string) bool {
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
