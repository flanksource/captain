// Package prompt renders Google dotprompt (.prompt) templates into captain's
// ai.Request / ai.Config. A .prompt file carries YAML frontmatter (model,
// config, input/output schema) and a Handlebars body whose {{role "system"}} /
// {{role "user"}} markers split the rendered output into messages.
//
// Structured output is driven by the Go target passed to Render (out), which
// becomes ai.Request.StructuredOutput — captain's providers derive the JSON
// schema from that Go type. The frontmatter output schema is advisory and is
// not used to populate StructuredOutput (captain cannot consume a bare schema
// map), so pass a Go struct when you want structured results.
package prompt

import (
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	dp "github.com/google/dotprompt/go/dotprompt"
)

// Template is a parsed .prompt source ready to render with runtime data.
type Template struct {
	dp     *dp.Dotprompt
	source string
	name   string
}

// Load parses a .prompt source string.
func Load(source string) *Template {
	return &Template{dp: dp.NewDotprompt(nil), source: source, name: "<inline>"}
}

// LoadFile reads and parses a .prompt file from disk.
func LoadFile(path string) (*Template, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read prompt %s: %w", path, err)
	}
	t := Load(string(b))
	t.name = path
	return t, nil
}

// LoadFS reads and parses a .prompt file from an fs.FS (e.g. an embed.FS).
func LoadFS(fsys fs.FS, path string) (*Template, error) {
	b, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, fmt.Errorf("read prompt %s: %w", path, err)
	}
	t := Load(string(b))
	t.name = path
	return t, nil
}

// Render executes the template body with data and folds the frontmatter into an
// ai.Request and ai.Config. When out is non-nil it becomes
// Request.StructuredOutput (the structured-output target).
func (t *Template) Render(data map[string]any, out any) (ai.Request, ai.Config, error) {
	rendered, err := t.dp.Render(t.source, &dp.DataArgument{Input: data}, nil)
	if err != nil {
		return ai.Request{}, ai.Config{}, fmt.Errorf("render prompt %s: %w", t.name, err)
	}

	var system, user []string
	for _, m := range rendered.Messages {
		switch m.Role {
		case dp.RoleSystem:
			system = append(system, textOf(m.Content))
		case dp.RoleUser:
			user = append(user, textOf(m.Content))
		}
	}

	req := ai.Request{
		SystemPrompt: strings.TrimSpace(strings.Join(system, "\n")),
		Prompt:       strings.TrimSpace(strings.Join(user, "\n")),
		Source:       t.name,
	}
	cfg := ai.Config{Model: rendered.Model}
	if rendered.Model != "" {
		if b, berr := ai.InferBackend(rendered.Model); berr == nil {
			cfg.Backend = b
		}
	}
	applyModelConfig(rendered.Config, &req, &cfg)
	if out != nil {
		req.StructuredOutput = out
	}
	return req, cfg, nil
}

// Library renders named .prompt files from an fs.FS (typically an embed.FS), the
// drop-in replacement for ad-hoc embed + gomplate prompt rendering.
type Library struct{ fsys fs.FS }

// NewLibrary returns a Library backed by fsys.
func NewLibrary(fsys fs.FS) *Library { return &Library{fsys: fsys} }

// Render loads name from the library and renders it.
func (l *Library) Render(name string, data map[string]any, out any) (ai.Request, ai.Config, error) {
	t, err := LoadFS(l.fsys, name)
	if err != nil {
		return ai.Request{}, ai.Config{}, err
	}
	return t.Render(data, out)
}

// applyModelConfig maps the dotprompt config block (model-agnostic keys) onto
// the captain request/config.
func applyModelConfig(c dp.ModelConfig, req *ai.Request, cfg *ai.Config) {
	if v, ok := floatOf(c["maxOutputTokens"]); ok {
		req.MaxTokens = int(v)
		cfg.MaxTokens = int(v)
	}
	if v, ok := floatOf(c["temperature"]); ok {
		req.Temperature = v
		cfg.Temperature = v
	}
	if s, ok := c["reasoning"].(string); ok {
		req.ReasoningEffort = s
	}
}

// textOf concatenates the text of a message's parts (non-text parts are skipped).
func textOf(parts []dp.Part) string {
	var b strings.Builder
	for _, p := range parts {
		if tp, ok := p.(*dp.TextPart); ok {
			b.WriteString(tp.Text)
		}
	}
	return b.String()
}

// floatOf coerces a YAML/JSON-decoded numeric to float64. The dotprompt YAML
// decoder yields unsigned ints for positive whole numbers, so all integer kinds
// are handled.
func floatOf(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}
