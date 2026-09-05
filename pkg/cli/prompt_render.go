package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
)

// renderPrompt is the HTTP/Spec render path: the caller's structured api.Spec
// (the web UI's runtime overrides) is the last layer over the selected runtime
// profile and the rendered frontmatter. A non-empty Content renders that draft
// instead of the saved file.
func renderPrompt(ctx context.Context, id string, renderReq PromptRenderRequest) (PromptRenderResult, error) {
	if strings.TrimSpace(id) == "" {
		return renderEphemeralPrompt(ctx, renderReq)
	}
	record, err := resolvePromptRecord(ctx, id)
	if err != nil {
		return PromptRenderResult{}, err
	}
	content, err := readPromptContent(record)
	if err != nil {
		return PromptRenderResult{}, err
	}
	if strings.TrimSpace(renderReq.Content) != "" {
		content = renderReq.Content
	}
	vars := renderReq.Variables
	if vars == nil {
		vars = map[string]any{}
	}
	frontmatter, _, err := promptlib.Load(content).Render(vars, nil)
	if err != nil {
		return PromptRenderResult{}, err
	}
	frontmatter.Prompt.Source = record.Rel
	resolved, err := resolveRenderLayers(ctx, record.Rel, content, frontmatter, renderReq)
	if err != nil {
		return PromptRenderResult{}, err
	}
	return finishPromptRender(record, content, resolved, renderReq.Runtimes)
}

func renderEphemeralPrompt(ctx context.Context, renderReq PromptRenderRequest) (PromptRenderResult, error) {
	record := promptRecord{
		Source: promptSource{Kind: "ephemeral", ID: "ephemeral", Label: "Ephemeral"},
		Path:   "<ephemeral>",
		Rel:    "scratch.prompt",
	}
	content := ephemeralPromptContent()
	frontmatter := ai.Request{Prompt: api.Prompt{Source: "<ephemeral>"}}
	resolved, err := resolveRenderLayers(ctx, record.Rel, content, frontmatter, renderReq)
	if err != nil {
		return PromptRenderResult{}, err
	}
	return finishPromptRender(record, content, resolved, renderReq.Runtimes)
}

func ephemeralPromptContent() string {
	return `---
name: Scratch Prompt
description: Ephemeral prompt
---
{{role "user"}}
`
}

