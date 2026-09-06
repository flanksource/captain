package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/collections"
)

func executePromptRequest(parent context.Context, req ai.Request, cfg ai.Config, timeout time.Duration, noStream bool) (any, error) {
	ctx, cancel, err := runContext(parent, req, remoteAwareTimeout(req, cfg, timeout))
	if err != nil {
		return nil, err
	}
	defer cancel()
	if err := preparePromptAttachments(ctx, &req, cfg); err != nil {
		return nil, err
	}

	p, cleanup, err := buildProvider(ctx, &req, cfg)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	defer closeProvider(p)

	if streamer, ok := p.(ai.StreamingProvider); ok && !noStream && !req.Prompt.HasSchema() {
		return runStreaming(ctx, streamer, req)
	}
	return runBuffered(ctx, p, req)
}

// runContext derives the timeout-bounded context for a prompt execution. A
// parseable req.Budget.Timeout overrides the caller-supplied timeout; an
// unparseable one is an error, not a silent substitution. A caller-supplied
// timeout of zero falls back to the CLI default.
func runContext(parent context.Context, req ai.Request, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		parent = context.Background()
	}
	declared, err := runtimeTimeout(req.Budget.Timeout)
	if err != nil {
		return nil, nil, err
	}
	if declared > 0 {
		timeout = declared
	}
	if timeout <= 0 {
		timeout = defaultRunTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return ctx, cancel, nil
}

// warnIfLikelyModelTypo emits a "did you mean" hint when the model name is not a
// known catalog id but is a close (edit-distance ≤ 2) match to one — catching
// typos like "claud-sonnet-4" even when an explicit mode is configured (so
// ProviderFor's suggestion never fires). Non-blocking: an unrecognized model may
// still be a valid provider/OpenRouter id, so the run proceeds.
func warnIfLikelyModelTypo(model string) {
	if suggestion, ok := suggestKnownModel(model); ok {
		log.Warnf("model %q is not a recognized model; did you mean %q?", model, suggestion)
	}
}

// suggestKnownModel returns the closest known model to model and true when it is
// unrecognized-but-close (edit-distance ≤ 2) — a likely typo. A model known to
// the catalog or the pricing registry, or one far from any known name (a
// plausibly-valid id we simply don't list), returns ("", false).
//
// The catalog is checked first (in-memory, no I/O); only a catalog miss consults
// the pricing registry, which loads from its disk cache (fetching once if stale)
// and degrades to catalog-only if unavailable.
func suggestKnownModel(model string) (string, bool) {
	if model == "" {
		return "", false
	}
	for _, m := range ai.Catalog() {
		if m.ID == model || baseModelName(m.ID) == model {
			return "", false // known catalog model
		}
	}
	if pricing.Contains(model) {
		return "", false // known pricing-registry (e.g. OpenRouter) model
	}
	return closestModel(model, knownModelNames())
}

// knownModelNames is every model name captain can suggest: catalog canonical ids
// and their base names, plus the pricing registry's ids.
func knownModelNames() []string {
	catalog := ai.Catalog()
	names := make([]string, 0, len(catalog)*2+pricing.RegistrySize())
	for _, m := range catalog {
		names = append(names, m.ID, baseModelName(m.ID))
	}
	for _, mi := range pricing.ListModels("") {
		names = append(names, mi.ModelID, baseModelName(mi.ModelID))
	}
	return names
}

// closestModel returns the nearest candidate to model and true when it is a
// likely typo (edit-distance ≤ 2, not an exact match).
func closestModel(model string, candidates []string) (string, bool) {
	lower := strings.ToLower(model)
	best, bestDist := "", -1
	for _, cand := range candidates {
		if cand == model {
			return "", false
		}
		if d := collections.Levenshtein(lower, strings.ToLower(cand)); bestDist < 0 || d < bestDist {
			best, bestDist = cand, d
		}
	}
	if best != "" && bestDist >= 0 && bestDist <= 2 {
		return best, true
	}
	return "", false
}

