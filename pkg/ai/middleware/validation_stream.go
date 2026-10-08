package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// streamedTurn is what one streamed turn produced: its text and structured
// output, the provider session it ran in, and its terminal result, held back
// until validation decides whether it ends the stream.
type streamedTurn struct {
	text       strings.Builder
	structured json.RawMessage
	sessionID  string
	result     *ai.Event
	failed     bool
}

func (t *streamedTurn) response() *ai.Response {
	resp := &ai.Response{Text: t.text.String()}
	if len(t.structured) > 0 {
		resp.StructuredData = t.structured
	}
	return resp
}

// forwardTurn relays every event of one turn except its terminal result, which
// it holds on the returned turn.
func forwardTurn(events <-chan ai.Event, out chan<- ai.Event) *streamedTurn {
	turn := &streamedTurn{}
	for ev := range events {
		switch ev.Kind {
		case ai.EventText:
			turn.text.WriteString(ev.Text)
		case ai.EventSystem:
			if ev.SessionID != "" {
				turn.sessionID = ev.SessionID
			}
		case ai.EventError:
			turn.failed = true
		case ai.EventResult:
			if len(ev.StructuredData) > 0 {
				turn.structured = ev.StructuredData
			}
			turn.failed = turn.failed || ev.Error != ""
			held := ev
			turn.result = &held
			continue
		}
		out <- ev
	}
	return turn
}

// ExecuteStream forwards the stream and validates the terminal result against
// the request schema before releasing it. Under "retry" strictness a
// non-conforming result is repaired in further turns that resume the turn's
// provider session, so an agent corrects its answer with the context its work
// was done in; the released result carries the cost and usage of every turn.
// "error" strictness, and "retry" once its budget is spent, follow the result
// with an EventError that fails the iteration; "warning" only logs.
func (v *validatingProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	streamer, ok := v.provider.(ai.StreamingProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s/%s does not support streaming", v.provider.GetRuntime(), v.provider.GetModel())
	}

	strictness := v.effectiveStrictness(req)
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
		v.validateStream(ctx, req, schema, strictness, forwardTurn(upstream, out), out)
	}()
	return out, nil
}

func (v *validatingProvider) validateStream(ctx context.Context, req ai.Request, schema json.RawMessage, strictness api.SchemaStrictness, turn *streamedTurn, out chan<- ai.Event) {
	var cost float64
	var usage ai.Usage
	sawUsage := false
	for attempt := 1; ; attempt++ {
		if turn.result != nil {
			cost += turn.result.CostUSD
			if turn.result.Usage != nil {
				usage, sawUsage = usage.Add(*turn.result.Usage), true
			}
		}
		verrs, err := validateResponse(schema, turn.response())
		if err != nil {
			out <- ai.Event{Kind: ai.EventError, Error: err.Error()}
			return
		}
		if verrs != "" && !turn.failed && strictness == api.SchemaStrictnessRetry && attempt <= maxSchemaRetries {
			next, err := v.streamRepair(ctx, req, schema, verrs, turn, attempt, out)
			if err != nil {
				out <- ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("%s: repair attempt %d: %v", ai.ErrSchemaValidation, attempt, err)}
				return
			}
			turn = next
			continue
		}
		if turn.result != nil {
			result := *turn.result
			result.CostUSD = cost
			if sawUsage {
				result.Usage = new(usage)
			}
			out <- result
		}
		switch {
		case verrs == "":
		case strictness == api.SchemaStrictnessWarning:
			log.Warnf("schema validation failed (%s/%s): %s", v.provider.GetRuntime(), v.provider.GetModel(), verrs)
		default:
			out <- ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("%s: %s", ai.ErrSchemaValidation, verrs)}
		}
		return
	}
}

// streamRepair streams one repair turn. On the parent's runtime it resumes the
// session the rejected turn ran in; a repair routed to another runtime cannot
// share that session and runs standalone on the repair prompt alone.
func (v *validatingProvider) streamRepair(ctx context.Context, parent ai.Request, schema json.RawMessage, verrs string, prev *streamedTurn, attempt int, out chan<- ai.Event) (*streamedTurn, error) {
	req, cfg, useParent, err := v.repairRequest(parent, schema, verrs, prev.response(), attempt)
	if err != nil {
		return nil, err
	}
	provider := v.provider
	if useParent {
		if prev.sessionID != "" {
			req.SessionID = prev.sessionID
		}
	} else {
		req.SessionID = ""
		if provider, err = ai.NewProvider(cfg); err != nil {
			return nil, err
		}
		if c, ok := provider.(io.Closer); ok {
			defer func() { _ = c.Close() }()
		}
	}
	streamer, ok := provider.(ai.StreamingProvider)
	if !ok {
		return nil, fmt.Errorf("schema repair provider %s/%s does not support streaming", provider.GetRuntime(), provider.GetModel())
	}
	events, err := streamer.ExecuteStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return forwardTurn(events, out), nil
}
