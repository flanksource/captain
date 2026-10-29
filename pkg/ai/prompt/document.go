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
	// Presets are the ordered reusable task specs selected by the prompt.
	Presets    []string
	PresetsSet bool
	// RuntimeProfile is retained for parsing compatibility. It is deprecated and
	// ignored by execution; callers should surface Warnings to users.
	RuntimeProfile string
	Warnings       []string
	// Body is the unrendered Handlebars template body.
	Body string
	// Templated reports that the authored frontmatter is a template, so
	// Frontmatter and Spec are its rendered view rather than its text.
	Templated bool
	// RawFrontmatter is the authored frontmatter text, verbatim.
	RawFrontmatter string

	// header is the authored source up to the body; declaredYAML is the
	// rendered view as Parse decoded it. String re-emits header for a templated
	// document whose Frontmatter is unchanged.
	header       string
	declaredYAML string
}

// Parse splits a .prompt source into frontmatter and body WITHOUT rendering the
// body, then decodes the spec-native frontmatter into Spec. A frontmatter whose
// YAML structure is templated is read as declared (see declaredSource). It
// fails loud on a broken frontmatter template, malformed YAML or a spec-native
// key the spec does not model — the opposite of the dotprompt library's
// ParseDocument, which prints and swallows YAML errors.
func Parse(source string) (*Document, error) {
	declared, err := declaredSource(source)
	if err != nil {
		return nil, fmt.Errorf("parse prompt frontmatter: %w", err)
	}
	return parseRendered(source, declared)
}

// declaredSource is source with its frontmatter as declared before any runtime
// data. Expressions inside YAML values render at run time and stay as
// authored. A frontmatter that is YAML only once rendered — its structure is
// templated, e.g. a `{{#if x}}` line adding a key — is rendered with no
// variables, so such blocks drop out and `{{x}}` renders empty.
func declaredSource(source string) (string, error) {
	authored, _, _ := splitFrontmatter(source)
	if !strings.Contains(authored, "{{") || yaml.Unmarshal([]byte(authored), new(any)) == nil {
		return source, nil
	}
	return RenderFrontmatter(source, map[string]any{})
}

// parseRendered decodes rendered, the source with its frontmatter already
// rendered, keeping the authored text of source for reserialization.
func parseRendered(source, rendered string) (*Document, error) {
	authored, body, _ := splitFrontmatter(source)
	frontmatter, _, hasFrontmatter := splitFrontmatter(rendered)
	doc := &Document{Body: body, RawFrontmatter: authored, header: source[:len(source)-len(body)]}
	if !hasFrontmatter || strings.TrimSpace(frontmatter) == "" {
		return doc, nil
	}
	raw := map[string]any{}
	if err := yaml.Unmarshal([]byte(frontmatter), &raw); err != nil {
		return nil, fmt.Errorf("parse prompt frontmatter: %w", err)
	}
	doc.Frontmatter = raw
	if frontmatter != authored {
		declaredYAML, err := yaml.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("marshal declared prompt frontmatter: %w", err)
		}
		doc.Templated, doc.declaredYAML = true, string(declaredYAML)
	}
	runtimes, err := decodePromptRuntimes(raw)
	if err != nil {
		return nil, fmt.Errorf("decode prompt runtimes: %w", err)
	}
	doc.Runtimes = runtimes
	doc.Presets, doc.PresetsSet, err = decodePromptPresets(raw)
	if err != nil {
		return nil, fmt.Errorf("decode prompt presets: %w", err)
	}
	doc.RuntimeProfile, err = decodePromptRuntimeProfile(raw)
	if err != nil {
		return nil, fmt.Errorf("decode prompt runtime profile: %w", err)
	}
	if doc.RuntimeProfile != "" {
		doc.Warnings = append(doc.Warnings, api.RuntimeProfileDeprecationWarning)
	}
	if err := decodeSpecFrontmatter(raw, &doc.Spec); err != nil {
		return nil, fmt.Errorf("decode prompt frontmatter into spec: %w", err)
	}
	return doc, nil
}

func decodePromptPresets(raw map[string]any) ([]string, bool, error) {
	value, ok := raw["presets"]
	if !ok {
		return nil, false, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, true, fmt.Errorf("presets must be a list")
	}
	presets := make([]string, 0, len(values))
	for i, value := range values {
		ref, ok := value.(string)
		if !ok {
			return nil, true, fmt.Errorf("preset %d must be a string, got %T", i+1, value)
		}
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return nil, true, fmt.Errorf("preset %d must name a preset", i+1)
		}
		presets = append(presets, ref)
	}
	return presets, true, nil
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
// dotprompt frontmatter grammar so Parse can read it back. A templated
// document re-emits its authored frontmatter; once its Frontmatter is edited it
// cannot be reserialized without losing the template, which is an error.
func (d *Document) String() (string, error) {
	if len(d.Frontmatter) == 0 && !d.Templated {
		return d.Body, nil
	}
	y, err := yaml.Marshal(d.Frontmatter)
	if err != nil {
		return "", fmt.Errorf("marshal prompt frontmatter: %w", err)
	}
	if d.Templated {
		if string(y) != d.declaredYAML {
			return "", fmt.Errorf("cannot reserialize edited templated frontmatter without losing its template; edit RawFrontmatter instead")
		}
		return d.header + d.Body, nil
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
