package ai

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

type selectorFamily string

const (
	selectorFamilyClaude   selectorFamily = "claude"
	selectorFamilyCodex    selectorFamily = "codex"
	selectorFamilyGemini   selectorFamily = "gemini"
	selectorFamilyDeepSeek selectorFamily = "deepseek"
)

// ContainsRuntimeSelector reports whether s contains a backend/mode selector such
// as "cmux:gpt-5.5" or "*:sonnet-5". Comma-separated fallback lists are scanned
// item by item.
func ContainsRuntimeSelector(s string) bool {
	for _, part := range splitSelectorCSV(s) {
		prefix, _, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		if isSelectorPrefix(strings.TrimSpace(prefix)) {
			return true
		}
	}
	return false
}

// ResolveModelSelectors normalizes selector-prefixed model names on a single
// api.Model, preserving the model's fallback semantics.
func ResolveModelSelectors(model api.Model) (api.Model, error) {
	model = model.ExpandCSV()
	resolved, err := resolveSelectorModel(model, "", false)
	if err != nil {
		return api.Model{}, err
	}
	out := mergeModelRuntime(model, resolved)
	out.Fallbacks = make([]api.Model, 0, len(model.Fallbacks))
	for _, fb := range model.Fallbacks {
		rfb, err := resolveSelectorModel(fb, "", false)
		if err != nil {
			return api.Model{}, err
		}
		out.Fallbacks = append(out.Fallbacks, mergeModelRuntime(fb, rfb))
	}
	return out, nil
}

// ResolveRuntimeSelectors expands --multi-models values into concrete runtime
// model/backend pairs. Values may be repeated and/or comma-separated.
func ResolveRuntimeSelectors(values []string, base api.Model) ([]api.Model, error) {
	base, err := ResolveModelSelectors(base)
	if err != nil {
		return nil, err
	}
	parts := splitSelectorValues(values)
	out := make([]api.Model, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		models, err := resolveSelectorPart(part, base.Name, "", true)
		if err != nil {
			return nil, err
		}
		for _, m := range models {
			m.Temperature = base.Temperature
			m.Effort = base.Effort
			m.NoCache = base.NoCache
			key := string(m.Backend) + "\x00" + m.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, m)
		}
	}
	return out, nil
}

func resolveSelectorModel(model api.Model, baseName string, allowWildcard bool) (api.Model, error) {
	if strings.TrimSpace(model.Name) == "" {
		return model, nil
	}
	resolved, err := resolveSelectorPart(model.Name, baseName, model.Backend, allowWildcard)
	if err != nil {
		return api.Model{}, err
	}
	if len(resolved) != 1 {
		return api.Model{}, fmt.Errorf("wildcard selector %q is only valid for --multi-models", model.Name)
	}
	return resolved[0], nil
}

func resolveSelectorPart(raw, baseName string, forced api.Backend, allowWildcard bool) ([]api.Model, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty runtime selector")
	}

	prefix, modelName, hasPrefix := strings.Cut(raw, ":")
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	modelName = strings.TrimSpace(modelName)
	if !hasPrefix {
		if isSelectorPrefix(raw) {
			prefix = strings.ToLower(raw)
			modelName = strings.TrimSpace(baseName)
			hasPrefix = true
		} else {
			return []api.Model{{Name: raw, Backend: forced}}, nil
		}
	}
	if !isSelectorPrefix(prefix) {
		return nil, fmt.Errorf("unknown runtime selector prefix %q in %q", prefix, raw)
	}
	if modelName == "" {
		modelName = strings.TrimSpace(baseName)
	}
	if modelName == "" {
		return nil, fmt.Errorf("runtime selector %q needs a model, or --model must provide a base model", raw)
	}
	if prefix == "*" {
		if !allowWildcard {
			return nil, fmt.Errorf("wildcard selector %q is only valid for --multi-models", raw)
		}
		family, err := selectorModelFamily(modelName)
		if err != nil {
			return nil, err
		}
		backends := wildcardBackends(family)
		out := make([]api.Model, 0, len(backends))
		for _, backend := range backends {
			out = append(out, api.Model{Name: normalizeSelectorModel(backend, modelName), Backend: backend})
		}
		return out, nil
	}

	backend, err := selectorBackend(prefix, modelName)
	if err != nil {
		return nil, err
	}
	if forced != "" && backend != forced {
		return nil, fmt.Errorf("runtime selector %q resolves to backend %q but --backend is %q", raw, backend, forced)
	}
	return []api.Model{{Name: normalizeSelectorModel(backend, modelName), Backend: backend}}, nil
}

func selectorBackend(prefix, model string) (api.Backend, error) {
	if b := api.Backend(prefix); b.Valid() {
		return b, nil
	}
	family, err := selectorModelFamily(model)
	if err != nil {
		return "", err
	}
	switch prefix {
	case "api":
		switch family {
		case selectorFamilyClaude:
			return api.BackendAnthropic, nil
		case selectorFamilyCodex:
			return api.BackendOpenAI, nil
		case selectorFamilyGemini:
			return api.BackendGemini, nil
		case selectorFamilyDeepSeek:
			return api.BackendDeepSeek, nil
		}
	case "cli":
		switch family {
		case selectorFamilyClaude:
			return api.BackendClaudeCLI, nil
		case selectorFamilyCodex:
			return api.BackendCodexCLI, nil
		case selectorFamilyGemini:
			return api.BackendGeminiCLI, nil
		}
	case "agent", "sdk":
		switch family {
		case selectorFamilyClaude:
			return api.BackendClaudeAgent, nil
		case selectorFamilyCodex:
			return api.BackendCodexAgent, nil
		}
	case "cmux":
		switch family {
		case selectorFamilyClaude:
			return api.BackendClaudeCmux, nil
		case selectorFamilyCodex:
			return api.BackendCodexCmux, nil
		}
	}
	return "", fmt.Errorf("runtime selector %q does not support %s models", prefix, family)
}

