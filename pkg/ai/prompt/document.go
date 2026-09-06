package prompt

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	dp "github.com/google/dotprompt/go/dotprompt"
	"gopkg.in/yaml.v3"
)

// Document is a parsed .prompt split into its raw YAML frontmatter, the
// spec-native fold of that frontmatter, and the unrendered Handlebars body.
//
// Unlike Render, Parse does not execute the template, so a Document round-trips
// an editable prompt: Frontmatter keeps every key (including the dotprompt-only
// config/input/output and any nested content the spec does not model, e.g.
// output.schema), Spec is the typed view for validation, and Body is left
// verbatim. String reserializes the document so Parse(d.String()) preserves the
// frontmatter keys and body.
type Document struct {
	// Frontmatter is the full YAML frontmatter as a map, nil when the source has
	// no frontmatter. It is the lossless source of truth for reserialization.
	Frontmatter map[string]any
	// Spec is the frontmatter decoded into the typed spec (the dotprompt-only
	// keys config/input/output/name/description/runtimes are stripped). Zero
	// when the source is body-only or declares no spec-native keys.
	Spec api.Spec
	// Runtimes are the prompt's default parallel execution targets.
	Runtimes []api.Model
	// RuntimeProfile pins the runtime profile (id or name) the prompt resolves
	// under; empty when the prompt leaves the selection to the caller.
	RuntimeProfile string
	// Body is the unrendered Handlebars template body.
	Body string
}

// Parse splits a .prompt source into frontmatter and body WITHOUT rendering the
// body, then decodes the spec-native frontmatter into Spec. It fails loud on
// malformed YAML or on a spec-native key the spec does not model — the opposite
// of the dotprompt library's ParseDocument, which prints and swallows YAML
// errors.
func Parse(source string) (*Document, error) {
	frontmatter, body, hasFrontmatter := splitFrontmatter(source)
	doc := &Document{Body: body}
	if !hasFrontmatter || strings.TrimSpace(frontmatter) == "" {
		return doc, nil
	}
	raw := map[string]any{}
	if err := yaml.Unmarshal([]byte(frontmatter), &raw); err != nil {
		return nil, fmt.Errorf("parse prompt frontmatter: %w", err)
	}
	doc.Frontmatter = raw
	runtimes, err := decodePromptRuntimes(raw)
	if err != nil {
		return nil, fmt.Errorf("decode prompt runtimes: %w", err)
	}
	doc.Runtimes = runtimes
	doc.RuntimeProfile, err = decodePromptRuntimeProfile(raw)
	if err != nil {
		return nil, fmt.Errorf("decode prompt runtime profile: %w", err)
	}
	if err := decodeSpecFrontmatter(raw, &doc.Spec); err != nil {
		return nil, fmt.Errorf("decode prompt frontmatter into spec: %w", err)
	}
	return doc, nil
}

func decodePromptRuntimeProfile(raw map[string]any) (string, error) {
	value, ok := raw["runtimeProfile"]
	if !ok {
		return "", nil
	}
	ref, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("runtimeProfile must be a string, got %T", value)
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("runtimeProfile must name a profile")
	}
	return ref, nil
}

func decodePromptRuntimes(raw map[string]any) ([]api.Model, error) {
	value, ok := raw["runtimes"]
	if !ok {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("runtimes must be a list")
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("runtimes must contain at least two entries")
	}
	runtimes := make([]api.Model, 0, len(values))
	for i, value := range values {
		encoded, err := yaml.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("runtime %d: encode: %w", i+1, err)
		}
		if _, compact := value.(string); compact {
			encoded, err = yaml.Marshal([]any{value})
			if err != nil {
				return nil, fmt.Errorf("runtime %d: encode compact value: %w", i+1, err)
			}
			var list api.ModelList
			if err := yaml.Unmarshal(encoded, &list); err != nil {
				return nil, fmt.Errorf("runtime %d: %w", i+1, err)
			}
			if len(list) != 1 {
				return nil, fmt.Errorf("runtime %d: expected one compact model", i+1)
			}
			runtimes = append(runtimes, list[0])
			continue
		}
		var runtime api.Model
		if err := validateDeclarationFields(encoded, runtime.DecodeFields()); err != nil {
			return nil, fmt.Errorf("runtime %d: %w", i+1, err)
		}
		if err := yaml.Unmarshal(encoded, &runtime); err != nil {
			return nil, fmt.Errorf("runtime %d: %w", i+1, err)
		}
		runtimes = append(runtimes, runtime)
	}
	return runtimes, nil
}

// String reserializes the document to .prompt text: "---\n<yaml>\n---\n<body>",
// or the body alone when there is no frontmatter. The output satisfies the
// dotprompt frontmatter grammar so Parse can read it back.
func (d *Document) String() (string, error) {
	if len(d.Frontmatter) == 0 {
		return d.Body, nil
	}
	y, err := yaml.Marshal(d.Frontmatter)
	if err != nil {
		return "", fmt.Errorf("marshal prompt frontmatter: %w", err)
	}
	return "---\n" + string(y) + "---\n" + d.Body, nil
}

// splitFrontmatter separates the YAML frontmatter from the body using the
// dotprompt grammar. A source with no frontmatter markers is treated as all
// body.
func splitFrontmatter(source string) (frontmatter, body string, hasFrontmatter bool) {
	if m := dp.FrontmatterAndBodyRegex.FindStringSubmatch(source); m != nil {
		return m[1], m[2], true
	}
	if m := dp.EmptyFrontmatterRegex.FindStringSubmatch(source); m != nil {
		return "", m[1], true
	}
	return "", source, false
}
