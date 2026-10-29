package prompt

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/mbleigh/raymond"
	"github.com/mbleigh/raymond/parser"
	"gopkg.in/yaml.v3"
)

type ValidationOptions struct {
	Data map[string]any
}

type Diagnostic struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
}

func SymlinkDiagnostic(err error) Diagnostic {
	message := err.Error()
	var pathError *fs.PathError
	if errors.As(err, &pathError) {
		message = pathError.Err.Error()
	}
	return Diagnostic{Rule: "promptfile/symlink", Severity: "warning", Message: "linked prompt unavailable: " + message}
}

var (
	frontmatterOpening = regexp.MustCompile(`^(?:(?:#[^\n]*|[ \t]*)\n)*---[ \t]*(?:\r?\n|$)`)
	diagnosticLine     = regexp.MustCompile(`(?i)line[: ]+([0-9]+)`)
	diagnosticField    = regexp.MustCompile(`field ([^ ]+) not found`)
)

// Validate checks authored declarations without executing the body or resolving runtime configuration.
func (t *Template) Validate(options ValidationOptions) []Diagnostic {
	frontmatter, body, hasFrontmatter := splitFrontmatter(t.source)
	if !hasFrontmatter && frontmatterOpening.MatchString(t.source) {
		return []Diagnostic{{Rule: "promptfile/frontmatter", Severity: "error", Message: "frontmatter is missing its closing --- delimiter", Line: 1}}
	}
	var diagnostics []Diagnostic
	bodyOffset := strings.Count(t.source[:len(t.source)-len(body)], "\n")
	if _, err := raymond.Parse(body); err != nil {
		diagnostics = append(diagnostics, validationError("promptfile/template", err, bodyOffset))
	}
	if !hasFrontmatter || strings.TrimSpace(frontmatter) == "" {
		return diagnostics
	}
	loc := frontmatterRe.FindStringSubmatchIndex(t.source)
	frontmatterOffset := strings.Count(t.source[:loc[2]], "\n")
	program, err := parser.Parse(deferHookExpressions(frontmatter))
	if err != nil {
		return append(diagnostics, validationError("promptfile/template", err, frontmatterOffset))
	}
	missing := missingFrontmatterVariables(program, options.Data)
	if len(missing) > 0 {
		return append(diagnostics, Diagnostic{Rule: "promptfile/variables", Severity: "warning", Line: frontmatterOffset + 1,
			Message: "frontmatter declaration checks deferred; supply --var or --vars for: " + strings.Join(missing, ", ")})
	}
	source, err := RenderFrontmatter(t.source, options.Data)
	if err != nil {
		return append(diagnostics, validationError("promptfile/frontmatter", err, frontmatterOffset))
	}
	declarations := validateDeclarations(source, t.dir)
	if source != t.source {
		for i := range declarations {
			declarations[i].Line, declarations[i].Column = 0, 0
			declarations[i].Message = "rendered frontmatter: " + declarations[i].Message
		}
	}
	return append(diagnostics, declarations...)
}

func validateDeclarations(source, dir string) []Diagnostic {
	frontmatter, _, _ := splitFrontmatter(source)
	if strings.TrimSpace(frontmatter) == "" {
		return nil
	}
	loc := frontmatterRe.FindStringSubmatchIndex(source)
	offset := strings.Count(source[:loc[2]], "\n")
	document, err := Parse(source)
	if err != nil {
		diagnostic := validationError("promptfile/frontmatter", err, offset)
		if field := diagnosticField.FindStringSubmatch(err.Error()); field != nil {
			diagnostic.Line, diagnostic.Column = declarationPosition(frontmatter, field[1])
			if diagnostic.Line > 0 {
				diagnostic.Line += offset
			}
		}
		return []Diagnostic{diagnostic}
	}
	var diagnostics []Diagnostic
	for _, warning := range document.Warnings {
		diagnostics = append(diagnostics, Diagnostic{Rule: "promptfile/deprecation", Severity: "warning", Message: warning})
	}
	if err := validateDeclaredSpec(document); err != nil {
		diagnostics = append(diagnostics, validationError("promptfile/spec", err, 0))
	}
	if err := validateHookPrompts(document.Spec, dir); err != nil {
		diagnostics = append(diagnostics, validationError("promptfile/hooks", err, 0))
	}
	if _, err := Load(source).Hook(); err != nil {
		diagnostics = append(diagnostics, validationError("promptfile/hook", err, offset))
	}
	if err := validateSchemaSections(document.Frontmatter); err != nil {
		diagnostics = append(diagnostics, validationError("promptfile/schema", err, offset))
	} else if err := validateDeclaredSchemas(source); err != nil {
		diagnostics = append(diagnostics, validationError("promptfile/schema", err, offset))
	}
	return append(diagnostics, validateInlineTools(document.Spec)...)
}

