package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	dp "github.com/google/dotprompt/go/dotprompt"
	"github.com/santhosh-tekuri/jsonschema/v6"
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
		decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("%s.schema: %w", section.name, err)
		}
		compiler := jsonschema.NewCompiler()
		compiler.UseLoader(jsonschema.SchemeURLLoader{})
		resource := "mem:///captain/prompt/" + section.name + ".json"
		if err := compiler.AddResource(resource, decoded); err != nil {
			return fmt.Errorf("%s.schema: %w", section.name, err)
		}
		if _, err := compiler.Compile(resource); err != nil {
			return fmt.Errorf("%s.schema: %w", section.name, err)
		}
	}
	return nil
}
