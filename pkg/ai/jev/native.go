package jev

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

type Question struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

func (q *Question) UnmarshalJSON(raw []byte) error {
	type plain Question
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*q = Question(value)
	return nil
}

type Request struct {
	Model     string              `json:"model"`
	State     json.RawMessage     `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type Answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        *string            `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
}

type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens  *int `json:"input_tokens"`
		OutputTokens *int `json:"output_tokens"`
	} `json:"usage"`
}

func ParseRequest(raw []byte) (Request, error) {
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("decode TypeSafe request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Request{}, fmt.Errorf("decode TypeSafe request: unexpected trailing JSON value")
		}
		return Request{}, fmt.Errorf("decode TypeSafe request: trailing JSON: %w", err)
	}
	if strings.TrimSpace(request.Model) == "" || len(request.Questions) == 0 {
		return Request{}, fmt.Errorf("TypeSafe request requires model and questions")
	}
	var state any
	if err := json.Unmarshal(request.State, &state); err != nil {
		return Request{}, fmt.Errorf("TypeSafe state: %w", err)
	}
	switch state.(type) {
	case string, map[string]any, []any:
	default:
		return Request{}, fmt.Errorf("TypeSafe state must be a string, object, or array")
	}
	for name, question := range request.Questions {
		if strings.TrimSpace(name) == "" {
			return Request{}, fmt.Errorf("TypeSafe question names must be non-empty")
		}
		if err := question.Validate(); err != nil {
			return Request{}, fmt.Errorf("TypeSafe question %q: %w", name, err)
		}
	}
	return request, nil
}

func (q Question) Validate() error {
	var instructions any
	if err := json.Unmarshal(q.Instructions, &instructions); err != nil {
		return fmt.Errorf("instructions: %w", err)
	}
	switch instructions.(type) {
	case string, map[string]any, []any:
	default:
		return fmt.Errorf("instructions must be a string, object, or array")
	}
	switch q.Type {
	case "noul":
		if len(q.Criteria) == 0 {
			return nil
		}
		var criteria map[string]json.RawMessage
		if err := json.Unmarshal(q.Criteria, &criteria); err != nil || criteria == nil {
			return fmt.Errorf("noul criteria must be an object")
		}
		for key, description := range criteria {
			if key != "true" && key != "false" {
				return fmt.Errorf("noul criteria has unknown key %q", key)
			}
			if err := validateDescription(description, false); err != nil {
				return fmt.Errorf("noul criteria %q: %w", key, err)
			}
		}
	case "choice":
		var criteria map[string]json.RawMessage
		if err := json.Unmarshal(q.Criteria, &criteria); err != nil || len(criteria) == 0 || len(criteria) > 255 {
			return fmt.Errorf("choice criteria must contain 1 to 255 options")
		}
		for option, description := range criteria {
			if strings.TrimSpace(option) == "" {
				return fmt.Errorf("choice options must be non-empty")
			}
			if err := validateDescription(description, true); err != nil {
				return fmt.Errorf("choice criteria %q: %w", option, err)
			}
		}
	case "score":
		var criteria []json.RawMessage
		if err := json.Unmarshal(q.Criteria, &criteria); err != nil || len(criteria) < 2 || len(criteria) > 10 {
			return fmt.Errorf("score criteria must contain 2 to 10 ordered levels")
		}
		for index, level := range criteria {
			var value any
			if err := json.Unmarshal(level, &value); err != nil {
				return fmt.Errorf("score level %d: %w", index, err)
			}
			switch value.(type) {
			case string, map[string]any, []any:
			default:
				return fmt.Errorf("score level %d must be a string, object, or array", index)
			}
		}
	default:
		return fmt.Errorf("unsupported type %q", q.Type)
	}
	return nil
}

func validateDescription(raw json.RawMessage, nullable bool) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	switch value.(type) {
	case string, map[string]any, []any:
		return nil
	case nil:
		if nullable {
			return nil
		}
	}
	return fmt.Errorf("description must be a string, object, or array")
}

