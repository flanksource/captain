package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	ErrUnknownSchema = errors.New("unknown schema")
	// ErrInvalidSchema marks a carried schema that cannot be compiled.
	ErrInvalidSchema = errors.New("invalid schema")
)

type ValidationIssue struct {
	InstancePath string `json:"instancePath"`
	SchemaPath   string `json:"schemaPath"`
	Message      string `json:"message"`
}

type ValidationResult struct {
	Valid  bool              `json:"valid"`
	Errors []ValidationIssue `json:"errors"`
}

// ValidationRequest names the document a value is checked against. Schema carries
// a published document's schema (native provider resources are not embedded);
// without it the ID must name an embedded catalog document.
type ValidationRequest struct {
	ID     string
	Schema json.RawMessage
	Value  any
}

var compiledResources = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	documents, err := Schemas()
	if err != nil {
		return nil, err
	}
	compiled := make(map[string]*jsonschema.Schema, len(documents))
	for _, document := range documents {
		if _, exists := compiled[document.ID]; exists {
			return nil, fmt.Errorf("duplicate resource id %q", document.ID)
		}
		schema, err := compileSchema(document.ID, document.Schema)
		if err != nil {
			return nil, err
		}
		compiled[document.ID] = schema
	}
	return compiled, nil
})

// compileSchema compiles a self-contained schema; external references are never loaded.
func compileSchema(id string, raw json.RawMessage) (*jsonschema.Schema, error) {
	body, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", id, err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	uri := "mem:///" + id + ".json"
	if err := compiler.AddResource(uri, body); err != nil {
		return nil, fmt.Errorf("register %s: %w", id, err)
	}
	schema, err := compiler.Compile(uri)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", id, err)
	}
	return schema, nil
}

// ValidateResource checks a value against its schema without resolving or executing it.
func ValidateResource(request ValidationRequest) (ValidationResult, error) {
	schema, err := requestSchema(request)
	if err != nil {
		return ValidationResult{}, err
	}
	result := ValidationResult{Valid: true, Errors: []ValidationIssue{}}
	if err := schema.Validate(request.Value); err != nil {
		var invalid *jsonschema.ValidationError
		if !errors.As(err, &invalid) {
			return ValidationResult{}, fmt.Errorf("validate %s: %w", request.ID, err)
		}
		result.Valid = false
		result.Errors = validationIssues(*invalid.DetailedOutput())
	}
	return result, nil
}

func requestSchema(request ValidationRequest) (*jsonschema.Schema, error) {
	if len(request.Schema) > 0 {
		schema, err := compileSchema(request.ID, request.Schema)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidSchema, err)
		}
		return schema, nil
	}
	compiled, err := compiledResources()
	if err != nil {
		return nil, err
	}
	schema, exists := compiled[request.ID]
	if !exists {
		return nil, fmt.Errorf("%w %q", ErrUnknownSchema, request.ID)
	}
	return schema, nil
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
