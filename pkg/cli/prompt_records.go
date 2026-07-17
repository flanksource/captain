package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	dp "github.com/google/dotprompt/go/dotprompt"
)

func listPromptRecords(ctx context.Context) ([]promptRecord, error) {
	sources, err := buildPromptSources(ctx)
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
		updatedAt := ""
		if info != nil && !info.ModTime().IsZero() {
			updatedAt = info.ModTime().Format(time.RFC3339)
		}
		records = append(records, promptRecord{
			Source: source,
			ID:     encodePromptID(source.Kind, source.ID, rel),
			Path:   path + "\x00" + updatedAt,
			Rel:    rel,
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

func resolvePromptRecord(ctx context.Context, id string) (promptRecord, error) {
	if looksLikePromptPath(id) {
		return filePromptRecord(id)
	}
	ref, err := decodePromptID(id)
	if err != nil {
		return promptRecord{}, err
	}
	sources, err := buildPromptSources(ctx)
	if err != nil {
		return promptRecord{}, err
	}
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
		Source: promptSource{Kind: "file", ID: "file", Label: "File", Root: filepath.Dir(abs)},
		ID:     id,
		Path:   abs,
		Rel:    filepath.Base(abs),
	}, nil
}

func promptSummary(record promptRecord) (PromptSummary, error) {
	content, err := readPromptContent(record)
	if err != nil {
		return PromptSummary{}, err
	}
	summary, err := promptSummaryFromContent(record, content)
	if err != nil {
		summary = basePromptSummary(record)
		summary.ParseError = err.Error()
	}
	if idx := strings.LastIndex(record.Path, "\x00"); idx >= 0 {
		summary.UpdatedAt = strings.TrimPrefix(record.Path[idx+1:], "\x00")
	}
	return summary, nil
}

func promptDetail(record promptRecord) (PromptDetail, error) {
	content, err := readPromptContent(record)
	if err != nil {
		return PromptDetail{}, err
	}
	return promptDetailFromContent(record, content)
}

func promptDetailFromContent(record promptRecord, content string) (PromptDetail, error) {
	summary, err := promptSummaryFromContent(record, content)
	if err != nil {
		return PromptDetail{}, err
	}
	inspection, err := inspectPrompt(content, nil)
	if err != nil {
		return PromptDetail{}, err
	}
	return PromptDetail{
		PromptSummary: summary,
		Content:       content,
		InputSchema:   inspection.InputSchema,
		InputDefault:  inspection.InputDefault,
		OutputSchema:  inspection.OutputSchema,
		Metadata:      inspection.Metadata,
	}, nil
}

func promptSummaryFromContent(record promptRecord, content string) (PromptSummary, error) {
	tmpl := promptlib.Load(content)
	req, cfg, err := tmpl.Render(map[string]any{}, nil)
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
	summary.Backend = firstNonEmpty(string(cfg.Model.Backend), string(req.Backend))
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
		Path:       displayPromptPath(record),
		RelPath:    record.Rel,
		Writable:   record.Source.Writable,
	}
}

func displayPromptPath(record promptRecord) string {
	if idx := strings.LastIndex(record.Path, "\x00"); idx >= 0 {
		return record.Path[:idx]
	}
	return record.Path
}

func readPromptContent(record promptRecord) (string, error) {
	if record.Source.FS != nil {
		data, err := fs.ReadFile(record.Source.FS, record.Rel)
		if err != nil {
			return "", fmt.Errorf("read embedded prompt %s: %w", record.Rel, err)
		}
		return string(data), nil
	}
	full, err := safeLocalPromptPath(record.Source, record.Rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("read prompt %s: %w", full, err)
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
		Metadata:     metadata,
		InputSchema:  inputSchema,
		InputDefault: inputDefault,
		OutputSchema: anyToMap(rendered.Output.Schema),
		Variables:    variablesFromSchema(inputSchema),
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
		summary.Backend,
	}, "\n"))
	return strings.Contains(haystack, filter)
}
