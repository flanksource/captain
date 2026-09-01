package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api/icons"
	"github.com/flanksource/commons/logger"
)

// log is the package-scoped logger for AI provider middleware. Its level
// follows -v/--log-level and can be tuned with -Plog.level.ai=debug.
var log = logger.GetLogger("ai")

type loggingProvider struct {
	provider ai.Provider
}

func (l *loggingProvider) GetModel() string       { return l.provider.GetModel() }
func (l *loggingProvider) GetRuntime() ai.Runtime { return l.provider.GetRuntime() }
func (l *loggingProvider) Unwrap() ai.Provider    { return l.provider }

func (l *loggingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()
	mode, model := logRuntime(l.provider, req)
	identity := ai.LogIdentity(mode, model, req.Effort)

	dispatch := clicky.Text("").
		Add(icons.AI).
		Append(" "+identity, "text-purple-600 font-medium")
	if req.Prompt.Source != "" {
		dispatch = dispatch.Append(fmt.Sprintf(" [%s]", req.Prompt.Source), "text-gray-500")
	}
	log.Infof("%v", dispatch)

	if log.IsDebugEnabled() {
		t := dispatch.NewLine().Append(req.Prompt.User, "text-gray-600")
		if s := schemaInJSON(req.Prompt); s != "" {
			t = t.NewLine().Append("schema-in ", "text-gray-500").Append(s, "text-gray-600")
		}
		log.Debugf("%v", t)
	}

	resp, err := l.provider.Execute(ctx, req)
	duration := time.Since(start)

	if err != nil {
		log.Errorf("%v", clicky.Text("").
			Add(icons.Error).
			Append(" "+identity, "text-red-600 font-medium").
			Append(fmt.Sprintf(" failed after %v: %v", duration.Round(time.Millisecond), err), "text-red-500"))
		return resp, err
	}

	log.Infof("%v", clicky.Text("").
		Add(icons.Check).
		Append(" "+identity, "text-green-600 font-medium").
		Append(fmt.Sprintf(" %v", duration.Round(time.Millisecond)), "text-gray-500").
		Append(fmt.Sprintf(" (tokens: %d in / %d out)", resp.Usage.InputTokens, resp.Usage.OutputTokens), "text-gray-400"))

	if log.IsTraceEnabled() {
		t := clicky.Text("").
			Add(icons.ArrowDown).
			Append(" response", "text-gray-500").
			NewLine().
			Append(resp.Text, "text-gray-600")
		if s := structuredOutJSON(resp.StructuredData); s != "" {
			t = t.NewLine().Append("schema-out ", "text-gray-500").Append(s, "text-gray-600")
		}
		log.Tracef("%v", t)
	}

	return resp, nil
}

// ExecuteStream forwards to the inner provider when it implements
// ai.StreamingProvider. The startup line is logged here so callers see the
// dispatch even when they bypass Execute. The success/failure summary is
// emitted by whoever drains the channel (Execute via CoalesceStream, or the
// CLI via runStreaming).
func (l *loggingProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	streamer, ok := l.provider.(ai.StreamingProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s/%s does not support streaming", l.provider.GetRuntime(), l.provider.GetModel())
	}
	mode, model := logRuntime(l.provider, req)
	identity := ai.LogIdentity(mode, model, req.Effort)

	dispatch := clicky.Text("").
		Add(icons.AI).
		Append(" "+identity+" (stream)", "text-purple-600 font-medium")
	if req.Prompt.Source != "" {
		dispatch = dispatch.Append(fmt.Sprintf(" [%s]", req.Prompt.Source), "text-gray-500")
	}
	log.Infof("%v", dispatch)

	if log.IsDebugEnabled() {
		log.Debugf("%v", dispatch.NewLine().Append(req.Prompt.User, "text-gray-600"))
	}

	return streamer.ExecuteStream(ctx, req)
}

func logRuntime(provider ai.Provider, req ai.Request) (api.RuntimeMode, string) {
	mode := req.Mode
	if mode == "" {
		mode = provider.GetRuntime().Mode
	}
	model := req.Name
	if model == "" {
		model = provider.GetModel()
	}
	return mode, model
}

func WithLogging() Option {
	return func(p ai.Provider) (ai.Provider, error) {
		return &loggingProvider{provider: p}, nil
	}
}

// schemaInJSON renders the JSON schema captain will send the model for a prompt:
// the pre-built Prompt.SchemaJSON verbatim when set (preserving the full JSON
// Schema vocabulary, e.g. maxItems), else the schema reflected from the Prompt.Schema
// Go target. Returns "" for text-mode requests (no schema); a resolution failure is
// surfaced inline rather than swallowed, since this is diagnostics that must never
// abort the run.
func schemaInJSON(p api.Prompt) string {
	raw, err := ai.SchemaJSONFor(p)
	if err != nil {
		return fmt.Sprintf("<schema-in error: %v>", err)
	}
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

// structuredOutJSON renders the structured response the provider parsed into the
// caller's target. Returns "" when the response was text-only (nil StructuredData).
func structuredOutJSON(data any) string {
	if data == nil {
		return ""
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Sprintf("<schema-out error: %v>", err)
	}
	return string(b)
}
