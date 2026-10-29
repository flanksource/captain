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
	// rejection is a provider's own schema rejection of the turn's answer — a
	// provider that validates constrained output itself fails the turn instead of
	// returning it. It is held, like result, until validation decides whether a
	// repair turn follows; the rejected answer is the turn's text.
	rejection *ai.Event
	failed    bool
	textShown bool
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
			turn.textShown = true
		case ai.EventSystem:
			if ev.SessionID != "" {
				turn.sessionID = ev.SessionID
			}
		case ai.EventError:
			if strings.HasPrefix(ev.Error, ai.ErrSchemaValidation.Error()) {
				held := ev
				turn.rejection = &held
				if turn.text.Len() == 0 {
					turn.text.WriteString(ev.Text)
				}
				continue
			}
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
// the request schema before releasing it. Under "repair" strictness a
// non-conforming result is repaired in further turns that resume the turn's
// provider session, so an agent corrects its answer with the context its work
// was done in; the released result carries the cost and usage of every turn.
// "error" strictness, and "repair" once its budget is spent, emit an EventError
// instead of accepting the result; "warning" only logs.
func (v *validatingProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	streamer, ok := v.provider.(ai.StreamingProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s/%s does not support streaming", v.provider.GetRuntime(), v.provider.GetModel())
	}

	strictness := v.effectiveStrictness(req)
	if v.provider.GetRuntime().Provider == "jev" && strictness == api.SchemaStrictnessRepair {
		return nil, fmt.Errorf("jev does not support schema repair retries")
	}
	if req.Prompt.OutputSchemaType == api.SchemaTypeTypeSafe {
		return streamer.ExecuteStream(ctx, req)
	}
	schema, serr := ai.SchemaJSONFor(req.Prompt)
	if serr != nil {
		return nil, serr
	}
	if strictness == api.SchemaStrictnessNone || len(schema) == 0 {
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
	fail := func(message string) {
		failure := ai.Event{Kind: ai.EventError, Error: message, CostUSD: cost}
		if sawUsage {
			failure.Usage = new(usage)
		}
		out <- failure
	}
	for attempt := 1; ; attempt++ {
		spent := turn.result
		if turn.rejection != nil {
			spent = turn.rejection
		}
		if spent != nil {
			cost += spent.CostUSD
			if spent.Usage != nil {
				usage, sawUsage = usage.Add(*spent.Usage), true
			}
		}
		if turn.rejection != nil {
			turn.showRejected(out)
			if strictness == api.SchemaStrictnessRepair && attempt <= maxSchemaRetries {
				next, err := v.streamRepair(ctx, req, schema, turn.rejection.Error, turn, attempt, out)
				if err != nil {
					fail(fmt.Sprintf("%s: repair attempt %d: %v", ai.ErrSchemaValidation, attempt, err))
					return
				}
				turn = next
				continue
			}
			failure := *turn.rejection
			failure.CostUSD = cost
			if sawUsage {
				failure.Usage = new(usage)
			}
			out <- failure
			return
		}
		verrs, err := v.validateResponse(schema, turn.response())
		if err != nil {
			turn.showRejected(out)
			fail(err.Error())
			return
		}
		if verrs != "" && !turn.failed && strictness == api.SchemaStrictnessRepair && attempt <= maxSchemaRetries {
			turn.showRejected(out)
			next, err := v.streamRepair(ctx, req, schema, verrs, turn, attempt, out)
			if err != nil {
				fail(fmt.Sprintf("%s: repair attempt %d: %v", ai.ErrSchemaValidation, attempt, err))
				return
			}
			turn = next
			continue
		}
		if turn.result != nil && (verrs == "" || strictness == api.SchemaStrictnessWarning) {
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
			turn.showRejected(out)
			fail(fmt.Sprintf("%s: %s", ai.ErrSchemaValidation, verrs))
		}
		return
	}
}

// streamRepair streams one repair turn. On the parent's runtime it resumes the
// session the rejected turn ran in; a repair routed to another runtime cannot
// share that session and runs standalone on the repair prompt alone.
func (v *validatingProvider) streamRepair(ctx context.Context, parent ai.Request, schema json.RawMessage, verrs string, prev *streamedTurn, attempt int, out chan<- ai.Event) (*streamedTurn, error) {
	out <- ai.Event{Kind: ai.EventSystem, Text: fmt.Sprintf("schema validation failed; repairing response (%d/%d): %s", attempt, maxSchemaRetries, verrs)}
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

func (t *streamedTurn) showRejected(out chan<- ai.Event) {
	if !t.textShown && t.text.Len() > 0 {
		out <- ai.Event{Kind: ai.EventText, Text: t.text.String(), Rejected: true}
		t.textShown = true
	}
	if t.rejection != nil {
		t.rejection.Text = ""
	}
}
