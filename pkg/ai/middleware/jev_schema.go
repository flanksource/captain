package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	native "github.com/flanksource/captain/pkg/ai/jev"
	"github.com/flanksource/captain/pkg/api"
)

type jevPlan struct {
	request native.Request
	scalar  bool
}

func prepareJev(req ai.Request, model string) (ai.Request, jevPlan, error) {
	var questions map[string]native.Question
	var scalar bool
	switch req.Prompt.OutputSchemaType {
	case "", api.SchemaTypeJSONSchema:
		if req.Prompt.Schema != nil && len(req.Prompt.SchemaJSON) > 0 {
			return ai.Request{}, jevPlan{}, fmt.Errorf("jev accepts one JSON Schema source")
		}
		if len(req.Prompt.TypeSafeQuestions) > 0 {
			return ai.Request{}, jevPlan{}, fmt.Errorf("jev TypeSafe questions require outputSchemaType typesafe-schema")
		}
		schema, err := ai.SchemaJSONFor(req.Prompt)
		if err != nil {
			return ai.Request{}, jevPlan{}, fmt.Errorf("jev output JSON Schema: %w", err)
		}
		questions, scalar, err = jevQuestionsFromSchema(schema, req.Prompt)
		if err != nil {
			return ai.Request{}, jevPlan{}, err
		}
	case api.SchemaTypeTypeSafe:
		if req.Prompt.Schema != nil || len(req.Prompt.SchemaJSON) > 0 {
			return ai.Request{}, jevPlan{}, fmt.Errorf("jev TypeSafe questions cannot be combined with JSON Schema")
		}
		if err := json.Unmarshal(req.Prompt.TypeSafeQuestions, &questions); err != nil || len(questions) == 0 {
			return ai.Request{}, jevPlan{}, fmt.Errorf("jev typesafe-schema requires a non-empty questions object")
		}
		for name, question := range questions {
			if err := question.Validate(); err != nil {
				return ai.Request{}, jevPlan{}, fmt.Errorf("jev question %q: %w", name, err)
			}
			if prefix := jevInstructions(req.Prompt.System, req.Prompt.AppendSystem); prefix != "" {
				var original any
				if err := json.Unmarshal(question.Instructions, &original); err != nil {
					return ai.Request{}, jevPlan{}, err
				}
				question.Instructions, _ = json.Marshal(map[string]any{"context": prefix, "question": original})
				questions[name] = question
			}
		}
	default:
		return ai.Request{}, jevPlan{}, fmt.Errorf("jev does not support output schema type %q", req.Prompt.OutputSchemaType)
	}
	if strings.TrimSpace(req.Prompt.User) == "" {
		return ai.Request{}, jevPlan{}, fmt.Errorf("jev requires a non-empty state")
	}
	request := native.Request{Model: model, State: native.JSONString(req.Prompt.User), Questions: questions}
	raw, err := json.Marshal(request)
	if err != nil {
		return ai.Request{}, jevPlan{}, fmt.Errorf("jev encode native request: %w", err)
	}
	if _, err := native.ParseRequest(raw); err != nil {
		return ai.Request{}, jevPlan{}, err
	}
	req.Prompt.User = string(raw)
	req.Prompt.System, req.Prompt.AppendSystem = "", ""
	req.Prompt.Schema, req.Prompt.SchemaJSON, req.Prompt.TypeSafeQuestions = nil, nil, nil
	req.Prompt.OutputSchemaType = ""
	return req, jevPlan{request: request, scalar: scalar}, nil
}

func jevQuestionsFromSchema(raw json.RawMessage, prompt api.Prompt) (map[string]native.Question, bool, error) {
	if len(raw) == 0 {
		return nil, false, fmt.Errorf("jev requires an output JSON Schema or TypeSafe questions")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return nil, false, fmt.Errorf("jev output JSON Schema must be an object")
	}
	var kind string
	if err := json.Unmarshal(root["type"], &kind); err != nil {
		return nil, false, fmt.Errorf("jev output JSON Schema requires a type")
	}
	rootDescription, err := jevDescription(root, "$")
	if err != nil {
		return nil, false, err
	}
	if kind != "object" {
		question, err := jevQuestion(root, "$", jevInstructions(prompt.System, prompt.AppendSystem, rootDescription))
		if err != nil {
			return nil, false, err
		}
		return map[string]native.Question{"value": question}, true, nil
	}
	if err := jevKeywords(root, "$", "type", "properties", "required", "additionalProperties", "description", "title", "$schema", "$id", "$comment"); err != nil {
		return nil, false, err
	}
	if !bytes.Equal(bytes.TrimSpace(root["additionalProperties"]), []byte("false")) {
		return nil, false, fmt.Errorf("jev output JSON Schema requires additionalProperties: false")
	}
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(root["properties"], &properties); err != nil || len(properties) == 0 {
		return nil, false, fmt.Errorf("jev output JSON Schema requires non-empty properties")
	}
	var required []string
	if err := json.Unmarshal(root["required"], &required); err != nil || len(required) != len(properties) {
		return nil, false, fmt.Errorf("jev output JSON Schema required must list every property")
	}
	seen := map[string]bool{}
	for _, name := range required {
		if _, ok := properties[name]; !ok || seen[name] {
			return nil, false, fmt.Errorf("jev output JSON Schema required contains unknown or duplicate property %q", name)
		}
		seen[name] = true
	}
	questions := make(map[string]native.Question, len(properties))
	for name, rawProperty := range properties {
		if strings.TrimSpace(name) == "" {
			return nil, false, fmt.Errorf("jev output JSON Schema property names must be non-empty")
		}
		var node map[string]json.RawMessage
		if err := json.Unmarshal(rawProperty, &node); err != nil || node == nil {
			return nil, false, fmt.Errorf("jev output JSON Schema property %q must be an object", name)
		}
		description, err := jevDescription(node, "$.properties."+name)
		if err != nil {
			return nil, false, err
		}
		if description == "" {
			description = name
		}
		questions[name], err = jevQuestion(node, "$.properties."+name, jevInstructions(prompt.System, prompt.AppendSystem, rootDescription, description))
		if err != nil {
			return nil, false, err
		}
	}
	return questions, false, nil
}