// finishPromptRender is the shared tail of the HTTP render paths: fold the
// resolved spec into request + config, resolve the sandbox, apply the saved
// defaults, normalize the context dir, then package the result.
func finishPromptRender(record promptRecord, content string, resolved api.ResolvedSpec, runtimes []api.Model) (PromptRenderResult, error) {
	req := resolved.Spec
	foldSkillPolicies(&req)
	cfg := configFromResolved(req)
	// There is no --sandbox over HTTP, so the ref carries the whole selection:
	// the layered spec the caller sent, else the prompt's own frontmatter.
	if err := applyRunSandbox(&req, &cfg, ""); err != nil {
		return PromptRenderResult{}, err
	}
	if err := applyPromptDefaults(&req, &cfg); err != nil {
		return PromptRenderResult{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return PromptRenderResult{}, fmt.Errorf("get working directory: %w", err)
	}
	if err := normalizePromptContextDir(&req, cwd); err != nil {
		return PromptRenderResult{}, err
	}
	return finalizeRenderResult(record, content, req, cfg, runtimes, resolved)
}

// renderPromptCLI is the CLI render path: load from id | discovered name |
// .prompt filepath | -p | stdin, layer the selected runtime profile beneath
// the frontmatter, and overlay the flat CLI flags (overlayCLI).
func renderPromptCLI(ctx context.Context, id string, opts AIPromptOptions, varsJSON, stdin string) (PromptRenderResult, error) {
	content, source, usedStdin, record, err := loadPromptContent(ctx, id, opts, stdin)
	if err != nil {
		return PromptRenderResult{}, err
	}
	vars, err := promptVars(opts, varsJSON, stdin, usedStdin)
	if err != nil {
		return PromptRenderResult{}, err
	}
	req, cfg, resolved, err := renderLoadedContent(ctx, content, source, vars, opts)
	if err != nil {
		return PromptRenderResult{}, err
	}
	result, err := finalizeRenderResult(record, content, req, cfg, nil, resolved)
	if err != nil {
		return PromptRenderResult{}, err
	}
	if len(opts.MultiModels) > 0 {
		result.Runtimes, err = ai.ResolveMulti(opts.MultiModels, result.Config.Model)
		if err != nil {
			return PromptRenderResult{}, err
		}
	}
	return result, nil
}

// finalizeRenderResult packages the rendered request/config + prompt detail into
// a PromptRenderResult (shared by both paths). A comma-separated model is
// normalized into a clean primary + fallbacks so the displayed Model is a single
// name, and a mistyped model (primary or any fallback) is caught at render time,
// not just on run. The resolution's spec is replaced with the final request so
// the trace explains exactly what runs.
func finalizeRenderResult(record promptRecord, content string, req ai.Request, cfg ai.Config, runtimeOverride []api.Model, resolved api.ResolvedSpec) (PromptRenderResult, error) {
	req.Model = req.ExpandCSV()
	cfg.Model = cfg.Model.ExpandCSV()
	var err error
	req.Model, err = ai.Resolve(req.Model)
	if err != nil {
		return PromptRenderResult{}, err
	}
	cfg.Model, err = ai.Resolve(cfg.Model)
	if err != nil {
		return PromptRenderResult{}, err
	}
	for _, c := range cfg.Model.Candidates() {
		warnIfLikelyModelTypo(c.Name)
	}
	detail, err := parsedPromptDetail(record, content)
	if err != nil {
		return PromptRenderResult{}, err
	}
	runtimes := detail.Runtimes
	if len(runtimeOverride) > 0 {
		runtimes = runtimeOverride
	}
	runtimes, err = resolvePromptRuntimes(runtimes, cfg.Model)
	if err != nil {
		return PromptRenderResult{}, err
	}
	resolved.Spec = req
	return PromptRenderResult{
		ID:              detail.ID,
		Name:            detail.Name,
		Model:           cfg.Model.Name,
		Provider:        providerName(cfg.Model.Provider),
		Mode:            string(cfg.Model.Mode),
		User:            req.Prompt.User,
		System:          req.Prompt.System,
		Input:           req,
		Config:          cfg,
		InputSchema:     detail.InputSchema,
		InputDefault:    detail.InputDefault,
		OutputSchema:    detail.OutputSchema,
		Runtimes:        runtimes,
		Resolution:      resolved,
		ValidationError: renderValidationError(req, cfg, runtimes),
	}, nil
}

// renderValidationError is the render-time verdict `captain prompt run` refuses
// on. Runnability is asked with api.Spec.ValidateRunnable — the same rule
// promptrun.Run enforces — so a spec that the run seam will reject is rejected
// here, before a provider or a stream exists, with the same message. Render used
// to ask a looser question ("prompt text or attachment"), so an attachments-only
// or messages-only prompt passed here and failed later.
func renderValidationError(req ai.Request, cfg ai.Config, runtimes []api.Model) string {
	if err := req.ValidateRunnable(); err != nil {
		return err.Error()
	}
	if cfg.Model.Name == "" && len(runtimes) == 0 {
		return "no model: set prompt frontmatter, pass a model override, or run 'captain configure'"
	}
	// A prompt that declares its own runtimes needs no singular base model:
	// each runtime names one and the run fans out across them. Validate
	// against the first so the rest of the request is still checked. This
	// used to pass only because the base name was back-filled from a
	// compiled-in default table, which made an unconfigured captain look
	// configured.
	if req.Name == "" && len(runtimes) > 0 {
		req.Model = runtimes[0]
	}
	if err := req.Validate(); err != nil {
		return err.Error()
	}
	return ""
}

func resolvePromptRuntimes(runtimes []api.Model, base api.Model) ([]api.Model, error) {
	if len(runtimes) == 0 {
		return nil, nil
	}
	resolved := make([]api.Model, len(runtimes))
	for i, runtime := range runtimes {
		if runtime.Temperature == nil {
			runtime.Temperature = base.Temperature
		}
		if runtime.Effort == api.EffortNone {
			runtime.Effort = base.Effort
		}
		runtime.NoCache = runtime.NoCache || base.NoCache
		var err error
		resolved[i], err = ai.Resolve(runtime)
		if err != nil {
			return nil, fmt.Errorf("runtime %d: %w", i+1, err)
		}
	}
	if err := validatePromptRuntimes(resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}
