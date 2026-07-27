package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// WithSchemaPrompt makes a structured-output request runnable on a backend that
// cannot enforce a schema natively (cmux, gemini-cli) by appending a JSON-only
// schema instruction to the user prompt and clearing the native schema fields so
// the run is a plain text turn. The returned schema is what
// ValidatedStructuredData checks the reply against once the run finishes.
func WithSchemaPrompt(req Request) (Request, json.RawMessage, error) {
	schema, err := SchemaJSONFor(req.Prompt)
	if err != nil {
		return req, nil, err
	}
	if len(schema) > 0 {
		req.Prompt.User = strings.TrimRight(req.Prompt.User, "\n") + "\n\n" + SchemaInstruction(string(schema))
	}
	req.Prompt.Schema = nil
	req.Prompt.SchemaJSON = nil
	return req, schema, nil
}

// ValidatedStructuredData extracts the JSON object a WithSchemaPrompt run asked
// for and validates it against the schema. A native terminal outcome (plan exit,
// ask) means the run ended on control flow rather than the requested answer, so
// there is nothing to extract.
func ValidatedStructuredData(schema json.RawMessage, text string, outcome *TerminalOutcome) (json.RawMessage, error) {
	if len(schema) == 0 || outcome != nil {
		return nil, nil
	}
	object, ok := ExtractJSONObject(text)
	if !ok {
		return nil, fmt.Errorf("%w: response carried no JSON object", ErrSchemaValidation)
	}
	violations, err := ValidateStructuredJSON(schema, object)
	if err != nil {
		return nil, err
	}
	if violations != "" {
		return nil, fmt.Errorf("%w: %s", ErrSchemaValidation, violations)
	}
	return json.RawMessage(object), nil
}
