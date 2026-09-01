package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	captainai "github.com/flanksource/captain/pkg/ai"
)

const modelsDevAPIURL = "https://models.dev/api.json"

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	Family           string                     `json:"family"`
	Reasoning        bool                       `json:"reasoning"`
	Temperature      bool                       `json:"temperature"`
	ReasoningOptions []modelsDevReasoningOption `json:"reasoning_options"`
	ReleaseDate      string                     `json:"release_date"`
	Modalities       modelsDevModalities        `json:"modalities"`
	Limit            modelsDevLimit             `json:"limit"`
	Cost             modelsDevCost              `json:"cost"`
}

// modelsDevCost is a model's published list price in USD per million tokens.
// The upstream block also carries tiered pricing (e.g. OpenAI's >200k-context
// rates) and per-modality audio rates; captain prices a single flat tier, so
// only the base four are read.
type modelsDevCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// modelsDevReasoningOption is one entry of a model's reasoning_options; only its
// type ("effort", "budget_tokens", "toggle", …) is needed to classify how the
// model controls thinking.
type modelsDevReasoningOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

type modelsDevModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type modelsDevLimit struct {
	Context int `json:"context"`
}

// generatedModel mirrors pkg/ai.registryModel field-for-field (and its JSON
// tags), so the emitted JSON round-trips into the registry on load.
type generatedModel struct {
	ID               string   `json:"id"`
	Provider         string   `json:"provider"`
	Family           string   `json:"family"`
	Version          string   `json:"version"`
	Label            string   `json:"label"`
	ReleaseDate      string   `json:"releaseDate,omitempty"`
	Reasoning        bool     `json:"reasoning,omitempty"`
	Temperature      bool     `json:"temperature,omitempty"`
	ContextWindow    int      `json:"contextWindow,omitempty"`
	Cost             *cost    `json:"cost,omitempty"`
	InputMediaTypes  []string `json:"inputMediaTypes,omitempty"`
	Preferred        bool     `json:"preferred,omitempty"`
	AdaptiveThinking bool     `json:"adaptiveThinking,omitempty"`
	Availability     []string `json:"availability,omitempty"`
	SupportedEfforts []string `json:"supportedEfforts,omitempty"`
	DefaultEffort    string   `json:"defaultEffort,omitempty"`
	Priority         int      `json:"priority,omitempty"`

	// Aliases and SupersededBy are patch-only: models.dev has no concept of
	// either. They carry the codename and retired-id knowledge that used to be
	// hardcoded in pkg/ai, where the spec decoder could not see it.
	Aliases      []string `json:"aliases,omitempty"`
	SupersededBy string   `json:"supersededBy,omitempty"`
}

// cost mirrors pkg/api/registry.ModelCost: USD per million tokens. It is a
// pointer on generatedModel so a model models.dev prices at nothing at all
// (and a patch-only entry that names no cost) omits the block entirely rather
// than claiming a free model.
type cost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
}

// deriveCost carries models.dev's list price into the catalog so pricing has a
// per-model, per-version source of truth. Captain used to price Claude from a
// hand-written family table (any "opus" id → $15/$75), which silently drifted
// three-fold once Opus 4.5 cut prices; sourcing it here means `task
// models:update` corrects prices along with everything else.
func deriveCost(model modelsDevModel) *cost {
	c := model.Cost
	if c.Input == 0 && c.Output == 0 {
		return nil
	}
	return &cost{Input: c.Input, Output: c.Output, CacheRead: c.CacheRead, CacheWrite: c.CacheWrite}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gen-model-registry: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var output string
	var source string
	var patchPath string
	flag.StringVar(&output, "output", "", "Path to write pkg/api/registry/models.json")
	flag.StringVar(&source, "source", modelsDevAPIURL, "models.dev API URL, local file path, or - for stdin")
	flag.StringVar(&patchPath, "patches", "", "Path to the per-model JSON merge-patch file")
	flag.Parse()
	if output == "" {
		return fmt.Errorf("--output is required")
	}

	data, err := readSource(source)
	if err != nil {
		return err
	}
	patches, err := readPatches(patchPath)
	if err != nil {
		return err
	}
	models, err := generateModels(data, patches)
	if err != nil {
		return err
	}
	src, err := renderJSON(models)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(output); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	if err := os.WriteFile(output, src, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", output, err)
	}
	return nil
}

