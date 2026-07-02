package ai

import (
	"sort"
	"strings"
	"time"
)

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

// CurrentModelsByReleaseDate returns a filtered copy sorted newest first. Known
// catalog release dates fill gaps left by provider list endpoints.
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
	return out
}

// SortModelsByReleaseDateDesc sorts in-place by release date descending, with
// unknown dates last and id descending as the stable deterministic tie-breaker.
func SortModelsByReleaseDateDesc(models []ModelDef) {
	sort.SliceStable(models, func(i, j int) bool {
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
