package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// maxSchemaRetries bounds the number of fix-up re-asks the repair strictness makes
// when a structured response fails schema validation before giving up with a hard
// error.
const maxSchemaRetries = 2

// validatingProvider validates a structured-output response against the request's
// JSON schema and applies the prompt's SchemaStrictness policy: warning (log and
// continue), error (fail), repair (re-ask the model with the validation errors up to
// maxSchemaRetries times, then fail; a streamed turn is re-asked in its own session). It is a no-op unless the prompt sets both a schema and a strictness
// mode; Anthropic native structured-output backends default to repair validation
// because their provider-facing schema is intentionally stripped down.
type validatingProvider struct {
	provider ai.Provider
	cfg      ai.Config
}

func (v *validatingProvider) GetModel() string       { return v.provider.GetModel() }
func (v *validatingProvider) GetRuntime() ai.Runtime { return v.provider.GetRuntime() }
func (v *validatingProvider) Unwrap() ai.Provider    { return v.provider }

// WithSchemaValidation validates structured responses against the request schema
// and enforces api.Prompt.SchemaStrictness.
func WithSchemaValidation(cfg ...ai.Config) Option {
	return func(p ai.Provider) (ai.Provider, error) {
		var c ai.Config
		if len(cfg) > 0 {
			c = cfg[0]
		}
		return &validatingProvider{provider: p, cfg: c}, nil
	}
}

func (v *validatingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	strictness := v.effectiveStrictness(req)
	if v.provider.GetRuntime().Provider == "jev" && strictness == api.SchemaStrictnessRepair {
		return nil, fmt.Errorf("jev does not support schema repair retries")
	}
	resp, err := v.provider.Execute(ctx, req)
	if err != nil {
		// A provider that validates the model's structured output during generation
		// (e.g. genkit's constrained output) rejects it itself, returning the
		// rejected answer when it has one. Under "repair" that rejection is
		// recoverable: re-ask the model with the schema errors and the answer
		// instead of failing outright.
		if strictness == api.SchemaStrictnessRepair && errors.Is(err, ai.ErrSchemaValidation) {
			if schema, serr := ai.SchemaJSONFor(req.Prompt); serr == nil && len(schema) > 0 {
				return v.retryWithFeedback(ctx, req, schema, err.Error(), resp)
			}
		}
		return resp, err
	}

	if strictness == api.SchemaStrictnessNone {
		return resp, nil
	}
	schema, err := ai.SchemaJSONFor(req.Prompt)
	if err != nil || len(schema) == 0 {
		return resp, err
	}

	verrs, err := v.validateResponse(schema, resp)
	if err != nil {
		return resp, err
	}
	if verrs == "" {
		return resp, nil
	}

	switch strictness {
	case api.SchemaStrictnessWarning:
		log.Warnf("schema validation failed (%s/%s): %s", v.provider.GetRuntime(), v.provider.GetModel(), verrs)
		return resp, nil
	case api.SchemaStrictnessRepair:
		return v.retryWithFeedback(ctx, req, schema, verrs, resp)
	case api.SchemaStrictnessError:
		return resp, fmt.Errorf("%w: %s", ai.ErrSchemaValidation, verrs)
	default:
		return resp, fmt.Errorf("%w: unknown schemaStrictness %q: %s", ai.ErrSchemaValidation, strictness, verrs)
	}
}

func (v *validatingProvider) effectiveStrictness(req ai.Request) api.SchemaStrictness {
	switch req.Prompt.SchemaStrictness.Canonical() {
	case api.SchemaStrictnessDisabled:
		return api.SchemaStrictnessNone
	case api.SchemaStrictnessNone:
		runtime := v.provider.GetRuntime()
		if runtime.Provider == "jev" && (req.Prompt.Schema != nil || len(req.Prompt.SchemaJSON) > 0) {
			return api.SchemaStrictnessError
		}
		provider, _ := runtime.ModelProvider()
		if req.Prompt.HasSchema() && ai.UsesAnthropicSchemaSubset(provider, runtime.Mode) {
			return api.SchemaStrictnessRepair
		}
		return api.SchemaStrictnessNone
	default:
		return req.Prompt.SchemaStrictness.Canonical()
	}
}

