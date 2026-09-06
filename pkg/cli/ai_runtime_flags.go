package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/spf13/pflag"
)

// WithExplicit preserves authored zero values from flag binders.
func (o AIRuntimeOptions) WithExplicit(paths ...string) AIRuntimeOptions {
	o.ExplicitFields = (api.Spec{Explicit: o.ExplicitFields}).WithExplicit(paths...).Explicit
	return o
}

var runtimeFlagFields = map[string]string{
	"model": "/model", "mode": "/mode", "fallback": "/fallbacks", "effort": "/effort", "temperature": "/temperature", "no-cache": "/noCache",
	"budget": "/budget/cost", "max-tokens": "/budget/maxTokens", "max-turns": "/budget/maxTurns", "resume": "/sessionId",
	"no-mcp": "/permissions/mcp/disabled", "permission-mode": "/permissions/mode",
	"no-hooks": "/memory/skipHooks", "no-skills": "/memory/skipSkills", "no-user": "/memory/skipUser",
	"no-project": "/memory/skipProject", "no-memory": "/memory/skipMemory", "bare": "/memory/bare", "skill-dir": "/memory/skills",
	"timeout": "/budget/timeout", "system": "/prompt/system", "append-system": "/prompt/appendSystem",
}

// WithChangedFlags carries explicit false values through Cobra's typed binding.
func (o AIRuntimeOptions) WithChangedFlags(flags *pflag.FlagSet) AIRuntimeOptions {
	flags.Visit(func(flag *pflag.Flag) {
		if path, ok := runtimeFlagFields[flag.Name]; ok {
			o = o.WithExplicit(path)
			switch path {
			case "/model", "/mode", "/effort", "/temperature", "/noCache", "/fallbacks":
				o.ModelFlags = o.ModelFlags.WithExplicit(path)
			}
		}
	})
	return o
}

func (o AIRuntimeOptions) requestSpec() (api.Spec, error) {
	model, err := o.ToModel()
	if err != nil {
		return api.Spec{}, err
	}
	budget, err := o.BudgetUSD()
	if err != nil {
		return api.Spec{}, err
	}
	if o.MaxTurns < 0 || o.MaxTurns > 100 {
		return api.Spec{}, fmt.Errorf("invalid --max-turns %d (valid: 0-100, 0=provider default)", o.MaxTurns)
	}
	if err := validatePermissionMode(o.PermissionMode); err != nil {
		return api.Spec{}, err
	}
	permissions := api.Permissions{
		Mode:  api.PermissionMode(o.PermissionMode),
		Tools: api.ToolsFromLists(o.AllowedTools, o.DisallowedTools),
		MCP:   api.MCP{Disabled: o.NoMCP},
	}
	if o.Edit {
		permissions.Presets = []api.Preset{api.PresetEdit}
		if permissions.Mode == "" {
			permissions.Mode = api.PermissionAcceptEdits
		}
	}
	spec := api.Spec{
		Explicit: o.ExplicitFields.Clone(),
		Model:    model,
		Budget:   api.Budget{Cost: budget, MaxTokens: o.MaxTokens, MaxTurns: o.MaxTurns},
		Memory: api.Memory{Skills: o.SkillDirs, SkipHooks: o.NoHooks, SkipSkills: o.NoSkills,
			SkipUser: o.NoUser, SkipProject: o.NoProject, SkipMemory: o.NoMemory, Bare: o.Bare},
		Permissions: permissions,
		SessionID:   o.Resume,
	}
	if strings.TrimSpace(o.Budget) != "" {
		spec = spec.WithExplicit("/budget/cost")
	}
	for _, path := range []string{"/budget/timeout", "/prompt/system", "/prompt/appendSystem"} {
		delete(spec.Explicit, path)
	}
	if selector := o.SandboxSelector(); selector != "" {
		spec.Sandbox = &api.SandboxRef{Backend: selector}
		if kind, ok := registry.ParseSandboxKind(selector); ok {
			spec.Sandbox = &api.SandboxRef{Mode: kind}
			spec = spec.WithExplicit("/sandbox/backend")
		}
	}
	return spec, nil
}

func (o AIPromptOptions) promptSpec() (api.Spec, error) {
	var attachments []api.AttachmentRef
	if len(o.Attach) > 0 {
		var err error
		attachments, err = attachmentRefsFromFlags(o.Attach)
		if err != nil {
			return api.Spec{}, err
		}
	}
	spec := api.Spec{Prompt: api.Prompt{System: o.System, AppendSystem: o.AppendSystem, Attachments: attachments},
		Budget: api.Budget{Timeout: o.Timeout}}
	for _, path := range []string{"/budget/timeout", "/prompt/system", "/prompt/appendSystem"} {
		if o.ExplicitFields.Has(path) {
			spec = spec.WithExplicit(path)
		}
	}
	return spec, nil
}
