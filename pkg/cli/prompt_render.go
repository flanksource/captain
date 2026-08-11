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

// renderPrompt is the HTTP/Spec render path: overlay a structured api.Spec (the
// web UI's rich runtime overrides) onto the rendered template.
func renderPrompt(ctx context.Context, id string, renderReq PromptRenderRequest) (PromptRenderResult, error) {
	if strings.TrimSpace(id) == "" {
		return renderEphemeralPrompt(renderReq)
	}
	record, err := resolvePromptRecord(ctx, id)
	if err != nil {
		return PromptRenderResult{}, err
	}
	content, err := readPromptContent(record)
	if err != nil {
		return PromptRenderResult{}, err
	}
	vars := renderReq.Variables
	if vars == nil {
		vars = map[string]any{}
	}
	req, cfg, err := promptlib.Load(content).Render(vars, nil)
	if err != nil {
		return PromptRenderResult{}, err
	}
	req.Prompt.Source = record.Rel
	if renderReq.Spec != nil {
		overlayRuntimeSpec(&req, &cfg, *renderReq.Spec)
	}
	// There is no --sandbox over HTTP, so the ref carries the whole selection:
	// the spec override the caller sent, else the prompt's own frontmatter.
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
	return finalizeRenderResult(record, content, req, cfg, renderReq.Runtimes)
}

func renderEphemeralPrompt(renderReq PromptRenderRequest) (PromptRenderResult, error) {
	record := promptRecord{
		Source: promptSource{Kind: "ephemeral", ID: "ephemeral", Label: "Ephemeral"},
		ID:     "",
		Path:   "<ephemeral>",
		Rel:    "scratch.prompt",
	}
	content := ephemeralPromptContent()
	var req ai.Request
	var cfg ai.Config
	if renderReq.Spec != nil {
		overlayRuntimeSpec(&req, &cfg, *renderReq.Spec)
	}
	if req.Prompt.Source == "" {
		req.Prompt.Source = "<ephemeral>"
	}
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
	return finalizeRenderResult(record, content, req, cfg, renderReq.Runtimes)
}

func ephemeralPromptContent() string {
	return `---
name: Scratch Prompt
description: Ephemeral prompt
---
{{role "user"}}
`
}

// renderPromptCLI is the CLI render path: load from id | discovered name |
// .prompt filepath | -p | stdin and overlay the flat CLI flags (overlayCLI).
func renderPromptCLI(ctx context.Context, id string, opts AIPromptOptions, varsJSON, stdin string) (PromptRenderResult, error) {
	content, source, usedStdin, record, err := loadPromptContent(ctx, id, opts, stdin)
	if err != nil {
		return PromptRenderResult{}, err
	}
	vars, err := promptVars(opts, varsJSON, stdin, usedStdin)
	if err != nil {
		return PromptRenderResult{}, err
	}
	req, cfg, err := renderLoadedContent(content, source, vars, opts)
	if err != nil {
		return PromptRenderResult{}, err
	}
	result, err := finalizeRenderResult(record, content, req, cfg, nil)
	if err != nil {
		return PromptRenderResult{}, err
	}
	if len(opts.MultiModels) > 0 {
		result.Runtimes, err = ai.ResolveRuntimeSelectors(opts.MultiModels, result.Config.Model)
		if err != nil {
			return PromptRenderResult{}, err
		}
	}
	return result, nil
}

