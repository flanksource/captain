package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/promptrun"
)

// renderPrompt is the HTTP/Spec render path: the caller's structured api.Spec
// (the web UI's runtime overrides) is the last layer over the selected runtime
// presets and the rendered frontmatter. A non-empty Content renders that draft
// instead of the saved file; without an id that draft is self-contained, its
// own frontmatter included.
func renderPrompt(ctx context.Context, id string, renderReq PromptRenderRequest) (PromptRenderResult, error) {
	hasDraft := strings.TrimSpace(renderReq.Content) != ""
	if strings.TrimSpace(id) == "" && !hasDraft {
		return renderEphemeralPrompt(ctx, renderReq)
	}
	saved, err := loadSavedConfig()
	if err != nil {
		return PromptRenderResult{}, err
	}
	record, content := draftPromptRecord(), renderReq.Content
	if strings.TrimSpace(id) != "" {
		if record, err = resolvePromptRecord(ctx, promptRecordOptions{ID: id, Config: &saved}); err != nil {
			return PromptRenderResult{}, err
		}
		if !hasDraft {
			if content, err = readPromptContent(record); err != nil {
				return PromptRenderResult{}, err
			}
		}
	}
	vars := renderReq.Variables
	if vars == nil {
		vars = map[string]any{}
	}
	frontmatter, _, skip, err := promptrun.RenderTemplate(ctx, promptlib.Load(content), promptlib.RenderOptions{Data: vars, Declared: true, Dir: promptDir(record)}, ai.Config{})
	if err != nil {
		return PromptRenderResult{}, err
	}
	frontmatter.Prompt.Source = record.Rel
	resolved, warnings, err := renderLayers(ctx, record.Rel, content, frontmatter, renderReq, saved)
	if err != nil {
		return PromptRenderResult{}, err
	}
	return completePromptRender(promptRenderInput{Directory: renderReq.Directory, Record: record, Content: content, Layers: resolved, Warnings: warnings, Runtimes: renderReq.Runtimes, Saved: saved, Skip: skip})
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
	resolved, warnings, err := renderLayers(ctx, record.Rel, content, frontmatter, renderReq, saved)
	if err != nil {
		return PromptRenderResult{}, err
	}
	return completePromptRender(promptRenderInput{Directory: renderReq.Directory, Record: record, Content: content, Layers: resolved, Warnings: warnings, Runtimes: renderReq.Runtimes, Saved: saved})
}

// promptDir is the directory a saved prompt's hooks resolve relative child
// prompts against; empty for a prompt not read from a directory on disk.
func promptDir(record promptRecord) string {
	if record.Source.FS != nil || !filepath.IsAbs(record.Path) {
		return ""
	}
	return filepath.Dir(record.Path)
}

// previewSchemaHooks runs the rendered spec's output.hooks so `captain prompt
// render` shows the schema a run would send. Only a render does this: a run
// applies them itself, before its first turn.
func previewSchemaHooks(ctx context.Context, rendered *PromptRenderResult) error {
	spec := &rendered.Input
	// A run its input hooks skip never reaches its schema hooks; the preview
	// must not run them either.
	if len(spec.Prompt.SchemaHooks) == 0 || rendered.Skip != nil {
		return nil
	}
	if err := promptrun.RenderSchemaHooks(ctx, spec, rendered.Config); err != nil {
		return err
	}
	raw, schemaType := spec.Prompt.SchemaJSON, api.SchemaTypeJSONSchema
	if spec.Prompt.OutputSchemaType == api.SchemaTypeTypeSafe {
		raw, schemaType = spec.Prompt.TypeSafeQuestions, api.SchemaTypeTypeSafe
	}
	rendered.OutputSchema, rendered.OutputSchemaType = nil, schemaType
	if err := json.Unmarshal(raw, &rendered.OutputSchema); err != nil {
		return fmt.Errorf("decode emitted output schema: %w", err)
	}
	return nil
}

