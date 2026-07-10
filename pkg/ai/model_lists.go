package ai

import (
	"sort"
	"strings"
	"time"
)

const currentModelsPerFamily = 3

// legacyModelPrefixes hides model IDs that are either superseded by a newer
// generation or aren't primary text models.
var legacyModelPrefixes = []string{
	// OpenAI legacy
	"gpt-3",
	"gpt-4",  // covers gpt-4, gpt-4o, gpt-4.1, gpt-4-turbo, ...
	"gpt-5-", // API variants like mini/nano/codex/pro; CLI Codex is exempt by backend
	"o1",
	"o3",
	"o4",
	"codex-mini",
	// OpenAI non-primary endpoints and aliases
	"gpt-realtime",
	"gpt-image",
	"gpt-audio",
	"sora",
	"dall-",
	"image-",
	"audio-",
	"whisper",
	"tts-",
	"text-embedding",
	"text-moderation",
	"omni-moderation",
	"babbage",
	"davinci",
	"chat-latest",
	"chatgpt-",
	"computer-use-preview",
	// Claude legacy
	"claude-3",
	"claude-2",
	"claude-instant",
	"claude-sonnet-4-0",
	"claude-sonnet-4-2",
	"claude-opus-4-0",
	"claude-opus-4-1",
	"fable-",
	"opus-",
	"sonnet-",
	"haiku-",
	// Gemini legacy
	"gemini-1",
	"gemini-2.0",
	// Grok legacy
	"grok-3",
	"grok-code-fast-1",
}

// IsLegacyModelID reports whether id is a known legacy, outdated, or non-chat
// model id. Call IsLegacyModelIDForBackend when backend context is available.
func IsLegacyModelID(id string) bool {
	idLower := strings.ToLower(bareModelID(strings.TrimPrefix(strings.TrimSpace(id), "models/")))
	if IsIgnoredOpenAIModelID(idLower) {
		return true
	}
	for _, p := range legacyModelPrefixes {
		if strings.HasPrefix(idLower, p) {
			return true
		}
	}
	return false
}

// IsIgnoredOpenAIModelID reports whether an OpenAI model-list id should be
// hidden from ordinary model pickers. OpenAI exposes many non-primary surfaces
// (realtime, audio, image, Sora, code/chat aliases, dated and size variants);
// Captain's picker keeps stable primary GPT text ids and explicitly registered
// Codex runtime variants. Use this before remapping live OpenAI ids onto Codex
// backends.
func IsIgnoredOpenAIModelID(id string) bool {
	idLower := strings.ToLower(bareModelID(strings.TrimPrefix(strings.TrimSpace(id), "models/")))
	if idLower == "" {
		return false
	}
	for _, p := range legacyModelPrefixes {
		if strings.HasPrefix(idLower, p) {
			return true
		}
	}
	if strings.HasPrefix(idLower, "gpt-") {
		return !isPrimaryGPTModelID(idLower)
	}
	for _, needle := range []string{"realtime", "image", "audio", "whisper", "tts", "sora", "codex", "code", "chat-latest", "chatgpt", "computer-use"} {
		if strings.Contains(idLower, needle) {
			return true
		}
	}
	return false
}

func isPrimaryGPTModelID(id string) bool {
	version := strings.TrimPrefix(id, "gpt-")
	if version == "" || version == id {
		return false
	}
	sawDigit := false
	for _, r := range version {
		switch {
		case r >= '0' && r <= '9':
			sawDigit = true
		case r == '.':
			continue
		default:
			return false
		}
	}
	return sawDigit
}

// IsLegacyModelIDForBackend keeps model menus clean for every backend. CLI and
// agent backends receive exact provider IDs too, so code/chat/realtime/audio
// variants are hidden there just as they are for direct API backends.
func IsLegacyModelIDForBackend(id string, backend Backend) bool {
	if backend == BackendCodexAgent || backend == BackendCodexCLI || backend == BackendCodexCmux {
		if _, ok := RegistryModelDef(backend, id); ok {
			return false
		}
	}
	return IsLegacyModelID(id)
}

// CurrentModelsByReleaseDate returns a filtered copy sorted newest first,
// retaining the newest few models per family prefix. Known catalog release
// dates fill gaps left by provider list endpoints.
func CurrentModelsByReleaseDate(models []ModelDef) []ModelDef {
	return currentModelsByReleaseDate(models, true)
}

// CurrentCuratedModelsByReleaseDate sorts and limits a trusted runtime catalog
// whose own visibility field has already removed hidden models. It deliberately
// skips the generic OpenAI variant blacklist used for raw provider listings.
func CurrentCuratedModelsByReleaseDate(models []ModelDef) []ModelDef {
	return currentModelsByReleaseDate(models, false)
}