// readPatches loads the per-model merge-patch file: a JSON object keyed by model
// ID whose values carry only the fields to override on the fetched row (or a
// full row for models not present in the models.dev catalog).
func readPatches(path string) (map[string]json.RawMessage, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read patches %s: %w", path, err)
	}
	var patches map[string]json.RawMessage
	if err := json.Unmarshal(data, &patches); err != nil {
		return nil, fmt.Errorf("parse patches %s: %w", path, err)
	}
	return patches, nil
}

func readSource(source string) ([]byte, error) {
	switch {
	case source == "-":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return data, nil
	case strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://"):
		return readURL(source)
	default:
		data, err := os.ReadFile(source)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", source, err)
		}
		return data, nil
	}
}

func readURL(source string) ([]byte, error) {
	client := http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, source, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "captain/gen-model-registry")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s: unexpected status %s", source, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", source, err)
	}
	return data, nil
}

func generateModels(data []byte, patches map[string]json.RawMessage) ([]generatedModel, error) {
	var providers map[string]modelsDevProvider
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, fmt.Errorf("parse models.dev catalog: %w", err)
	}
	out := map[string]generatedModel{}
	for _, mapping := range []struct {
		source   string
		provider string
	}{
		{"anthropic", "anthropic"},
		{"openai", "openai"},
		{"google", "google"},
		{"deepseek", "deepseek"},
	} {
		provider, ok := providers[mapping.source]
		if !ok {
			return nil, fmt.Errorf("models.dev catalog is missing provider %q", mapping.source)
		}
		for id, model := range provider.Models {
			if model.ID != "" {
				id = model.ID
			}
			_, explicitlyPatched := patches[id]
			row, ok := generatedModelFromModelsDev(mapping.provider, id, model, explicitlyPatched)
			if ok {
				out[row.ID] = row
			}
		}
	}
	if err := applyPatches(out, patches); err != nil {
		return nil, err
	}

	models := make([]generatedModel, 0, len(out))
	for _, row := range out {
		models = append(models, row)
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Provider == models[j].Provider {
			if models[i].ReleaseDate == models[j].ReleaseDate {
				return models[i].ID < models[j].ID
			}
			return models[i].ReleaseDate > models[j].ReleaseDate
		}
		return providerRank(models[i].Provider) < providerRank(models[j].Provider)
	})
	return models, nil
}

func generatedModelFromModelsDev(provider, id string, model modelsDevModel, explicitlyPatched bool) (generatedModel, bool) {
	id = strings.TrimSpace(id)
	if id == "" || !supportsTextIO(model) || (!explicitlyPatched && hiddenModel(provider, id)) {
		return generatedModel{}, false
	}
	identity, ok := captainai.ParseModelIdentity(provider, id)
	if !ok || identity.Provider != provider {
		return generatedModel{}, false
	}
	if !supportedFamily(identity) {
		return generatedModel{}, false
	}
	label := strings.TrimSpace(model.Name)
	if label == "" {
		label = titleModelID(id)
	}
	// Preferred is opt-in: models default to non-preferred and are surfaced in
	// menus only when patches.json explicitly sets "preferred": true.
	return generatedModel{
		ID:               id,
		Provider:         provider,
		Family:           identity.Family,
		Version:          identity.Version,
		Label:            label,
		ReleaseDate:      model.ReleaseDate,
		Reasoning:        model.Reasoning,
		Temperature:      model.Temperature,
		ContextWindow:    model.Limit.Context,
		Cost:             deriveCost(model),
		InputMediaTypes:  deriveInputMediaTypes(model.Modalities.Input),
		AdaptiveThinking: deriveAdaptiveThinking(provider, model),
		SupportedEfforts: deriveSupportedEfforts(provider, model),
	}, true
}

func deriveInputMediaTypes(modalities []string) []string {
	out := make([]string, 0, len(modalities))
	for _, modality := range modalities {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "image":
			out = append(out, "image/*")
		case "audio":
			out = append(out, "audio/*")
		case "video":
			out = append(out, "video/*")
		case "pdf":
			out = append(out, "application/pdf")
		}
	}
	return out
}

