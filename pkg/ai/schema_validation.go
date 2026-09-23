package ai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

const maxValidationResponseChars = 500

// ValidateStructuredJSON returns joined schema violations, or a hard error when
// the schema or JSON document cannot be evaluated.
func ValidateStructuredJSON(schema json.RawMessage, document string) (string, error) {
	if len(schema) == 0 {
		return "", fmt.Errorf("%w: schema is required", ErrSchemaValidation)
	}
	if strings.TrimSpace(document) == "" {
		return "response carried no JSON to validate", nil
	}
	result, err := gojsonschema.Validate(gojsonschema.NewBytesLoader(schema), gojsonschema.NewStringLoader(document))
	if err != nil {
		return "", fmt.Errorf("%w: validation could not run: %v (response preview, first %d chars: %q)",
			ErrSchemaValidation, err, maxValidationResponseChars, validationResponsePreview(document))
	}
	if result.Valid() {
		return "", nil
	}
	messages := make([]string, 0, len(result.Errors()))
	for _, validationErr := range result.Errors() {
		messages = append(messages, validationErr.String())
	}
	return strings.Join(messages, "; "), nil
}

func validationResponsePreview(document string) string {
	preview := []rune(document)
	if len(preview) > maxValidationResponseChars {
		preview = preview[:maxValidationResponseChars]
	}
	return string(preview)
}
