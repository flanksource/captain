package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	dp "github.com/google/dotprompt/go/dotprompt"
)

func listPromptRecords(ctx context.Context) ([]promptRecord, error) {
	sources, err := buildPromptSources(ctx, promptSourceOptions{})
	if err != nil {
		return nil, err
	}
	var records []promptRecord
	for _, source := range sources {
		recs, err := listPromptRecordsFromSource(source)
		if err != nil {
			return nil, err
		}
		records = append(records, recs...)
	}
	return records, nil
}

func listPromptRecordsFromSource(source promptSource) ([]promptRecord, error) {
	var records []promptRecord
	add := func(rel string, info fs.FileInfo) {
		rel = filepath.ToSlash(rel)
		path := rel
		if source.Root != "" {
			path = filepath.Join(source.Root, filepath.FromSlash(rel))
		}
		records = append(records, promptRecord{
			Source:    source,
			ID:        encodePromptID(source.Kind, source.ID, rel),
			Path:      path,
			Rel:       rel,
			UpdatedAt: modTimeString(info),
		})
	}

	if source.FS != nil {
		err := fs.WalkDir(source.FS, source.WalkRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".prompt") {
				return nil
			}
			info, _ := d.Info()
			add(path, info)
			return nil
		})
		return records, err
	}

	err := filepath.WalkDir(source.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "node_modules" || name == "vendor" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !strings.HasSuffix(name, ".prompt") {
			return nil
		}
		rel, err := filepath.Rel(source.Root, path)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		add(rel, info)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) && source.Implicit {
		return records, nil
	}
	return records, err
}

type promptRecordOptions struct {
	ID     string
	Config *captainconfig.Config
}

func resolvePromptRecord(ctx context.Context, options promptRecordOptions) (promptRecord, error) {
	id := strings.TrimSpace(options.ID)
	if looksLikePromptPath(id) {
		record, err := filePromptRecord(id)
		if err == nil {
			return record, nil
		}
		if !isBarePromptFilename(id) || !errors.Is(err, fs.ErrNotExist) {
			return promptRecord{}, err
		}
	}
	sources, err := buildPromptSources(ctx, promptSourceOptions{Config: options.Config})
	if err != nil {
		return promptRecord{}, err
	}
	ref, decodeErr := decodePromptID(id)
	if decodeErr == nil {
		for _, source := range sources {
			if source.Kind != ref.Kind || source.ID != ref.SourceID {
				continue
			}
			path := ref.RelPath
			if source.Root != "" {
				path = filepath.Join(source.Root, filepath.FromSlash(ref.RelPath))
			}
			return promptRecord{Source: source, ID: id, Path: path, Rel: ref.RelPath}, nil
		}
		return promptRecord{}, fmt.Errorf("prompt source %q not found", ref.SourceID)
	}
	return resolvePromptRecordByName(sources, id)
}

func resolvePromptRecordByName(sources []promptSource, name string) (promptRecord, error) {
	bareName := strings.TrimSuffix(name, ".prompt")
	var matches []promptRecord
	for _, source := range sources {
		records, err := listPromptRecordsFromSource(source)
		if err != nil {
			return promptRecord{}, err
		}
		for _, record := range records {
			recordName := strings.TrimSuffix(filepath.Base(record.Rel), ".prompt")
			if recordName == bareName {
				matches = append(matches, record)
			}
		}
	}
	switch len(matches) {
	case 0:
		return promptRecord{}, fmt.Errorf("prompt %q not found", name)
	case 1:
		return matches[0], nil
	default:
		paths := make([]string, len(matches))
		for i, match := range matches {
			paths[i] = match.Source.Label + ":" + match.Rel
		}
		return promptRecord{}, fmt.Errorf("prompt name %q is ambiguous (%s); use a prompt id or path", name, strings.Join(paths, ", "))
	}
}

func isBarePromptFilename(id string) bool {
	return filepath.Base(id) == id && !strings.HasPrefix(id, ".")
}

// looksLikePromptPath reports whether id is a filesystem path rather than a
// base64 registry id. Registry ids are base64-raw-url (no ".", "/", or leading
// "."), so a .prompt suffix, a path separator, or a leading "." marks a path.
func looksLikePromptPath(id string) bool {
	return strings.HasSuffix(id, ".prompt") ||
		strings.ContainsRune(id, os.PathSeparator) ||
		strings.HasPrefix(id, ".")
}