// draftPromptRecord names an unsaved draft posted without a saved prompt id.
func draftPromptRecord() promptRecord {
	return promptRecord{
		Source: promptSource{Kind: "draft", ID: "draft", Label: "Draft"},
		Path:   "<draft>",
		Rel:    "draft.prompt",
	}
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
	Directory string
	Record    promptRecord
	Content   string
	// Literal marks Content as caller data, so it is not inspected for
	// frontmatter, input/output schemas or template variables.
	Literal  bool
	Layers   []api.SpecLayer
	Warnings []string
	Runtimes []api.Model
	Options  AIPromptOptions
	Saved    captainconfig.Config
	// Skip is the input hook's request not to run the prompt, carried onto the
	// result.
	Skip *promptrun.Skip
}

func renderPromptCLI(ctx context.Context, id string, opts AIPromptOptions, varsJSON, stdin string) (PromptRenderResult, error) {
	saved, err := loadSavedConfig()
	if err != nil {
		return PromptRenderResult{}, err
	}
	hasVars := len(opts.Var) > 0 || strings.TrimSpace(varsJSON) != ""
	body, err := loadPromptContent(ctx, promptContentOptions{ID: id, Prompt: opts, Stdin: stdin, Config: &saved, HasVars: hasVars})
	if err != nil {
		return PromptRenderResult{}, err
	}
	vars, err := ParsePromptVariables(opts, varsJSON, stdin, body.UsedStdin)
	if err != nil {
		return PromptRenderResult{}, err
	}
	layers, warnings, skip, err := renderLoadedLayers(ctx, body, vars, stdin, opts, saved)
	if err != nil {
		return PromptRenderResult{}, err
	}
	return completePromptRender(promptRenderInput{Record: body.Record, Content: body.Text, Literal: body.Literal, Layers: layers, Warnings: warnings, Runtimes: fallbackModelsFromFlags(opts.MultiModels), Options: opts, Saved: saved, Skip: skip})
}

// literalOrParsedDetail describes the prompt behind a render. A literal body is
// only ever a body: inspecting it would split a leading "---" off as
// frontmatter and mine its braces for variables, which is the thing a literal
// body exists to prevent. It gets the bare record summary instead — enough for
// the run's name and task labels, which is all a render reads from it.
func literalOrParsedDetail(input promptRenderInput) (PromptDetail, error) {
	if !input.Literal {
		return parsedPromptDetail(input.Record, input.Content)
	}
	return PromptDetail{PromptSummary: basePromptSummary(input.Record), Content: input.Content}, nil
}

func completePromptRender(input promptRenderInput) (PromptRenderResult, error) {
	saved := input.Saved
	cwd := input.Directory
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return PromptRenderResult{}, fmt.Errorf("get working directory: %w", err)
		}
	}
	flags, err := input.Options.requestSpec()
	if err != nil {
		return PromptRenderResult{}, err
	}
	layers := append([]api.SpecLayer(nil), input.Layers...)
	if len(flags.Fields()) > 0 {
		layers = append(layers, api.RequestSpecLayer("CLI flags", flags))
	}
	detail, err := literalOrParsedDetail(input)
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
	result.Resolution.Warnings = append(input.Warnings, result.Resolution.Warnings...)
	req, cfg := result.Request, result.Config
	if len(req.Prompt.SchemaJSON) > 0 {
		if err := json.Unmarshal(req.Prompt.SchemaJSON, &detail.OutputSchema); err != nil {
			return PromptRenderResult{}, fmt.Errorf("decode resolved output schema: %w", err)
		}
		detail.OutputSchemaType = api.SchemaTypeJSONSchema
	}
	return PromptRenderResult{
		ID: detail.ID, Name: detail.Name, Model: cfg.Model.Name, Provider: providerName(cfg.Model.Provider), Mode: string(cfg.Model.Mode),
		User: req.Prompt.User, System: req.Prompt.System, Input: req, Config: cfg,
		InputSchema: detail.InputSchema, InputSchemaType: detail.InputSchemaType, InputDefault: detail.InputDefault,
		OutputSchema: detail.OutputSchema, OutputSchemaType: detail.OutputSchemaType,
		Runtimes: runtimes, variants: variants, Resolution: result.Resolution, ValidationError: renderValidationError(req, cfg, runtimes),
		Skip: input.Skip,
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