// retryWithFeedback re-asks the model up to maxSchemaRetries times, feeding back
// the schema validation errors (and the previous response when we have one) so it
// can correct itself. It serves both failure modes: a post-response validation
// failure (prev is the offending response) and a provider-side rejection during
// generation (prev is the rejected answer, or nil when the provider returned
// none). A response that is
// still invalid after the last attempt is a hard error.
func (v *validatingProvider) retryWithFeedback(ctx context.Context, req ai.Request, schema json.RawMessage, verrs string, prev *ai.Response) (*ai.Response, error) {
	var usage ai.Usage
	var cost float64
	if prev != nil {
		usage, cost = prev.Usage, prev.CostUSD
	}
	for attempt := 1; attempt <= maxSchemaRetries; attempt++ {
		if prev != nil && prev.Text != "" {
			ai.ReportEvent(ctx, ai.Event{Kind: ai.EventText, Text: prev.Text, Model: prev.Model, Rejected: true})
		}
		ai.ReportEvent(ctx, ai.Event{Kind: ai.EventSystem, Text: fmt.Sprintf("schema validation failed; repairing response (%d/%d): %s", attempt, maxSchemaRetries, verrs)})
		resp, err := v.executeRepair(ctx, req, schema, verrs, prev, attempt)
		if resp != nil {
			usage, cost = usage.Add(resp.Usage), cost+resp.CostUSD
			resp.Usage, resp.CostUSD = usage, cost
		} else if err != nil {
			resp = prev
		}
		if err != nil {
			// The provider rejected the corrected output too; feed the new errors
			// back and keep trying until the retry budget is exhausted.
			if errors.Is(err, ai.ErrSchemaValidation) && attempt < maxSchemaRetries {
				verrs, prev = err.Error(), resp
				continue
			}
			return resp, err
		}

		verrs, err = v.validateResponse(schema, resp)
		if err != nil {
			return resp, err
		}
		if verrs == "" {
			return resp, nil
		}
		prev = resp
	}
	return prev, fmt.Errorf("%w: still invalid after %d repair attempts: %s", ai.ErrSchemaValidation, maxSchemaRetries, verrs)
}

// responseJSON extracts the raw structured JSON a provider returned: the decoded
// json.RawMessage when present, else the raw response text.
func responseJSON(resp *ai.Response) string {
	if resp == nil {
		return ""
	}
	if raw, ok := resp.StructuredData.(json.RawMessage); ok && len(raw) > 0 {
		return string(raw)
	}
	if resp.StructuredData != nil {
		if data, err := json.Marshal(resp.StructuredData); err == nil && string(data) != "null" {
			return string(data)
		}
	}
	return resp.Text
}

// validateResponse validates the provider's structured JSON against schema. It
// returns a human-readable joined error string (empty when the response conforms),
// plus a hard error only when the schema or document could not be loaded/parsed.
func validateResponse(schema json.RawMessage, resp *ai.Response) (string, error) {
	violations, err := ai.ValidateStructuredJSON(schema, responseJSON(resp))
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) && json.Valid(schema) {
		if ai.IsMarkdown(responseJSON(resp)) {
			return "expected JSON matching the schema, received Markdown: " + err.Error(), nil
		}
		return err.Error(), nil
	}
	return violations, err
}

func (v *validatingProvider) validateResponse(schema json.RawMessage, resp *ai.Response) (string, error) {
	if v.provider.GetRuntime().Provider != "jev" {
		return validateResponse(schema, resp)
	}
	var envelope struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal([]byte(responseJSON(resp)), &envelope); err != nil || len(envelope.Value) == 0 {
		return "", fmt.Errorf("jev response is missing a valid value envelope")
	}
	return ai.ValidateStructuredJSON(schema, string(envelope.Value))
}