func deriveSupportedEfforts(provider string, model modelsDevModel) []string {
	// Only retain source levels that Captain can submit through the selected
	// backend. DeepSeek selects reasoning by model ID, while Anthropic's legacy
	// budget-token models do not accept an effort value. Gemini's "minimal"
	// level is intentionally dropped by the shared Effort validation below until
	// Captain has a faithful token-budget mapping for it.
	if provider == "deepseek" || (provider == "anthropic" && !deriveAdaptiveThinking(provider, model)) {
		return nil
	}
	for _, opt := range model.ReasoningOptions {
		if opt.Type != "effort" {
			continue
		}
		out := make([]string, 0, len(opt.Values))
		seen := map[string]bool{}
		for _, value := range opt.Values {
			value = strings.ToLower(strings.TrimSpace(value))
			if value == "" || value == "none" || seen[value] || captainai.ValidateEffort(captainai.Effort(value)) != nil {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
		return out
	}
	return nil
}

// deriveAdaptiveThinking reports whether an Anthropic model uses the adaptive
// thinking schema (thinking:{type:adaptive} + output_config.effort) rather than
// the legacy enabled schema. models.dev encodes this in reasoning_options: an
// effort control with no budget_tokens control means adaptive. It is
// Anthropic-only; other providers carry their own effort mechanisms.
func deriveAdaptiveThinking(provider string, model modelsDevModel) bool {
	if provider != "anthropic" || !model.Reasoning {
		return false
	}
	hasEffort, hasBudget := false, false
	for _, opt := range model.ReasoningOptions {
		switch opt.Type {
		case "effort":
			hasEffort = true
		case "budget_tokens":
			hasBudget = true
		}
	}
	return hasEffort && !hasBudget
}

// supportsTextIO keeps only models Captain can actually prompt: it must accept a
// text prompt and return text. Audio-only Live API entries (e.g.
// gemini-3.5-live-translate-preview) declare no text input and would otherwise
// land in the catalog as models no prompt can be sent to.
func supportsTextIO(model modelsDevModel) bool {
	return declaresText(model.Modalities.Input) && declaresText(model.Modalities.Output)
}

// declaresText reports whether a modality list names text. An empty list means
// models.dev did not record modalities, which predates the field and is treated
// as plain text.
func declaresText(modalities []string) bool {
	if len(modalities) == 0 {
		return true
	}
	for _, modality := range modalities {
		if strings.EqualFold(strings.TrimSpace(modality), "text") {
			return true
		}
	}
	return false
}

func hiddenModel(provider, id string) bool {
	switch provider {
	case "openai":
		return captainai.IsLegacyModelIDForRuntime(id, captainai.OpenAI, captainai.ModeAPI)
	case "anthropic":
		return captainai.IsLegacyModelIDForRuntime(id, captainai.Anthropic, captainai.ModeAPI)
	case "google":
		lower := strings.ToLower(id)
		return strings.Contains(lower, "embedding") || strings.Contains(lower, "image") || strings.Contains(lower, "tts")
	default:
		return false
	}
}

func supportedFamily(identity captainai.ModelIdentity) bool {
	switch identity.Provider {
	case "anthropic":
		switch identity.Family {
		case "opus", "sonnet", "haiku", "fable":
			return true
		}
	case "openai":
		return identity.Family == "gpt"
	case "google":
		return identity.Family == "gemini"
	case "deepseek":
		return identity.Family == "deepseek"
	}
	return false
}

// applyPatches overlays the per-model merge patches onto the fetched catalog.
// Each patch overwrites only the fields it names (RFC 7386 merge semantics); a
// null patch value drops the model from the catalog entirely, and a patch whose
// ID is absent from the catalog is treated as a new full entry that must name
// provider, family, and label.
func applyPatches(out map[string]generatedModel, patches map[string]json.RawMessage) error {
	ids := make([]string, 0, len(patches))
	for id := range patches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		raw := patches[id]
		if strings.TrimSpace(string(raw)) == "null" {
			delete(out, id)
			continue
		}
		m, existed := out[id]
		if m.ID == "" {
			m.ID = id
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("patch %q: %w", id, err)
		}
		if !existed && (m.Provider == "" || m.Family == "" || m.Label == "") {
			return fmt.Errorf("patch %q adds a new model but is missing provider, family, or label", id)
		}
		out[id] = m
	}
	return nil
}

func renderJSON(models []generatedModel) ([]byte, error) {
	data, err := json.MarshalIndent(models, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func providerRank(provider string) int {
	switch provider {
	case "anthropic":
		return 0
	case "openai":
		return 1
	case "google":
		return 2
	case "deepseek":
		return 3
	default:
		return 100
	}
}

func titleModelID(id string) string {
	parts := strings.Split(strings.ReplaceAll(id, "_", "-"), "-")
	for i, part := range parts {
		switch strings.ToLower(part) {
		case "gpt":
			parts[i] = "GPT"
		default:
			if part != "" {
				parts[i] = strings.ToUpper(part[:1]) + part[1:]
			}
		}
	}
	return strings.Join(parts, " ")
}