func ParseResponse(raw []byte, request Request) (Response, map[string]any, error) {
	var response Response
	if err := json.Unmarshal(raw, &response); err != nil {
		return Response{}, nil, fmt.Errorf("decode TypeSafe response: %w", err)
	}
	if response.Model == "" || response.Usage.InputTokens == nil || response.Usage.OutputTokens == nil || *response.Usage.InputTokens < 0 || *response.Usage.OutputTokens < 0 {
		return Response{}, nil, fmt.Errorf("TypeSafe response requires model and non-negative usage")
	}
	if len(response.Answers) != len(request.Questions) {
		return Response{}, nil, fmt.Errorf("TypeSafe returned %d answers for %d questions", len(response.Answers), len(request.Questions))
	}
	values := make(map[string]any, len(request.Questions))
	for name, question := range request.Questions {
		answer, found := response.Answers[name]
		if !found || answer.Type != question.Type {
			return Response{}, nil, fmt.Errorf("TypeSafe answer %q has wrong or missing type", name)
		}
		value, err := projectAnswer(question, answer)
		if err != nil {
			return Response{}, nil, fmt.Errorf("TypeSafe answer %q: %w", name, err)
		}
		values[name] = value
	}
	return response, values, nil
}

func projectAnswer(question Question, answer Answer) (any, error) {
	switch question.Type {
	case "noul":
		if answer.Noul == nil || !unit(*answer.Noul) {
			return nil, fmt.Errorf("noul must be between 0 and 1")
		}
		return *answer.Noul, nil
	case "choice":
		var options map[string]json.RawMessage
		if err := json.Unmarshal(question.Criteria, &options); err != nil {
			return nil, err
		}
		if answer.Choice == nil || answer.Confidence == nil || !unit(*answer.Confidence) {
			return nil, fmt.Errorf("choice or confidence is missing or invalid")
		}
		if _, ok := options[*answer.Choice]; !ok {
			return nil, fmt.Errorf("choice %q is outside the requested options", *answer.Choice)
		}
		if err := validateProbabilities(answer.Probabilities, len(options)); err != nil {
			return nil, err
		}
		for option := range options {
			if _, ok := answer.Probabilities[option]; !ok {
				return nil, fmt.Errorf("missing probability for %q", option)
			}
		}
		return *answer.Choice, nil
	case "score":
		var levels []json.RawMessage
		if err := json.Unmarshal(question.Criteria, &levels); err != nil {
			return nil, err
		}
		if answer.Score == nil || answer.Confidence == nil || !unit(*answer.Confidence) || math.IsNaN(*answer.Score) || *answer.Score < 0 || *answer.Score > float64(len(levels)-1) {
			return nil, fmt.Errorf("score or confidence is missing or out of range")
		}
		if err := validateProbabilities(answer.Probabilities, len(levels)); err != nil {
			return nil, err
		}
		if len(answer.Legend) != len(levels) {
			return nil, fmt.Errorf("legend has %d levels, expected %d", len(answer.Legend), len(levels))
		}
		for index := range levels {
			key := strconv.Itoa(index)
			if _, ok := answer.Legend[key]; !ok {
				return nil, fmt.Errorf("legend lacks level %s", key)
			}
			if _, ok := answer.Probabilities[key]; !ok {
				return nil, fmt.Errorf("missing probability for level %s", key)
			}
		}
		return *answer.Score, nil
	}
	return nil, fmt.Errorf("unsupported answer type %q", answer.Type)
}

func validateProbabilities(probabilities map[string]float64, count int) error {
	if len(probabilities) != count {
		return fmt.Errorf("probabilities has %d entries, expected %d", len(probabilities), count)
	}
	var total float64
	for key, probability := range probabilities {
		if !unit(probability) {
			return fmt.Errorf("invalid probability for %q", key)
		}
		total += probability
	}
	if math.Abs(total-1) > 0.02 {
		return fmt.Errorf("probabilities sum to %g, expected 1", total)
	}
	return nil
}

func unit(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func JSONString(value string) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func JSONNullCriteria(options []string) json.RawMessage {
	criteria := make(map[string]any, len(options))
	for _, option := range options {
		criteria[option] = nil
	}
	raw, _ := json.Marshal(criteria)
	return raw
}
