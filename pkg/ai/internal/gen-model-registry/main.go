package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
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

type generatedModel struct {
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gen-model-registry: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var output string
	var source string
	flag.StringVar(&output, "output", "", "Path to write pkg/ai/model_registry_static.go")
	flag.StringVar(&source, "source", modelsDevAPIURL, "models.dev API URL, local file path, or - for stdin")
	flag.Parse()
	if output == "" {
		return fmt.Errorf("--output is required")
	}

	data, err := readSource(source)
	if err != nil {
		return err
	}
	models, err := generateModels(data)
	if err != nil {
		return err
	}
	src, err := renderRegistry(models)
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

func generateModels(data []byte) ([]generatedModel, error) {
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
	for _, row := range localRegistryOverrides() {
		out[row.ID] = row
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
	return generatedModel{
		ID:            id,
		Provider:      provider,
		Family:        identity.Family,
		Version:       identity.Version,
		Label:         label,
		ReleaseDate:   model.ReleaseDate,
		Reasoning:     model.Reasoning,
		ContextWindow: model.Limit.Context,
		Preferred:     !isNonPreferredID(id),
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

func isNonPreferredID(id string) bool {
	lower := strings.ToLower(id)
	return hasTrailingDateID(lower) || strings.Contains(lower, "preview") || strings.Contains(lower, "latest")
}

func hasTrailingDateID(id string) bool {
	parts := strings.Split(id, "-")
	if len(parts) == 0 {
		return false
	}
	last := parts[len(parts)-1]
	if len(last) != 8 {
		return false
	}
	for _, r := range last {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func renderRegistry(models []generatedModel) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("// Code generated by pkg/ai/internal/gen-model-registry; DO NOT EDIT.\n\n")
	buf.WriteString("package ai\n\n")
	buf.WriteString("var exactModelRegistry = []registryModel{\n")
	for i, model := range models {
		if i > 0 && models[i-1].Provider != model.Provider {
			buf.WriteByte('\n')
		}
		fmt.Fprintf(&buf, "\t{ID: %q, Provider: %s, Family: %q, Version: %q, Label: %q",
			model.ID, providerConst(model.Provider), model.Family, model.Version, model.Label)
		if model.ReleaseDate != "" {
			fmt.Fprintf(&buf, ", ReleaseDate: %q", model.ReleaseDate)
		}
		if model.Reasoning {
			buf.WriteString(", Reasoning: true")
		}
		if model.ContextWindow > 0 {
			fmt.Fprintf(&buf, ", ContextWindow: %d", model.ContextWindow)
		}
		if model.Preferred {
			buf.WriteString(", Preferred: true")
		}
		buf.WriteString("},\n")
	}
	buf.WriteString("}\n")
	src, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, err
	}
	return src, nil
}

func providerConst(provider string) string {
	switch provider {
	case "anthropic":
		return "modelProviderAnthropic"
	case "openai":
		return "modelProviderOpenAI"
	case "google":
		return "modelProviderGoogle"
	case "deepseek":
		return "modelProviderDeepSeek"
	default:
		panic("unhandled provider " + provider)
	}
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

func localRegistryOverrides() []generatedModel {
	return []generatedModel{
		{ID: "claude-sonnet-5", Provider: "anthropic", Family: "sonnet", Version: "5", Label: "Claude Sonnet 5", ReleaseDate: "2026-06-29", Reasoning: true, ContextWindow: 1000000, Preferred: true},
		{ID: "claude-fable-5", Provider: "anthropic", Family: "fable", Version: "5", Label: "Claude Fable 5", ReleaseDate: "2026-06-07", Reasoning: true, ContextWindow: 1000000, Preferred: true},
		{ID: "claude-opus-4-8", Provider: "anthropic", Family: "opus", Version: "4.8", Label: "Claude Opus 4.8", ReleaseDate: "2026-05-28", Reasoning: true, ContextWindow: 1000000, Preferred: true},
		{ID: "claude-opus-4-7", Provider: "anthropic", Family: "opus", Version: "4.7", Label: "Claude Opus 4.7", ReleaseDate: "2026-04-14", Reasoning: true, ContextWindow: 1000000, Preferred: true},
		{ID: "claude-sonnet-4-6", Provider: "anthropic", Family: "sonnet", Version: "4.6", Label: "Claude Sonnet 4.6", ReleaseDate: "2026-02-17", Reasoning: true, ContextWindow: 1000000, Preferred: true},
		{ID: "claude-haiku-4-5", Provider: "anthropic", Family: "haiku", Version: "4.5", Label: "Claude Haiku 4.5", ReleaseDate: "2025-10-15", Reasoning: true, ContextWindow: 200000, Preferred: true},
		{ID: "claude-haiku-4-5-20251001", Provider: "anthropic", Family: "haiku", Version: "4.5-20251001", Label: "Claude Haiku 4.5", ReleaseDate: "2025-10-15", Reasoning: true, ContextWindow: 200000},
		{ID: "gpt-5.5", Provider: "openai", Family: "gpt", Version: "5.5", Label: "GPT-5.5", ReleaseDate: "2026-04-23", Reasoning: true, ContextWindow: 1050000, Preferred: true},
		{ID: "gpt-5.4", Provider: "openai", Family: "gpt", Version: "5.4", Label: "GPT-5.4", ReleaseDate: "2026-03-05", Reasoning: true, ContextWindow: 1050000, Preferred: true},
		{ID: "gpt-5", Provider: "openai", Family: "gpt", Version: "5", Label: "GPT-5", ReleaseDate: "2025-08-07", Reasoning: true, ContextWindow: 400000, Preferred: true},
		{ID: "gemini-3.5-flash", Provider: "google", Family: "gemini", Version: "3.5-flash", Label: "Gemini 3.5 Flash", ReleaseDate: "2026-05-19", Reasoning: true, ContextWindow: 1048576, Preferred: true},
		{ID: "gemini-2.5-pro", Provider: "google", Family: "gemini", Version: "2.5-pro", Label: "Gemini 2.5 Pro", ReleaseDate: "2025-06-17", Reasoning: true, ContextWindow: 1048576, Preferred: true},
		{ID: "gemini-2.5-flash", Provider: "google", Family: "gemini", Version: "2.5-flash", Label: "Gemini 2.5 Flash", ReleaseDate: "2025-06-17", Reasoning: true, ContextWindow: 1048576, Preferred: true},
		{ID: "deepseek-v4-pro", Provider: "deepseek", Family: "deepseek", Version: "v4-pro", Label: "DeepSeek V4 Pro", ReleaseDate: "2026-04-24", Reasoning: true, ContextWindow: 1000000, Preferred: true},
		{ID: "deepseek-v4-flash", Provider: "deepseek", Family: "deepseek", Version: "v4-flash", Label: "DeepSeek V4 Flash", ReleaseDate: "2026-04-24", Reasoning: true, ContextWindow: 1000000, Preferred: true},
		{ID: "deepseek-reasoner", Provider: "deepseek", Family: "deepseek", Version: "reasoner", Label: "DeepSeek Reasoner", ReleaseDate: "2025-12-01", Reasoning: true, ContextWindow: 1000000, Preferred: true},
		{ID: "deepseek-chat", Provider: "deepseek", Family: "deepseek", Version: "chat", Label: "DeepSeek Chat", ReleaseDate: "2025-12-01", ContextWindow: 1000000, Preferred: true},
	}
}