// validateInlineTools compiles each tool's schemas and flags declarations whose
// effect differs from what they appear to say: a retried destructive tool
// repeats its side effect, direct credentials inside the sandbox put real
// secrets there, and a tool kept away from secrets does not keep them from the
// agent when the run's own setup hands them over.
func validateInlineTools(spec api.Spec) []Diagnostic {
	var diagnostics []Diagnostic
	warn := func(format string, args ...any) {
		diagnostics = append(diagnostics, Diagnostic{Rule: "promptfile/tools", Severity: "warning", Message: fmt.Sprintf(format, args...)})
	}
	var runConnections []string
	if spec.Setup != nil {
		runConnections = api.DeclaredConnections(spec.Setup.Connections)
	}
	for _, name := range spec.Tools.Names() {
		tool := spec.Tools[name]
		if err := compileToolSchemas(tool); err != nil {
			diagnostics = append(diagnostics, Diagnostic{Rule: "promptfile/tools", Severity: "error", Message: fmt.Sprintf("tools.%s: %s", name, err)})
		}
		if tool.Destructive && tool.Retry != nil && tool.Retry.Attempts > 1 {
			warn("tools.%s is destructive and retried: a failed attempt may already have had its effect", name)
		}
		if tool.Placement == api.ToolPlacementSandbox && tool.Credentials == api.ToolCredentialsDirect {
			warn("tools.%s hands real credentials into the sandbox; credentials: proxied keeps them on the host", name)
		}
		if len(runConnections) > 0 && (tool.Credentials == api.ToolCredentialsNone || tool.Credentials == api.ToolCredentialsProxied) {
			warn("tools.%s is credentials: %s, but setup.connections (%s) still reach the agent; declare them on the tool instead",
				name, tool.Credentials, strings.Join(runConnections, ", "))
		}
	}
	return diagnostics
}

func validateDeclaredSpec(document *Document) error {
	if config, ok := document.Frontmatter["config"]; ok {
		values, ok := config.(map[string]any)
		if !ok {
			return fmt.Errorf("config must be a mapping")
		}
		if err := validateDotpromptConfig(values); err != nil {
			return err
		}
		applyModelConfig(values, &document.Spec)
	}
	if err := document.Spec.ValidateStructure(); err != nil {
		return err
	}
	for i, model := range document.Runtimes {
		if err := (api.Spec{Model: model}).ValidateStructure(); err != nil {
			return fmt.Errorf("runtime %d: %w", i+1, err)
		}
	}
	return nil
}

func validateDotpromptConfig(values map[string]any) error {
	for _, key := range []string{"maxOutputTokens", "temperature"} {
		if value, exists := values[key]; exists {
			number, ok := floatOf(value)
			if !ok {
				return fmt.Errorf("config.%s must be a number", key)
			}
			if math.IsNaN(number) || math.IsInf(number, 0) {
				return fmt.Errorf("config.%s must be finite", key)
			}
			if key == "maxOutputTokens" && (math.Trunc(number) != number || number >= float64(int(^uint(0)>>1))) {
				return fmt.Errorf("config.maxOutputTokens must be an integer within the supported range")
			}
		}
	}
	if value, exists := values["reasoning"]; exists {
		if _, ok := value.(string); !ok {
			return fmt.Errorf("config.reasoning must be a string")
		}
	}
	return nil
}

func validationError(rule string, err error, offset int) Diagnostic {
	diagnostic := Diagnostic{Rule: rule, Severity: "error", Message: err.Error()}
	if match := diagnosticLine.FindStringSubmatch(err.Error()); match != nil {
		diagnostic.Line, _ = strconv.Atoi(match[1])
		diagnostic.Line += offset
	}
	return diagnostic
}

func declarationPosition(source, field string) (int, int) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(source), &root); err != nil {
		return 0, 0
	}
	var visit func(*yaml.Node) (int, int)
	visit = func(node *yaml.Node) (int, int) {
		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Value == field {
					return node.Content[i].Line, node.Content[i].Column
				}
			}
		}
		for _, child := range node.Content {
			if line, column := visit(child); line > 0 {
				return line, column
			}
		}
		return 0, 0
	}
	return visit(&root)
}