func selectorModelFamily(model string) (selectorFamily, error) {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "claude-"),
		strings.HasPrefix(m, "claude-agent-"),
		strings.HasPrefix(m, "claude-code-"),
		strings.HasPrefix(m, "opus"),
		strings.HasPrefix(m, "sonnet"),
		strings.HasPrefix(m, "haiku"),
		strings.HasPrefix(m, "fable"):
		return selectorFamilyClaude, nil
	case strings.HasPrefix(m, "gemini-"), strings.HasPrefix(m, "models/gemini-"):
		return selectorFamilyGemini, nil
	case strings.HasPrefix(m, "deepseek"):
		return selectorFamilyDeepSeek, nil
	case strings.HasPrefix(m, "codex"),
		strings.HasPrefix(m, "gpt-"),
		strings.HasPrefix(m, "o1"),
		strings.HasPrefix(m, "o3"),
		strings.HasPrefix(m, "o4"):
		return selectorFamilyCodex, nil
	default:
		if b, err := api.InferBackend(model); err == nil {
			switch b {
			case api.BackendAnthropic, api.BackendClaudeAgent, api.BackendClaudeCLI, api.BackendClaudeCmux:
				return selectorFamilyClaude, nil
			case api.BackendGemini, api.BackendGeminiCLI:
				return selectorFamilyGemini, nil
			case api.BackendDeepSeek:
				return selectorFamilyDeepSeek, nil
			case api.BackendOpenAI, api.BackendCodexCLI, api.BackendCodexAgent, api.BackendCodexCmux:
				return selectorFamilyCodex, nil
			}
		}
		return "", fmt.Errorf("cannot infer model family for %q", model)
	}
}

func wildcardBackends(family selectorFamily) []api.Backend {
	switch family {
	case selectorFamilyClaude:
		return []api.Backend{api.BackendAnthropic, api.BackendClaudeAgent, api.BackendClaudeCLI, api.BackendClaudeCmux}
	case selectorFamilyCodex:
		return []api.Backend{api.BackendOpenAI, api.BackendCodexAgent, api.BackendCodexCLI, api.BackendCodexCmux}
	case selectorFamilyGemini:
		return []api.Backend{api.BackendGemini, api.BackendGeminiCLI}
	case selectorFamilyDeepSeek:
		return []api.Backend{api.BackendDeepSeek}
	default:
		return nil
	}
}

func normalizeSelectorModel(backend api.Backend, model string) string {
	return NormalizeModelForBackend(backend, model)
}

// NormalizeModelForBackend maps a catalog/live/provider model id onto the exact
// runtime id accepted by backend. Provider prefixes and old backend-specific
// aliases are input compatibility only; the returned value is never a family
// alias such as "opus" or a synthetic id such as "claude-agent-opus".
func NormalizeModelForBackend(backend api.Backend, model string) string {
	resolved, _ := ResolveExactModelForBackend(backend, model)
	return resolved
}

func lookupSelectorCatalogModel(backend api.Backend, model string) (string, bool) {
	want := selectorCatalogBackend(backend)
	for _, m := range Catalog() {
		if m.Backend != want {
			continue
		}
		if !selectorCatalogMatch(m, model, backend) {
			continue
		}
		if m.IsAgent() {
			if m.AgentModel != "" {
				return m.AgentModel, true
			}
			return m.ID, true
		}
		return m.BareID(), true
	}
	return "", false
}

func selectorCatalogBackend(backend api.Backend) api.Backend {
	switch backend {
	case api.BackendClaudeCLI, api.BackendClaudeCmux:
		return api.BackendClaudeAgent
	case api.BackendCodexCLI, api.BackendCodexCmux:
		return api.BackendCodexAgent
	default:
		return backend
	}
}

func selectorCatalogMatch(m Model, model string, backend api.Backend) bool {
	needle := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(needle, "/"); i >= 0 {
		needle = needle[i+1:]
	}
	candidates := []string{m.ID, m.BareID(), m.AgentModel}
	for _, c := range candidates {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if c == needle {
			return true
		}
		if i := strings.LastIndex(c, "/"); i >= 0 && c[i+1:] == needle {
			return true
		}
	}
	catalogBackend := selectorCatalogBackend(backend)
	if catalogBackend == api.BackendClaudeAgent || backend == api.BackendAnthropic {
		if tier := claudeTier(needle); tier != "" && tier == claudeTier(m.ID) {
			if catalogBackend == api.BackendClaudeAgent {
				return true
			}
			if strings.Contains(needle, "-") {
				return strings.Contains(strings.ToLower(m.ID), needle)
			}
			return true
		}
	}
	return false
}

func claudeTier(model string) string {
	m := strings.ToLower(model)
	for _, tier := range []string{"fable", "opus", "sonnet", "haiku"} {
		if strings.Contains(m, tier) {
			return tier
		}
	}
	return ""
}

func mergeModelRuntime(original, resolved api.Model) api.Model {
	out := original
	if strings.TrimSpace(resolved.Name) != "" {
		out.Name = resolved.Name
	}
	if resolved.ID != "" {
		out.ID = resolved.ID
	}
	if resolved.Backend != "" {
		out.Backend = resolved.Backend
	}
	return out
}

func isSelectorPrefix(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "api", "agent", "cli", "sdk", "cmux", "*":
		return true
	default:
		return api.Backend(s).Valid()
	}
}

func splitSelectorValues(values []string) []string {
	var out []string
	for _, value := range values {
		out = append(out, splitSelectorCSV(value)...)
	}
	return out
}

func splitSelectorCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
