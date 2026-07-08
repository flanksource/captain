package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/xeipuuv/gojsonschema"
)

// maxSchemaRetries bounds the number of fix-up re-asks the retry strictness makes
// when a structured response fails schema validation before giving up with a hard
// error.
const maxSchemaRetries = 2

// validatingProvider validates a structured-output response against the request's
// JSON schema and applies the prompt's SchemaStrictness policy: warning (log and
// continue), error (fail), retry (re-ask the model once with the validation error,
// then fail). It is a no-op unless the prompt sets both a schema and a strictness
// mode, so it is safe to keep in the default middleware stack.
type validatingProvider struct {
	provider ai.Provider
}

func (v *validatingProvider) GetModel() string       { return v.provider.GetModel() }
func (v *validatingProvider) GetBackend() ai.Backend { return v.provider.GetBackend() }

// WithSchemaValidation validates structured responses against the request schema
// and enforces api.Prompt.SchemaStrictness.
func WithSchemaValidation() Option {
	return func(p ai.Provider) (ai.Provider, error) {
		return &validatingProvider{provider: p}, nil
	}
}

func (v *validatingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	resp, err := v.provider.Execute(ctx, req)
	strictness := req.Prompt.SchemaStrictness
	if err != nil {
		// A provider that validates the model's structured output during generation
		// (e.g. genkit's constrained output) rejects it before we ever see a
		// response. Under "retry" that rejection is recoverable: re-ask the model
		// with the schema errors instead of failing outright.
		if strictness == api.SchemaStrictnessRetry && errors.Is(err, ai.ErrSchemaValidation) {
			if schema, serr := ai.SchemaJSONFor(req.Prompt); serr == nil && len(schema) > 0 {
				return v.retryWithFeedback(ctx, req, schema, err.Error(), nil)
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

	verrs, err := validateResponse(schema, resp)
	if err != nil {
		return resp, err
	}
	if verrs == "" {
		return resp, nil
	}

	switch strictness {
	case api.SchemaStrictnessWarning:
		log.Warnf("schema validation failed (%s/%s): %s", v.provider.GetBackend(), v.provider.GetModel(), verrs)
		return resp, nil
	case api.SchemaStrictnessRetry:
		return v.retryWithFeedback(ctx, req, schema, verrs, resp)
	case api.SchemaStrictnessError:
		return resp, fmt.Errorf("%w: %s", ai.ErrSchemaValidation, verrs)
	default:
		return resp, fmt.Errorf("%w: unknown schemaStrictness %q: %s", ai.ErrSchemaValidation, strictness, verrs)
	}
}

// retryWithFeedback re-asks the model up to maxSchemaRetries times, feeding back
// the schema validation errors (and the previous response when we have one) so it
// can correct itself. It serves both failure modes: a post-response validation
// failure (prev is the offending response) and a provider-side rejection during
// generation (prev is nil because the response never surfaced). A response that is
// still invalid after the last attempt is a hard error.
func (v *validatingProvider) retryWithFeedback(ctx context.Context, req ai.Request, schema json.RawMessage, verrs string, prev *ai.Response) (*ai.Response, error) {
	for attempt := 1; attempt <= maxSchemaRetries; attempt++ {
		req2 := req
		req2.Prompt.User = req.Prompt.User + correctionFeedback(prev, verrs)

		resp, err := v.provider.Execute(ctx, req2)
		if err != nil {
			// The provider rejected the corrected output too; feed the new errors
			// back and keep trying until the retry budget is exhausted.
			if errors.Is(err, ai.ErrSchemaValidation) && attempt < maxSchemaRetries {
				verrs, prev = err.Error(), nil
				continue
			}
			return resp, err
		}

		verrs, err = validateResponse(schema, resp)
		if err != nil {
			return resp, err
		}
		if verrs == "" {
			return resp, nil
		}
		prev = resp
	}
	return nil, fmt.Errorf("%w: still invalid after %d retries: %s", ai.ErrSchemaValidation, maxSchemaRetries, verrs)
}

// correctionFeedback builds the instruction appended to the user prompt on a
// retry: the schema errors, plus the previous response when one was captured.
func correctionFeedback(prev *ai.Response, verrs string) string {
	if prev == nil {
		return fmt.Sprintf(
			"\n\nYour previous response did not conform to the required JSON schema.\n"+
				"Schema validation errors:\n%s\n\n"+
				"Return a corrected JSON response that satisfies the schema.",
			verrs)
	}
	return fmt.Sprintf(
		"\n\nYour previous response did not conform to the required JSON schema.\n"+
			"Previous response:\n%s\n\nSchema validation errors:\n%s\n\n"+
			"Return a corrected JSON response that satisfies the schema.",
		responseJSON(prev), verrs)
}

// ExecuteStream tees the stream: it forwards every event unchanged, accumulates
// the response (text + any EventResult.StructuredData), and once the terminal
// result arrives validates it against the request schema. Under "error"
// strictness an empty or non-conforming response injects a trailing EventError
// (which fails the iteration); "warning" only logs. (Streaming "retry" isn't
// supported — it would require re-streaming — so it is treated as "error".)
func (v *validatingProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	streamer, ok := v.provider.(ai.StreamingProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s/%s does not support streaming", v.provider.GetBackend(), v.provider.GetModel())
	}

	strictness := req.Prompt.SchemaStrictness
	schema, serr := ai.SchemaJSONFor(req.Prompt)
	if strictness == api.SchemaStrictnessNone || serr != nil || len(schema) == 0 {
		return streamer.ExecuteStream(ctx, req) // nothing to validate; forward as-is
	}

	upstream, err := streamer.ExecuteStream(ctx, req)
	if err != nil {
		return nil, err
	}

	out := make(chan ai.Event)
	go func() {
		defer close(out)
		var text strings.Builder
		var structured json.RawMessage
		validated := false
		for ev := range upstream {
			switch ev.Kind {
			case ai.EventText:
				text.WriteString(ev.Text)
			case ai.EventResult:
				if len(ev.StructuredData) > 0 {
					structured = ev.StructuredData
				}
			}
			out <- ev
			if ev.Kind == ai.EventResult {
				validated = true
				v.emitValidation(out, schema, strictness, text.String(), structured)
			}
		}
		// Some backends close the stream without a terminal EventResult.
		if !validated {
			v.emitValidation(out, schema, strictness, text.String(), structured)
		}
	}()
	return out, nil
}

// emitValidation validates the accumulated response and, on failure, injects an
// EventError (error/retry strictness) or logs (warning strictness).
func (v *validatingProvider) emitValidation(out chan<- ai.Event, schema json.RawMessage, strictness api.SchemaStrictness, text string, structured json.RawMessage) {
	resp := &ai.Response{Text: text}
	if len(structured) > 0 {
		resp.StructuredData = structured
	}
	verrs, err := validateResponse(schema, resp)
	if err != nil {
		out <- ai.Event{Kind: ai.EventError, Error: err.Error()}
		return
	}
	if verrs == "" {
		return
	}
	if strictness == api.SchemaStrictnessWarning {
		log.Warnf("schema validation failed (%s/%s): %s", v.provider.GetBackend(), v.provider.GetModel(), verrs)
		return
	}
	out <- ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("%s: %s", ai.ErrSchemaValidation, verrs)}
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
	return resp.Text
}

// validateResponse validates the provider's structured JSON against schema. It
// returns a human-readable joined error string (empty when the response conforms),
// plus a hard error only when the schema or document could not be loaded/parsed.
func validateResponse(schema json.RawMessage, resp *ai.Response) (string, error) {
	doc := responseJSON(resp)
	if strings.TrimSpace(doc) == "" {
		return "response carried no JSON to validate", nil
	}
	result, err := gojsonschema.Validate(gojsonschema.NewBytesLoader(schema), gojsonschema.NewStringLoader(doc))
	if err != nil {
		return "", fmt.Errorf("%w: validation could not run: %v", ai.ErrSchemaValidation, err)
	}
	if result.Valid() {
		return "", nil
	}
	msgs := make([]string, 0, len(result.Errors()))
	for _, e := range result.Errors() {
		msgs = append(msgs, e.String())
	}
	return strings.Join(msgs, "; "), nil
}