func baseModelName(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// buildProvider constructs the logging-wrapped AI provider for req/cfg, applying
// req.Setup first so req describes the prepared workspace rather than asking for
// one (see pkg/ai/agent/setup). Callers MUST defer the returned cleanup; ctx
// should already carry the run timeout.
func buildProvider(ctx context.Context, req *ai.Request, cfg ai.Config) (ai.Provider, func(), error) {
	for _, c := range cfg.Model.Candidates() {
		warnIfLikelyModelTypo(c.Name)
	}
	cleanup := func() {}
	if req.NoCache {
		cfg.NoCache = true
	}
	// A remote-executing sandbox replaces provider execution wholesale: the
	// run happens on another machine, so no local setup checkout, no CLI
	// process, no streaming. Resolved before setup.Apply because the sandbox
	// is itself a workspace isolator — a local checkout would double-isolate.
	if remote, err := remoteExecProviderFor(req, cfg); err != nil {
		return nil, cleanup, err
	} else if remote != nil {
		wrapped, err := middleware.Wrap(remote, middleware.WithLogging(), middleware.WithSchemaValidation(cfg))
		if err != nil {
			closeProvider(remote)
			return nil, cleanup, err
		}
		return bufferedOnlyProvider{Provider: wrapped}, cleanup, nil
	}
	prepared, err := setup.Apply(ctx, req, "")
	if err != nil {
		return nil, cleanup, err
	}
	if prepared != nil && prepared.Cleanup != nil {
		cleanup = func() { _ = prepared.Cleanup() }
	}
	p, err := ai.NewProvider(cfg)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if p, err = middleware.Wrap(p, middleware.WithLogging(), middleware.WithSchemaValidation(cfg)); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return p, cleanup, nil
}

func runBuffered(ctx context.Context, p ai.Provider, req ai.Request) (any, error) {
	start := time.Now()
	resp, err := p.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	model := firstNonEmpty(resp.Model, p.GetModel(), req.Name)
	runtime := firstRuntime(resp.Runtime, p.GetRuntime(), api.RuntimeOf(req.Provider, req.Mode))
	input := resolvedPromptInput(req, model, runtime, req.SessionID)
	dir := actualRunDir(input)
	structuredOutput, err := structuredOutputMap(resp.StructuredData)
	if err != nil {
		return nil, err
	}
	text, err := structuredOutputText(resp.Text, structuredOutput)
	if err != nil {
		return nil, err
	}
	return AIPromptResult{
		Text:             text,
		StructuredOutput: structuredOutput,
		Model:            model,
		Provider:         runtime.Provider,
		Mode:             string(runtime.Mode),
		Dir:              dir,
		SessionID:        input.SessionID,
		HistoryFile:      historyFileForRun(providerOf(runtime), runtime.Mode, input.SessionID, dir),
		Input:            input,
		InputTokens:      resp.Usage.InputTokens,
		Output:           resp.Usage.OutputTokens,
		Duration:         time.Since(start).Round(time.Millisecond).String(),
	}, nil
}

// runStreaming drives the streaming provider through ai.RunUntil with a single
// iteration, rendering live tool/text events to stderr while accumulating the
// final text/usage/cost into AIPromptResult on stdout.
func runStreaming(ctx context.Context, sp ai.StreamingProvider, req ai.Request) (any, error) {
	start := time.Now()
	var (
		text             string
		usage            ai.Usage
		cost             float64
		runtime          = sp.GetRuntime()
		model            = sp.GetModel()
		session          = req.SessionID
		structuredOutput map[string]any
		structuredErr    error
	)
	renderer := NewEventRenderer(os.Stderr)
	loop, err := ai.RunUntil(ctx, ai.LoopOptions{
		Provider:      sp,
		MaxIterations: 1,
		MaxCostUSD:    req.Budget.Cost, // enforce the USD budget
		BuildRequest: func(iter int, prev *ai.LoopIteration) (ai.Request, bool) {
			if iter > 0 {
				return ai.Request{}, false
			}
			return req, true
		},
		OnEvent: func(iteration int, ev ai.Event) {
			if ev.Model != "" {
				model = ev.Model
			}
			if ev.SessionID != "" {
				session = ev.SessionID
			}
			renderer.Handle(iteration, ev)
			if ev.Kind == ai.EventText {
				text += ev.Text
			}
			if ev.Kind == ai.EventResult {
				if len(ev.StructuredData) > 0 {
					text = string(ev.StructuredData)
					structuredOutput, structuredErr = structuredOutputMap(ev.StructuredData)
				}
				if ev.Usage != nil {
					usage = *ev.Usage
				}
				cost = ev.CostUSD
			}
		},
	})
	if renderErr := renderer.Flush(); renderErr != nil {
		return nil, errors.Join(err, renderErr)
	}
	if err != nil {
		return nil, err
	}
	if structuredErr != nil {
		return nil, structuredErr
	}
	if loop.StopReason == "error" {
		return nil, fmt.Errorf("streaming loop stopped: %s", loop.StopReason)
	}
	if loop.StopReason == "max-cost" {
		return nil, fmt.Errorf("%w: spent $%.4f of $%.4f budget", ai.ErrBudgetExceeded, loop.TotalCost, req.Budget.Cost)
	}
	if session == "" && len(loop.Iterations) > 0 {
		session = loop.Iterations[0].SessionID
	}
	input := resolvedPromptInput(req, model, runtime, session)
	dir := actualRunDir(input)
	return AIPromptResult{
		Text:             text,
		StructuredOutput: structuredOutput,
		Model:            model,
		Provider:         runtime.Provider,
		Mode:             string(runtime.Mode),
		Dir:              dir,
		SessionID:        input.SessionID,
		HistoryFile:      historyFileForRun(providerOf(runtime), runtime.Mode, input.SessionID, dir),
		Input:            input,
		InputTokens:      usage.InputTokens,
		Output:           usage.OutputTokens,
		CostUSD:          cost,
		Duration:         time.Since(start).Round(time.Millisecond).String(),
	}, nil
}

// firstRuntime is the first fully-resolved (provider, mode) pair among the
// candidates: what the response reported, else what the provider serves, else
// what the request asked for.
func firstRuntime(candidates ...api.Runtime) api.Runtime {
	for _, candidate := range candidates {
		if candidate.Valid() {
			return candidate
		}
	}
	return api.Runtime{}
}

func providerOf(runtime api.Runtime) *api.ModelProvider {
	p, _ := runtime.ModelProvider()
	return p
}

func resolvedPromptInput(req ai.Request, model string, runtime api.Runtime, sessionID string) ai.Request {
	out := req
	if model != "" {
		out.Name = model
	}
	if runtime.Valid() {
		out.Provider = providerOf(runtime)
		out.Mode = runtime.Mode
	}
	if sessionID != "" {
		out.SessionID = sessionID
	}
	return out
}

func actualRunDir(req ai.Request) string {
	if cwd := req.Cwd(); cwd != "" {
		return cwd
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

type AITestOptions struct {
	AIProviderOptions
	Timeout string `flag:"timeout" help:"Request timeout" default:"60s"`
}

type AITestResult struct {
	Model    string `json:"model" pretty:"label=Model"`
	Provider string `json:"provider" pretty:"label=Provider"`
	Mode     string `json:"mode" pretty:"label=Mode"`
	Status   string `json:"status" pretty:"label=Status"`
	Latency  string `json:"latency" pretty:"label=Latency"`
}

func RunAITest(opts AITestOptions) (any, error) {
	resolved, err := resolveInvocation(AIRuntimeOptions{AIProviderOptions: opts.AIProviderOptions}, []api.SpecLayer{api.PromptSpecLayer("connectivity test", api.Spec{
		Prompt: api.Prompt{User: "Respond with exactly: ok"}, Budget: api.Budget{MaxTokens: 10},
	})})
	if err != nil {
		return nil, err
	}
	cfg := resolved.Config
	logRuntimeWarnings(resolved.Resolution.Warnings)
	if cfg.Model.Name == "" {
		return nil, fmt.Errorf("no model: pass --model or run 'captain configure' to set a default")
	}
	p, err := ai.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	if p, err = middleware.Wrap(p, middleware.WithLogging()); err != nil {
		return nil, err
	}
	timeout, _ := time.ParseDuration(opts.Timeout)
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	_, err = p.Execute(ctx, resolved.Request)

	result := AITestResult{
		Model:    p.GetModel(),
		Provider: p.GetRuntime().Provider,
		Mode:     string(p.GetRuntime().Mode),
		Latency:  time.Since(start).Round(time.Millisecond).String(),
	}

	if err != nil {
		result.Status = fmt.Sprintf("FAIL: %v", err)
	} else {
		result.Status = "OK"
	}
	return result, nil
}