func jevQuestion(node map[string]json.RawMessage, path, instructions string) (native.Question, error) {
	var kind string
	if err := json.Unmarshal(node["type"], &kind); err != nil {
		return native.Question{}, fmt.Errorf("jev output JSON Schema %s requires type", path)
	}
	question := native.Question{Instructions: native.JSONString(instructions)}
	switch kind {
	case "string":
		if err := jevKeywords(node, path, "type", "enum", "description", "title", "$schema", "$id", "$comment"); err != nil {
			return native.Question{}, err
		}
		var values []string
		if err := json.Unmarshal(node["enum"], &values); err != nil || len(values) == 0 || len(values) > 255 {
			return native.Question{}, fmt.Errorf("jev output JSON Schema %s.enum must contain 1 to 255 strings", path)
		}
		seen := map[string]bool{}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || seen[value] {
				return native.Question{}, fmt.Errorf("jev output JSON Schema %s.enum has an empty or duplicate choice", path)
			}
			seen[value] = true
		}
		question.Type, question.Criteria = "choice", native.JSONNullCriteria(values)
	case "number":
		if err := jevKeywords(node, path, "type", "minimum", "maximum", "description", "title", "x-typesafe-criteria", "$schema", "$id", "$comment"); err != nil {
			return native.Question{}, err
		}
		var minimum, maximum float64
		if err := json.Unmarshal(node["minimum"], &minimum); err != nil || minimum != 0 {
			return native.Question{}, fmt.Errorf("jev output JSON Schema %s.minimum must be 0", path)
		}
		if err := json.Unmarshal(node["maximum"], &maximum); err != nil {
			return native.Question{}, fmt.Errorf("jev output JSON Schema %s.maximum is required", path)
		}
		if len(node["x-typesafe-criteria"]) > 0 {
			var levels []json.RawMessage
			if err := json.Unmarshal(node["x-typesafe-criteria"], &levels); err != nil || len(levels) < 2 || len(levels) > 10 || maximum != float64(len(levels)-1) {
				return native.Question{}, fmt.Errorf("jev output JSON Schema %s score requires 2 to 10 criteria and maximum equal to the final level index", path)
			}
			question.Type, question.Criteria = "score", node["x-typesafe-criteria"]
		} else if maximum == 1 {
			question.Type = "noul"
		} else {
			return native.Question{}, fmt.Errorf("jev output JSON Schema %s number requires a Score rubric or maximum 1 for Noul", path)
		}
	default:
		return native.Question{}, fmt.Errorf("jev output JSON Schema %s has unsupported type %q", path, kind)
	}
	if err := question.Validate(); err != nil {
		return native.Question{}, fmt.Errorf("jev output JSON Schema %s: %w", path, err)
	}
	return question, nil
}

func jevDescription(node map[string]json.RawMessage, path string) (string, error) {
	if len(node["description"]) == 0 {
		return "", nil
	}
	var description string
	if err := json.Unmarshal(node["description"], &description); err != nil || bytes.Equal(bytes.TrimSpace(node["description"]), []byte("null")) {
		return "", fmt.Errorf("jev output JSON Schema %s.description must be a string", path)
	}
	return description, nil
}

func jevKeywords(node map[string]json.RawMessage, path string, allowed ...string) error {
	for key := range node {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("jev output JSON Schema %s uses unsupported keyword %q", path, key)
		}
	}
	return nil
}

func jevInstructions(parts ...string) string {
	var nonempty []string
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			nonempty = append(nonempty, trimmed)
		}
	}
	return strings.Join(nonempty, "\n")
}
