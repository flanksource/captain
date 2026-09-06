package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// renderPrompt is the HTTP/Spec render path: the caller's structured api.Spec
// (the web UI's runtime overrides) is the last layer over the selected runtime
// profile and the rendered frontmatter. A non-empty Content renders that draft
// instead of the saved file.
func renderPrompt(ctx context.Context, id string, renderReq PromptRenderRequest) (PromptRenderResult, error) {
	if strings.TrimSpace(id) == "" {
		return renderEphemeralPrompt(ctx, renderReq)
	}
	saved, err := loadSavedConfig()
	if err != nil {
		return PromptRenderResult{}, err
	}
	record, err := resolvePromptRecord(ctx, promptRecordOptions{ID: id, Config: &saved})
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
	frontmatter, _, err := promptlib.Load(content).Render(promptlib.RenderOptions{Data: vars, Declared: true})
	if err != nil {
		return PromptRenderResult{}, err
	}
	frontmatter.Prompt.Source = record.Rel
	resolved, err := renderLayers(ctx, record.Rel, content, frontmatter, renderReq, saved)
	if err != nil {
		return PromptRenderResult{}, err
	}
	return completePromptRender(promptRenderInput{Record: record, Content: content, Layers: resolved, Runtimes: renderReq.Runtimes, Saved: saved})
}

func renderEphemeralPrompt(ctx context.Context, renderReq PromptRenderRequest) (PromptRenderResult, error) {
	record := promptRecord{
		Source: promptSource{Kind: "ephemeral", ID: "ephemeral", Label: "Ephemeral"},
		Path:   "<ephemeral>",
		Rel:    "scratch.prompt",
	}
	content := ephemeralPromptContent()
	frontmatter := ai.Request{Prompt: api.Prompt{Source: "<ephemeral>"}}
	saved, err := loadSavedConfig()
	if err != nil {
		return PromptRenderResult{}, err
	}
	resolved, err := renderLayers(ctx, record.Rel, content, frontmatter, renderReq, saved)
	if err != nil {
		return PromptRenderResult{}, err
	}
	return completePromptRender(promptRenderInput{Record: record, Content: content, Layers: resolved, Runtimes: renderReq.Runtimes, Saved: saved})
}

func ephemeralPromptContent() string {
	return `---
name: Scratch Prompt
description: Ephemeral prompt
---
{{role "user"}}
`
}

type promptRenderInput struct {
	Record   promptRecord
	Content  string
	Layers   []api.SpecLayer
	Runtimes []api.Model
	Options  AIPromptOptions
	Saved    captainconfig.Config
}

func renderPromptCLI(ctx context.Context, id string, opts AIPromptOptions, varsJSON, stdin string) (PromptRenderResult, error) {
	saved, err := loadSavedConfig()
	if err != nil {
		return PromptRenderResult{}, err
	}
	content, source, usedStdin, record, err := loadPromptContent(ctx, promptContentOptions{ID: id, Prompt: opts, Stdin: stdin, Config: &saved})
	if err != nil {
		return PromptRenderResult{}, err
	}
	vars, err := promptVars(opts, varsJSON, stdin, usedStdin)
	if err != nil {
		return PromptRenderResult{}, err
	}
	layers, err := renderLoadedLayers(ctx, content, source, vars, opts, saved)
	if err != nil {
		return PromptRenderResult{}, err
	}
	return completePromptRender(promptRenderInput{Record: record, Content: content, Layers: layers, Runtimes: fallbackModelsFromFlags(opts.MultiModels), Options: opts, Saved: saved})
}

func completePromptRender(input promptRenderInput) (PromptRenderResult, error) {
	saved := input.Saved
	cwd, err := os.Getwd()
	if err != nil {
		return PromptRenderResult{}, fmt.Errorf("get working directory: %w", err)
	}
	flags, err := input.Options.requestSpec()
	if err != nil {
		return PromptRenderResult{}, err
	}
	layers := append([]api.SpecLayer(nil), input.Layers...)
	if len(flags.Fields()) > 0 {
		layers = append(layers, api.RequestSpecLayer("CLI flags", flags))
	}
	detail, err := parsedPromptDetail(input.Record, input.Content)
	if err != nil {
		return PromptRenderResult{}, err
	}
	runtimes := input.Runtimes
	if len(runtimes) == 0 {
		runtimes = detail.Runtimes
	}
	variants, err := resolvePromptRuntimes(promptRuntimeOptions{Models: runtimes, Layers: layers, Options: input.Options.AIRuntimeOptions, Saved: saved, Cwd: cwd, CLI: len(input.Options.MultiModels) > 0})
	if err != nil {
		return PromptRenderResult{}, err
	}
	var result AIRuntimeResolved
	if len(variants) == 0 {
		result, err = input.Options.resolveAuthored(AIRuntimeResolveOptions{Layers: layers, Saved: saved, Cwd: cwd})
		if err != nil {
			return PromptRenderResult{}, err
		}
	} else {
		result = variants[0]
		runtimes = make([]api.Model, len(variants))
		result.Resolution.Warnings = nil
		for i, variant := range variants {
			runtimes[i] = variant.Request.Model
			for _, warning := range variant.Resolution.Warnings {
				result.Resolution.Warnings = append(result.Resolution.Warnings, fmt.Sprintf("runtime %d: %s", i+1, warning))
			}
		}
	}
	req, cfg := result.Request, result.Config
	return PromptRenderResult{
		ID: detail.ID, Name: detail.Name, Model: cfg.Model.Name, Provider: providerName(cfg.Model.Provider), Mode: string(cfg.Model.Mode),
		User: req.Prompt.User, System: req.Prompt.System, Input: req, Config: cfg,
		InputSchema: detail.InputSchema, InputDefault: detail.InputDefault, OutputSchema: detail.OutputSchema,
		Runtimes: runtimes, variants: variants, Resolution: result.Resolution, ValidationError: renderValidationError(req, cfg, runtimes),
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
	needsModel := !req.IsVerifyOnly() || len(req.Workflow.Verify.Prompts) > 0
	if cfg.Model.Name == "" && len(runtimes) == 0 && needsModel {
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
