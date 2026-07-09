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
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Family      string              `json:"family"`
	Reasoning   bool                `json:"reasoning"`
	ReleaseDate string              `json:"release_date"`
	Modalities  modelsDevModalities `json:"modalities"`
	Limit       modelsDevLimit      `json:"limit"`
}

type modelsDevModalities struct {
	Output []string `json:"output"`
}

type modelsDevLimit struct {
	Context int `json:"context"`
}

// generatedModel mirrors pkg/ai.registryModel field-for-field (and its JSON
// tags), so the emitted JSON round-trips into the registry on load.
type generatedModel struct {
	ID               string `json:"id"`
	Provider         string `json:"provider"`
	Family           string `json:"family"`
	Version          string `json:"version"`
	Label            string `json:"label"`
	ReleaseDate      string `json:"releaseDate,omitempty"`
	Reasoning        bool   `json:"reasoning,omitempty"`
	ContextWindow    int    `json:"contextWindow,omitempty"`
	Preferred        bool   `json:"preferred,omitempty"`
	AdaptiveThinking bool   `json:"adaptiveThinking,omitempty"`
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
	flag.StringVar(&output, "output", "", "Path to write pkg/ai/model_registry.json")
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
			row, ok := generatedModelFromModelsDev(mapping.provider, id, model)
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

func generatedModelFromModelsDev(provider, id string, model modelsDevModel) (generatedModel, bool) {
	id = strings.TrimSpace(id)
	if id == "" || !supportsTextOutput(model) || hiddenModel(provider, id) {
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
		ID:            id,
		Provider:      provider,
		Family:        identity.Family,
		Version:       identity.Version,
		Label:         label,
		ReleaseDate:   model.ReleaseDate,
		Reasoning:     model.Reasoning,
		ContextWindow: model.Limit.Context,
	}, true
}

func supportsTextOutput(model modelsDevModel) bool {
	if len(model.Modalities.Output) == 0 {
		return true
	}
	for _, output := range model.Modalities.Output {
		if output == "text" {
			return true
		}
	}
	return false
}

func hiddenModel(provider, id string) bool {
	switch provider {
	case "openai":
		return captainai.IsLegacyModelIDForBackend(id, captainai.BackendOpenAI)
	case "anthropic":
		return captainai.IsLegacyModelIDForBackend(id, captainai.BackendAnthropic)
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
