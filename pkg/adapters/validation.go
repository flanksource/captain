package adapters

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var ErrUnknownSchema = errors.New("unknown schema")

type ValidationIssue struct {
	InstancePath string `json:"instancePath"`
	SchemaPath   string `json:"schemaPath"`
	Message      string `json:"message"`
}

type ValidationResult struct {
	Valid  bool              `json:"valid"`
	Errors []ValidationIssue `json:"errors"`
}

var compiledResources = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	documents, err := Schemas()
	if err != nil {
		return nil, err
	}
	compiled := make(map[string]*jsonschema.Schema, len(documents))
	for _, document := range documents {
		body, err := jsonschema.UnmarshalJSON(bytes.NewReader(document.Schema))
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", document.ID, err)
		}
		compiler := jsonschema.NewCompiler()
		uri := "mem:///" + document.ID + ".json"
		if err := compiler.AddResource(uri, body); err != nil {
			return nil, fmt.Errorf("register %s: %w", document.ID, err)
		}
		schema, err := compiler.Compile(uri)
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", document.ID, err)
		}
		if _, exists := compiled[document.ID]; exists {
			return nil, fmt.Errorf("duplicate resource id %q", document.ID)
		}
		compiled[document.ID] = schema
	}
	return compiled, nil
})

// ValidateResource checks a value against its published schema without resolving or executing it.
func ValidateResource(id string, value any) (ValidationResult, error) {
	compiled, err := compiledResources()
	if err != nil {
		return ValidationResult{}, err
	}
	schema, exists := compiled[id]
	if !exists {
		return ValidationResult{}, fmt.Errorf("%w %q", ErrUnknownSchema, id)
	}
	result := ValidationResult{Valid: true, Errors: []ValidationIssue{}}
	if err := schema.Validate(value); err != nil {
		var invalid *jsonschema.ValidationError
		if !errors.As(err, &invalid) {
			return ValidationResult{}, fmt.Errorf("validate %s: %w", id, err)
		}
		result.Valid = false
		result.Errors = validationIssues(*invalid.DetailedOutput())
	}
	return result, nil
}

func validationIssues(output jsonschema.OutputUnit) []ValidationIssue {
	if len(output.Errors) == 0 && output.Error != nil {
		return []ValidationIssue{{InstancePath: output.InstanceLocation,
			SchemaPath: output.KeywordLocation, Message: output.Error.String()}}
	}
	var issues []ValidationIssue
	for _, child := range output.Errors {
		issues = append(issues, validationIssues(child)...)
	}
	return issues
}
