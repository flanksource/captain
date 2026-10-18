package prompt

import (
	"encoding/json"
	"fmt"
	"strings"

	native "github.com/flanksource/captain/pkg/ai/jev"
	"github.com/flanksource/captain/pkg/api"
	"gopkg.in/yaml.v3"
)

type SchemaDeclarations struct {
	InputType         api.SchemaType
	OutputType        api.SchemaType
	TypeSafeQuestions json.RawMessage
}

// PrepareSchemaSource keeps native TypeSafe questions out of dotprompt's JSON Schema parser.
func PrepareSchemaSource(source string) (string, SchemaDeclarations, error) {
	loc := frontmatterRe.FindStringSubmatchIndex(source)
	if loc == nil {
		return source, SchemaDeclarations{}, nil
	}
	var frontmatter map[string]any
	if err := yaml.Unmarshal([]byte(source[loc[2]:loc[3]]), &frontmatter); err != nil {
		return "", SchemaDeclarations{}, fmt.Errorf("parse schema declarations: %w", err)
	}
	input, err := schemaSection(frontmatter, "input")
	if err != nil {
		return "", SchemaDeclarations{}, err
	}
	output, err := schemaSection(frontmatter, "output")
	if err != nil {
		return "", SchemaDeclarations{}, err
	}
	declarations := SchemaDeclarations{}
	if input != nil {
		declarations.InputType, err = schemaType(input, "input")
		if err != nil {
			return "", SchemaDeclarations{}, err
		}
		if err := declarations.InputType.ValidateInput(); err != nil {
			return "", SchemaDeclarations{}, err
		}
	}
	if output != nil {
		declarations.OutputType, err = schemaType(output, "output")
		if err != nil {
			return "", SchemaDeclarations{}, err
		}
		if err := declarations.OutputType.ValidateOutput(); err != nil {
			return "", SchemaDeclarations{}, err
		}
	}
	if declarations.OutputType != api.SchemaTypeTypeSafe {
		return source, declarations, nil
	}
	questions, ok := output["schema"].(map[string]any)
	if !ok || len(questions) == 0 {
		return "", SchemaDeclarations{}, fmt.Errorf("output.schema must contain TypeSafe questions")
	}
	declarations.TypeSafeQuestions, err = json.Marshal(questions)
	if err != nil {
		return "", SchemaDeclarations{}, fmt.Errorf("encode TypeSafe questions: %w", err)
	}
	var typed map[string]native.Question
	if err := json.Unmarshal(declarations.TypeSafeQuestions, &typed); err != nil {
		return "", SchemaDeclarations{}, fmt.Errorf("decode TypeSafe questions: %w", err)
	}
	for name, question := range typed {
		if strings.TrimSpace(name) == "" {
			return "", SchemaDeclarations{}, fmt.Errorf("TypeSafe question names must be non-empty")
		}
		if err := question.Validate(); err != nil {
			return "", SchemaDeclarations{}, fmt.Errorf("TypeSafe question %q: %w", name, err)
		}
	}
	delete(output, "schema")
	frontmatterRaw, err := yaml.Marshal(frontmatter)
	if err != nil {
		return "", SchemaDeclarations{}, fmt.Errorf("encode TypeSafe prompt frontmatter: %w", err)
	}
	return source[:loc[2]] + string(frontmatterRaw) + source[loc[3]:], declarations, nil
}

func schemaSection(frontmatter map[string]any, name string) (map[string]any, error) {
	raw, exists := frontmatter[name]
	if !exists {
		return nil, nil
	}
	section, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a mapping", name)
	}
	return section, nil
}

func schemaType(section map[string]any, name string) (api.SchemaType, error) {
	raw, exists := section["schemaType"]
	if !exists {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s.schemaType must be a string", name)
	}
	return api.SchemaType(value), nil
}