// finalizeRenderResult packages the rendered request/config + prompt detail into
// a PromptRenderResult and sets the validation error (shared by both paths).
func finalizeRenderResult(record promptRecord, content string, req ai.Request, cfg ai.Config, runtimeOverride []api.Model) (PromptRenderResult, error) {
	// Normalize a comma-separated model into a clean primary + fallbacks so the
	// displayed Model is a single name, then catch a mistyped model (primary or any
	// fallback) at render time, not just on run.
	req.Model = req.ExpandCSV()
	cfg.Model = cfg.Model.ExpandCSV()
	var err error
	req.Model, err = ai.ResolveModelSelectors(req.Model)
	if err != nil {
		return PromptRenderResult{}, err
	}
	cfg.Model, err = ai.ResolveModelSelectors(cfg.Model)
	if err != nil {
		return PromptRenderResult{}, err
	}
	for _, c := range cfg.Model.Candidates() {
		warnIfLikelyModelTypo(c.Name)
	}
	detail, err := promptDetailFromContent(record, content)
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
	result := PromptRenderResult{
		ID:           detail.ID,
		Name:         detail.Name,
		Model:        cfg.Model.Name,
		Backend:      string(cfg.Model.Backend),
		User:         req.Prompt.User,
		System:       req.Prompt.System,
		Input:        req,
		Config:       cfg,
		InputSchema:  detail.InputSchema,
		InputDefault: detail.InputDefault,
		OutputSchema: detail.OutputSchema,
		Runtimes:     runtimes,
	}
	switch {
	case req.Prompt.User == "" && len(req.Prompt.Attachments) == 0 && !req.IsVerifyOnly():
		result.ValidationError = "prompt text or attachment required"
	case cfg.Model.Name == "" && len(result.Runtimes) == 0:
		result.ValidationError = "no model: set prompt frontmatter, pass a model override, or run 'captain configure'"
	default:
		if err := req.Validate(); err != nil {
			result.ValidationError = err.Error()
		}
	}
	return result, nil
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
		resolved[i], err = ai.ResolveModelSelectors(runtime)
		if err != nil {
			return nil, fmt.Errorf("runtime %d: %w", i+1, err)
		}
	}
	if err := validatePromptRuntimes(resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

func overlayRuntimeSpec(req *ai.Request, cfg *ai.Config, spec api.Spec) {
	if spec.Name != "" {
		req.Name = spec.Name
		cfg.Model.Name = spec.Name
		req.ID = spec.ID
		cfg.Model.ID = spec.ID
		req.Backend = spec.Backend
		cfg.Model.Backend = spec.Backend
	} else {
		if spec.ID != "" {
			req.ID = spec.ID
			cfg.Model.ID = spec.ID
		}
		if spec.Backend != "" {
			req.Backend = spec.Backend
			cfg.Model.Backend = spec.Backend
		}
	}
	if spec.Temperature != nil {
		req.Temperature = spec.Temperature
		cfg.Model.Temperature = spec.Temperature
	}
	if spec.Effort != "" {
		req.Effort = spec.Effort
		cfg.Model.Effort = spec.Effort
	}
	if len(spec.Fallbacks) > 0 {
		req.Fallbacks = spec.Fallbacks
		cfg.Model.Fallbacks = spec.Fallbacks
	}
	req.NoCache = req.NoCache || spec.NoCache
	cfg.NoCache = cfg.NoCache || spec.NoCache
	if spec.Budget.Cost > 0 {
		req.Budget.Cost = spec.Budget.Cost
		cfg.Budget.Cost = spec.Budget.Cost
	}
	if spec.Budget.MaxTokens > 0 {
		req.Budget.MaxTokens = spec.Budget.MaxTokens
		cfg.Budget.MaxTokens = spec.Budget.MaxTokens
	}
	if spec.Budget.MaxTurns > 0 {
		req.Budget.MaxTurns = spec.Budget.MaxTurns
		cfg.Budget.MaxTurns = spec.Budget.MaxTurns
	}
	if spec.Budget.Timeout != "" {
		req.Budget.Timeout = spec.Budget.Timeout
		cfg.Budget.Timeout = spec.Budget.Timeout
	}

	if spec.Prompt.User != "" {
		req.Prompt.User = spec.Prompt.User
	}
	if spec.Prompt.System != "" {
		req.Prompt.System = spec.Prompt.System
	}
	if spec.Prompt.AppendSystem != "" {
		req.Prompt.AppendSystem = spec.Prompt.AppendSystem
	}
	if spec.Prompt.Source != "" {
		req.Prompt.Source = spec.Prompt.Source
	}
	req.Prompt.Attachments = append(req.Prompt.Attachments, spec.Prompt.Attachments...)
	req.Prompt.Metadata = mergeStringMaps(req.Prompt.Metadata, spec.Prompt.Metadata)
	if len(spec.Prompt.SchemaJSON) > 0 {
		req.Prompt.SchemaJSON = spec.Prompt.SchemaJSON
	}
	if spec.Prompt.SchemaStrictness != "" {
		req.Prompt.SchemaStrictness = spec.Prompt.SchemaStrictness
	}
	if spec.Workflow != nil {
		req.Workflow = spec.Workflow
	}

	if spec.Permissions.Mode != "" {
		req.Permissions.Mode = spec.Permissions.Mode
	}
	req.Permissions.Presets = mergePresets(req.Permissions.Presets, spec.Permissions.Presets)
	toolPolicies := spec.Permissions.Tools.Policies()
	if len(toolPolicies) > 0 {
		req.Permissions.Tools.Allow = nil
		req.Permissions.Tools.Deny = nil
		req.Permissions.Tools.Modes = nil
		for _, tool := range sortedStringKeys(toolPolicies) {
			switch toolPolicies[tool] {
			case api.ToolPolicyAllow:
				req.Permissions.Tools.Allow = append(req.Permissions.Tools.Allow, tool)
			case api.ToolPolicyDeny:
				req.Permissions.Tools.Deny = append(req.Permissions.Tools.Deny, tool)
			case api.ToolPolicyAsk:
				if req.Permissions.Tools.Modes == nil {
					req.Permissions.Tools.Modes = map[string]api.ToolMode{}
				}
				req.Permissions.Tools.Modes[tool] = api.ToolModeAsk
			case api.ToolPolicyAuto:
				if req.Permissions.Tools.Modes == nil {
					req.Permissions.Tools.Modes = map[string]api.ToolMode{}
				}
				req.Permissions.Tools.Modes[tool] = api.ToolModeOn
			}
		}
	}
	req.Permissions.Tools.Modes = mergeToolModes(req.Permissions.Tools.Modes, spec.Permissions.Tools.Modes)
	req.Permissions.MCP.Disabled = req.Permissions.MCP.Disabled || spec.Permissions.MCP.Disabled
	if servers := spec.Permissions.MCP.EnabledServers(); len(servers) > 0 {
		req.Permissions.MCP.Servers = servers
	}
	if len(spec.Permissions.Plugins) > 0 {
		req.Permissions.Plugins = enabledResourcePolicies(spec.Permissions.Plugins)
	}

	skills := append([]string(nil), spec.Memory.Skills...)
	skills = append(skills, spec.Permissions.Skills.Enabled()...)
	if len(skills) > 0 {
		req.Memory.Skills = dedupeStrings(skills)
	}
	req.Memory.SkipProject = req.Memory.SkipProject || spec.Memory.SkipProject
	req.Memory.SkipUser = req.Memory.SkipUser || spec.Memory.SkipUser
	req.Memory.SkipSkills = req.Memory.SkipSkills || spec.Memory.SkipSkills
	req.Memory.SkipHooks = req.Memory.SkipHooks || spec.Memory.SkipHooks
	req.Memory.SkipMemory = req.Memory.SkipMemory || spec.Memory.SkipMemory
	req.Memory.Bare = req.Memory.Bare || spec.Memory.Bare

	if spec.Setup != nil {
		req.Setup = spec.Setup
	}
	if spec.Sandbox != nil {
		req.Sandbox = spec.Sandbox
	}

	if spec.SessionID != "" {
		req.SessionID = spec.SessionID
		cfg.SessionID = spec.SessionID
	}
}
