// Package prompt renders Google dotprompt (.prompt) templates into captain's
// ai.Request / ai.Config. A .prompt file carries YAML frontmatter and a
// Handlebars body whose {{role "system"}} / {{role "user"}} markers split the
// rendered output into messages.
//
// The frontmatter is parsed twice: once by dotprompt for its built-in keys
// (model, config.maxOutputTokens/temperature/reasoning, input/output schema),
// and a second time straight into the spec (ai.Request = api.Spec) so any
// request option can be declared in its native, nested shape — e.g.
// permissions.mode, permissions.tools.allow, memory.skipUser, budget.maxTokens,
// context.dir, effort, sessionId, maxTurns. When a file mixes both dialects the
// dotprompt config: block wins for the three knobs it owns.
//
// Structured output can come from either the Go target passed to Render (out),
// which becomes ai.Request.Prompt.Schema (providers reflect the JSON schema from
// that Go type), or the frontmatter `output.schema` block (picoschema or raw
// JSON Schema), which the dotprompt library resolves and Render marshals onto
// ai.Request.Prompt.SchemaJSON (sent to the model verbatim). A Go target takes
// precedence; pass out == nil to use the schema declared in the file.
package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	dp "github.com/google/dotprompt/go/dotprompt"
	"github.com/mbleigh/raymond"
	"gopkg.in/yaml.v3"
)

// frontmatterRe splits a .prompt source into YAML frontmatter (group 1) and body
// (group 2). The pattern is copied from google/dotprompt's FrontmatterAndBodyRegex
// so the split matches exactly what the dotprompt library re-parses.
var frontmatterRe = regexp.MustCompile(
	`^(?:(?:#[^\n]*|[ \t]*)\n)*---\s*(?:\r\n|\r|\n)([\s\S]*?)(?:\r\n|\r|\n)---\s*(?:\r\n|\r|\n)([\s\S]*)$`)

// renderFrontmatter templates the YAML frontmatter of a .prompt source with data
// (the same map used to render the body), so authors can parametrize frontmatter —
// notably output.schema constraints like `maxItems: {{maxCommits}}`. Only the
// frontmatter is rendered here; the body is left for dotprompt so its {{role …}}
// helpers and partials render normally. No-op when the source has no frontmatter or
// the frontmatter contains no `{{`.
func renderFrontmatter(source string, data map[string]any) (string, error) {
	loc := frontmatterRe.FindStringSubmatchIndex(source)
	if loc == nil {
		return source, nil
	}
	fm := source[loc[2]:loc[3]]
	if !strings.Contains(fm, "{{") {
		return source, nil
	}
	tpl, err := raymond.Parse(fm)
	if err != nil {
		return "", fmt.Errorf("parse frontmatter template: %w", err)
	}
	out, err := tpl.ExecWith(data, raymond.NewDataFrame(), &raymond.ExecOptions{NoEscape: true})
	if err != nil {
		return "", fmt.Errorf("render frontmatter template: %w", err)
	}
	return source[:loc[2]] + out + source[loc[3]:], nil
}

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
// Request.Prompt.Schema (the structured-output target).
func (t *Template) Render(data map[string]any, out any) (ai.Request, ai.Config, error) {
	src, err := renderFrontmatter(t.source, data)
	if err != nil {
		return ai.Request{}, ai.Config{}, fmt.Errorf("render prompt %s frontmatter: %w", t.name, err)
	}
	rendered, err := t.dp.Render(src, &dp.DataArgument{Input: data}, nil)
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

	// Second parse: fold the full frontmatter into the spec so any request option
	// (permissions, memory, budget, context, …) can be declared in the file.
	var req ai.Request
	if err := decodeSpecFrontmatter(rendered.Raw, &req); err != nil {
		return ai.Request{}, ai.Config{}, fmt.Errorf("decode prompt %s frontmatter into ai.Request: %w", t.name, err)
	}

	// The rendered template body wins over any frontmatter prompt.user/system.
	if s := strings.TrimSpace(strings.Join(system, "\n")); s != "" {
		req.Prompt.System = s
	}
	if u := strings.TrimSpace(strings.Join(user, "\n")); u != "" {
		req.Prompt.User = u
	}
	req.Prompt.Source = t.name

	cfg := ai.Config{Model: req.Model}
	if cfg.Model.Name == "" {
		cfg.Model.Name = rendered.Model
	}
	if cfg.Model.Backend == "" && cfg.Model.Name != "" {
		if b, berr := ai.InferBackend(cfg.Model.Name); berr == nil {
			cfg.Model.Backend = b
		}
	}
	// The dotprompt config: block stays canonical for maxOutputTokens/temperature/
	// reasoning when a file mixes both frontmatter dialects, so apply it last.
	applyModelConfig(rendered.Config, &req, &cfg)
	cfg.Budget = req.Budget
	if out != nil {
		req.Prompt.Schema = out
	} else if rendered.Output.Schema != nil {
		// A frontmatter `output.schema` block (resolved by the dotprompt library
		// from picoschema or a raw JSON Schema) becomes the verbatim SchemaJSON.
		raw, err := json.Marshal(rendered.Output.Schema)
		if err != nil {
			return ai.Request{}, ai.Config{}, fmt.Errorf("marshal prompt %s output schema: %w", t.name, err)
		}
		req.Prompt.SchemaJSON = raw
	}
	return req, cfg, nil
}

// decodeSpecFrontmatter folds the raw dotprompt frontmatter map into the spec
// (ai.Request) by re-encoding it as YAML and unmarshalling into the typed spec.
// Spec-native keys land on their fields; dotprompt-only keys (config, input,
// output) have no spec field and are ignored. It is a no-op for empty frontmatter.
func decodeSpecFrontmatter(raw map[string]any, req *ai.Request) error {
	if len(raw) == 0 {
		return nil
	}
	specRaw := map[string]any{}
	for key, value := range raw {
		switch key {
		case "config", "input", "output", "name", "description":
			continue
		default:
			specRaw[key] = value
		}
	}
	if len(specRaw) == 0 {
		return nil
	}
	b, err := yaml.Marshal(specRaw)
	if err != nil {
		return fmt.Errorf("re-encode frontmatter: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(req)
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
		req.Budget.MaxTokens = int(v)
		cfg.Budget.MaxTokens = int(v)
	}
	if v, ok := floatOf(c["temperature"]); ok {
		temp := v
		req.Temperature = &temp
		cfg.Model.Temperature = &temp
	}
	if s, ok := c["reasoning"].(string); ok {
		req.Effort = api.Effort(s)
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