func currentModelsByReleaseDate(models []ModelDef, filterLegacy bool) []ModelDef {
	out := make([]ModelDef, 0, len(models))
	for _, m := range models {
		if filterLegacy && IsLegacyModelIDForBackend(m.ID, m.Backend) {
			continue
		}
		if m.ReleaseDate == "" {
			m.ReleaseDate = CatalogReleaseDate(m.Backend, m.ID)
		}
		out = append(out, m)
	}
	SortModelsByReleaseDateDesc(out)
	return limitModelsPerFamily(out, currentModelsPerFamily)
}

// SortModelsByReleaseDateDesc sorts in-place by release date descending, with
// unknown dates last and id descending as the stable deterministic tie-breaker.
func SortModelsByReleaseDateDesc(models []ModelDef) {
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Priority != models[j].Priority && (models[i].Priority > 0 || models[j].Priority > 0) {
			if models[i].Priority == 0 {
				return false
			}
			if models[j].Priority == 0 {
				return true
			}
			return models[i].Priority < models[j].Priority
		}
		if ModelFamilyPrefix(models[i].ID) == ModelFamilyPrefix(models[j].ID) {
			if cmp := compareModelVersions(models[i].ID, models[j].ID); cmp != 0 {
				return cmp > 0
			}
		}
		left := models[i].ReleaseDate
		if left == "" {
			left = CatalogReleaseDate(models[i].Backend, models[i].ID)
		}
		right := models[j].ReleaseDate
		if right == "" {
			right = CatalogReleaseDate(models[j].Backend, models[j].ID)
		}
		if left == "" && right == "" {
			return models[i].ID > models[j].ID
		}
		if left == "" {
			return false
		}
		if right == "" {
			return true
		}
		if left == right {
			return models[i].ID > models[j].ID
		}
		return left > right
	})
}

// CatalogReleaseDate returns the catalog release date for a backend/model id
// when known. API callers may pass either provider-prefixed or bare ids.
func CatalogReleaseDate(backend Backend, id string) string {
	id = strings.TrimPrefix(strings.TrimSpace(id), "models/")
	bare := bareModelID(id)
	for _, m := range Catalog() {
		if m.Backend != backend {
			continue
		}
		switch {
		case m.ID == id, m.BareID() == bare, m.AgentModel == id, m.AgentModel == bare:
			return m.ReleaseDate
		}
	}
	return ""
}

// ModelFamilyPrefix groups versioned model ids by tier so recent entries from
// each tier survive filtering, e.g. claude-haiku, claude-sonnet, gemini-pro.
func ModelFamilyPrefix(id string) string {
	id = strings.TrimPrefix(strings.TrimSpace(id), "models/")
	id = bareModelID(id)
	parts := strings.Split(strings.ToLower(id), "-")
	if len(parts) < 2 {
		return strings.ToLower(id)
	}

	switch parts[0] {
	case "claude":
		if len(parts) >= 3 && parts[1] == "agent" {
			return strings.Join(parts[:3], "-")
		}
		return "claude-" + parts[1]
	case "fable", "opus", "sonnet", "haiku":
		return parts[0]
	case "gemini":
		for i := 1; i < len(parts); i++ {
			if isModelVersionToken(parts[i]) {
				continue
			}
			if parts[i] == "flash" && i+1 < len(parts) && parts[i+1] == "lite" {
				return "gemini-flash-lite"
			}
			return "gemini-" + parts[i]
		}
		return "gemini"
	case "gpt":
		if len(parts) >= 3 {
			return "gpt-" + parts[2]
		}
		return "gpt"
	case "grok":
		return "grok-" + parts[1]
	default:
		if strings.HasPrefix(parts[0], "o") && len(parts) >= 2 {
			return parts[0] + "-" + parts[1]
		}
		return strings.Join(parts[:2], "-")
	}
}

func limitModelsPerFamily(models []ModelDef, limit int) []ModelDef {
	if limit <= 0 {
		out := make([]ModelDef, len(models))
		copy(out, models)
		return out
	}
	counts := map[string]int{}
	out := make([]ModelDef, 0, len(models))
	for _, model := range models {
		family := ModelFamilyPrefix(model.ID)
		if counts[family] >= limit {
			continue
		}
		counts[family]++
		out = append(out, model)
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

func compareModelVersions(left, right string) int {
	lv := modelVersion(left)
	rv := modelVersion(right)
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

func modelVersion(id string) []int {
	id = bareModelID(strings.TrimPrefix(strings.TrimSpace(id), "models/"))
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

func normalizeReleaseDate(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < len("2006-01-02") {
		return ""
	}
	date := value[:len("2006-01-02")]
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return ""
	}
	return date
}