// filePromptRecord resolves an ad-hoc .prompt file path (not a registered id)
// into a record readable via readPromptContent/safeLocalPromptPath. Mirrors the
// captain-ai-prompt file loader so `captain prompt run|render ./x.prompt` works.
func filePromptRecord(id string) (promptRecord, error) {
	abs, err := filepath.Abs(id)
	if err != nil {
		return promptRecord{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return promptRecord{}, fmt.Errorf("prompt file %s: %w", id, err)
	}
	if info.IsDir() {
		return promptRecord{}, fmt.Errorf("%s is a directory, not a .prompt file", id)
	}
	return promptRecord{
		Source:    promptSource{Kind: "file", ID: "file", Label: "File", Root: filepath.Dir(abs)},
		ID:        id,
		Path:      abs,
		Rel:       filepath.Base(abs),
		UpdatedAt: modTimeString(info),
	}, nil
}

func promptSummary(record promptRecord) (PromptSummary, error) {
	content, err := readPromptContent(record)
	if err != nil {
		return PromptSummary{}, err
	}
	return promptSummaryOrRepair(record, content), nil
}

// promptSummaryOrRepair keeps a prompt that no longer parses listable: the base
// summary carries the parser message so the UI can offer the raw source for
// repair instead of a dead row.
func promptSummaryOrRepair(record promptRecord, content string) PromptSummary {
	summary, err := promptSummaryFromContent(record, content)
	if err != nil {
		summary = basePromptSummary(record)
		summary.ParseError = err.Error()
	}
	summary.Version = promptVersion(content)
	return summary
}

func promptDetail(record promptRecord) (PromptDetail, error) {
	content, err := readPromptContent(record)
	if err != nil {
		return PromptDetail{}, err
	}
	if record.UpdatedAt == "" {
		record.UpdatedAt = promptRecordModTime(record)
	}
	return promptDetailFromContent(record, content)
}

// promptDetailFromContent builds the editable view of a prompt. Content that no
// longer parses is still returned — with ParseError set and no run seed — so the
// editor can show the raw source for repair rather than refusing to open it.
func promptDetailFromContent(record promptRecord, content string) (PromptDetail, error) {
	detail, err := parsedPromptDetail(record, content)
	if err == nil {
		return detail, nil
	}
	summary := basePromptSummary(record)
	summary.ParseError = err.Error()
	summary.Version = promptVersion(content)
	return PromptDetail{PromptSummary: summary, Content: content}, nil
}

// parsedPromptDetail is the strict form: it fails on any content that cannot be
// rendered or inspected, which is exactly what a write must check before it
// touches disk.
func parsedPromptDetail(record promptRecord, content string) (PromptDetail, error) {
	summary, err := promptSummaryFromContent(record, content)
	if err != nil {
		return PromptDetail{}, err
	}
	inspection, err := inspectPrompt(content, nil)
	if err != nil {
		return PromptDetail{}, err
	}
	summary.Version = promptVersion(content)
	spec := &api.Spec{Model: api.Model{
		Name: summary.Model,
		Mode: api.RuntimeMode(summary.Mode),
	}}
	return PromptDetail{
		PromptSummary: summary,
		Content:       content,
		InputSchema:   inspection.InputSchema,
		InputDefault:  inspection.InputDefault,
		OutputSchema:  inspection.OutputSchema,
		Metadata:      inspection.Metadata,
		Run: PromptRenderRequest{
			Variables:      maps.Clone(inspection.InputDefault),
			RuntimeProfile: summary.RuntimeProfile,
			Spec:           spec,
			Runtimes:       promptRunModels(summary.Runtimes),
			Chat:           len(inspection.OutputSchema) == 0,
		},
	}, nil
}

func promptRunModels(models []api.Model) []api.Model {
	if len(models) == 0 {
		return nil
	}
	out := make([]api.Model, len(models))
	for index, model := range models {
		out[index] = api.Model{
			Explicit:    model.Explicit.Clone(),
			Name:        model.Name,
			ID:          model.ID,
			Mode:        model.Mode,
			Temperature: model.Temperature,
			Effort:      model.Effort,
			NoCache:     model.NoCache,
			Fallbacks:   promptRunModels(model.Fallbacks),
		}
	}
	return out
}

func promptSummaryFromContent(record promptRecord, content string) (PromptSummary, error) {
	tmpl := promptlib.Load(content)
	req, cfg, err := tmpl.Render(promptlib.RenderOptions{Data: map[string]any{}, Declared: true})
	if err != nil {
		return PromptSummary{}, err
	}
	inspection, err := inspectPrompt(content, nil)
	if err != nil {
		return PromptSummary{}, err
	}
	summary := basePromptSummary(record)
	if v, ok := inspection.Metadata["name"].(string); ok && strings.TrimSpace(v) != "" {
		summary.Name = strings.TrimSpace(v)
	}
	if v, ok := inspection.Metadata["description"].(string); ok {
		summary.Description = strings.TrimSpace(v)
	}
	summary.Model = firstNonEmpty(cfg.Model.Name, req.Name)
	summary.Mode = firstNonEmpty(string(cfg.Model.Mode), string(req.Mode))
	summary.RuntimeProfile = inspection.RuntimeProfile
	summary.Runtimes = inspection.Runtimes
	summary.Variables = inspection.Variables
	return summary, nil
}

func basePromptSummary(record promptRecord) PromptSummary {
	name := strings.TrimSuffix(filepath.Base(record.Rel), ".prompt")
	return PromptSummary{
		ID:         record.ID,
		Name:       name,
		SourceKind: record.Source.Kind,
		SourceID:   record.Source.ID,
		Source:     record.Source.Label,
		Path:       record.Path,
		RelPath:    record.Rel,
		Writable:   record.Source.Writable,
		UpdatedAt:  record.UpdatedAt,
	}
}

func readPromptContent(record promptRecord) (string, error) {
	if record.Source.FS != nil {
		data, err := fs.ReadFile(record.Source.FS, record.Rel)
		if err != nil {
			return "", fmt.Errorf("read embedded prompt %s: %w", record.Rel, err)
		}
		return string(data), nil
	}
	file, err := openLocalPromptFile(record, false)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := file.root.ReadFile(file.rel)
	if err != nil {
		return "", fmt.Errorf("read prompt %s: %w", file.path, err)
	}
	return string(data), nil
}

func inspectPrompt(content string, data map[string]any) (promptInspection, error) {
	if data == nil {
		data = map[string]any{}
	}
	rendered, err := dp.NewDotprompt(nil).Render(content, &dp.DataArgument{Input: data}, nil)
	if err != nil {
		return promptInspection{}, err
	}
	doc, err := promptlib.Parse(content)
	if err != nil {
		return promptInspection{}, err
	}
	metadata := map[string]any{}
	if rendered.Raw != nil {
		for k, v := range rendered.Raw {
			metadata[k] = v
		}
	}
	if rendered.Name != "" {
		metadata["name"] = rendered.Name
	}
	if rendered.Description != "" {
		metadata["description"] = rendered.Description
	}
	if rendered.Model != "" {
		metadata["model"] = rendered.Model
	}
	inputSchema := anyToMap(rendered.Input.Schema)
	inputDefault := map[string]any{}
	for k, v := range rendered.Input.Default {
		inputDefault[k] = v
	}
	return promptInspection{
		Metadata:       metadata,
		InputSchema:    inputSchema,
		InputDefault:   inputDefault,
		OutputSchema:   anyToMap(rendered.Output.Schema),
		Runtimes:       doc.Runtimes,
		RuntimeProfile: doc.RuntimeProfile,
		Variables:      variablesFromSchema(inputSchema),
	}, nil
}

func anyToMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

func variablesFromSchema(schema map[string]any) []PromptVariable {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	required := map[string]bool{}
	if raw, ok := schema["required"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				required[s] = true
			}
		}
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var vars []PromptVariable
	for _, name := range keys {
		prop, _ := props[name].(map[string]any)
		item := PromptVariable{Name: name, Required: required[name]}
		if v, ok := prop["type"].(string); ok {
			item.Type = v
		}
		if v, ok := prop["description"].(string); ok {
			item.Description = v
		}
		vars = append(vars, item)
	}
	return vars
}

func promptSourceMatches(summary PromptSummary, source string) bool {
	switch source {
	case "", "all":
		return true
	case "embedded":
		return summary.SourceKind == "embedded"
	case "local":
		return summary.SourceKind == "local"
	default:
		return summary.SourceID == source || strings.EqualFold(summary.Source, source)
	}
}

func promptMatches(summary PromptSummary, filter string) bool {
	if filter == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		summary.Name,
		summary.Description,
		summary.Source,
		summary.Path,
		summary.RelPath,
		summary.Model,
		summary.Mode,
	}, "\n"))
	return strings.Contains(haystack, filter)
}
