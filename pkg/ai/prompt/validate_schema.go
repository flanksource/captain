package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	dp "github.com/google/dotprompt/go/dotprompt"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/flanksource/captain/pkg/ai/tools/exectool"
	"github.com/flanksource/captain/pkg/api"
)

func validateSchemaSections(frontmatter map[string]any) error {
	for _, name := range []string{"input", "output"} {
		section, err := schemaSection(frontmatter, name)
		if err != nil {
			return err
		}
		allowed := []string{"schemaType", "schema", "format"}
		if name == "input" {
			allowed = []string{"schemaType", "schema", "default"}
		}
		for key := range section {
			if !slices.Contains(allowed, key) {
				return fmt.Errorf("unknown %s field %q", name, key)
			}
		}
		if schema, exists := section["schema"]; exists {
			switch schema.(type) {
			case string, map[string]any:
			default:
				return fmt.Errorf("%s.schema must be a mapping or Picoschema string", name)
			}
		}
		if defaults, exists := section["default"]; exists {
			if _, ok := defaults.(map[string]any); !ok {
				return fmt.Errorf("input.default must be a mapping")
			}
		}
		if format, exists := section["format"]; exists {
			if _, ok := format.(string); !ok {
				return fmt.Errorf("output.format must be a string")
			}
		}
	}
	return nil
}

func validateDeclaredSchemas(source string) error {
	prepared, _, err := PrepareSchemaSource(source)
	if err != nil {
		return err
	}
	metadata, err := dp.NewDotprompt(nil).RenderMetadata(prepared, nil)
	if err != nil {
		return err
	}
	for _, section := range []struct {
		name   string
		schema dp.Schema
	}{{"input", metadata.Input.Schema}, {"output", metadata.Output.Schema}} {
		if section.schema == nil {
			continue
		}
		raw, err := json.Marshal(section.schema)
		if err != nil {
			return fmt.Errorf("%s.schema: %w", section.name, err)
		}
		if err := compileSchema(section.name, raw); err != nil {
			return fmt.Errorf("%s.schema: %w", section.name, err)
		}
	}
	return nil
}

// compileToolSchemas converts an inline tool's input and output exactly as
// the run will, then compiles them like the prompt's own sections.
func compileToolSchemas(tool api.InlineTool) error {
	input, err := exectool.InputSchema(tool.Input)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("input schema: %w", err)
	}
	if err := compileSchema("tool-input", raw); err != nil {
		return fmt.Errorf("input schema: %w", err)
	}
	output, err := exectool.OutputSchema(tool.Output)
	if err != nil || output == nil {
		return err
	}
	if err := compileSchema("tool-output", output); err != nil {
		return fmt.Errorf("output schema: %w", err)
	}
	return nil
}

func compileSchema(name string, raw []byte) error {
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	resource := "mem:///captain/prompt/" + name + ".json"
	if err := compiler.AddResource(resource, decoded); err != nil {
		return err
	}
	_, err = compiler.Compile(resource)
	return err
}
