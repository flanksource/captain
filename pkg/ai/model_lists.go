package ai

import (
	"sort"
	"strings"
	"time"
)

const currentModelsPerFamily = 3

// legacyModelPrefixes hides model IDs that are either superseded by a newer
// generation or aren't chat completions (image/audio/embedding/moderation).
var legacyModelPrefixes = []string{
	// OpenAI legacy
	"gpt-3",
	"gpt-4",  // covers gpt-4, gpt-4o, gpt-4.1, gpt-4-turbo, ...
	"gpt-5-", // API variants like mini/nano/codex/pro; CLI Codex is exempt by backend
	"o1",
	"o3",
	"codex-mini",
	// OpenAI non-chat endpoints
	"dall-",
	"whisper",
	"tts-",
	"text-embedding",
	"text-moderation",
	"omni-moderation",
	"babbage",
	"davinci",
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
	for _, p := range legacyModelPrefixes {
		if strings.HasPrefix(idLower, p) {
			return true
		}
	}
	return false
}

// IsLegacyModelIDForBackend keeps API model menus clean while preserving local
// agent model slugs such as gpt-5-codex, which are current for Codex CLI even
// though the same id would be noisy in an OpenAI API model listing.
func IsLegacyModelIDForBackend(id string, backend Backend) bool {
	if backend.Kind() == "cli" {
		return false
	}
	return IsLegacyModelID(id)
}

// CurrentModelsByReleaseDate returns a filtered copy sorted newest first,
// retaining the newest few models per family prefix. Known catalog release
// dates fill gaps left by provider list endpoints.
func CurrentModelsByReleaseDate(models []ModelDef) []ModelDef {
	out := make([]ModelDef, 0, len(models))
	for _, m := range models {
		if IsLegacyModelIDForBackend(m.ID, m.Backend) {
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
