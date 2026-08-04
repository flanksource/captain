package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/commons-db/shell"
)

// resolvePromptTemplate picks the prompt source for `captain ai prompt` and loads
// it as a dotprompt template. Precedence: positional file path > --prompt/-p value
// (literal text, or file content clicky already loaded from an @ reference) >
// piped stdin. usedStdin reports whether stdin became the prompt source, so the
// caller knows not to also expose it as the {{input}} template variable.
func resolvePromptTemplate(opts AIPromptOptions, stdin string) (tmpl *prompt.Template, usedStdin bool, err error) {
	switch {
	case opts.File != "":
		t, err := prompt.LoadFile(opts.File)
		return t, false, err
	case opts.Prompt != "":
		return prompt.Load(opts.Prompt), false, nil
	case strings.TrimSpace(stdin) != "":
		return prompt.Load(stdin), true, nil
	case len(opts.Attach) > 0:
		return prompt.Load(""), false, nil
	default:
		return nil, false, fmt.Errorf("prompt or attachment required: pass a .prompt file, --prompt/-p text, --attach/-A, or pipe via stdin")
	}
}

// parseVars turns repeated --var key=value flags into the template data map.
func parseVars(pairs []string) (map[string]any, error) {
	data := make(map[string]any, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --var %q: want key=value", p)
		}
		data[k] = v
	}
	return data, nil
}

// overlayCLI layers the CLI flags over the rendered file spec, implementing the
// precedence CLI flag (non-zero) > frontmatter > saved defaults > built-in. The
// user prompt always comes from the rendered template body; everything else is
// merged per nested group. Negative toggles (--no-*) OR across all three layers,
// matching the existing flag semantics. Range/enum validation is left to the
// caller via req.Validate so the rules live in one place (pkg/api).
func overlayCLI(base ai.Request, baseCfg ai.Config, o AIPromptOptions) (ai.Request, ai.Config, error) {
	saved := loadSavedAI()

	temperature, err := parseFloatFlag("temperature", o.Temperature)
	if err != nil {
		return base, baseCfg, err
	}
	budget, err := parseFloatFlag("budget", o.Budget)
	if err != nil {
		return base, baseCfg, err
	}

	req := base

	bm := base.Model
	if bm.Name == "" {
		bm.Name = baseCfg.Model.Name
	}
	if bm.ID == "" {
		bm.ID = baseCfg.Model.ID
	}
	if bm.Backend == "" {
		bm.Backend = baseCfg.Model.Backend
	}
	if bm.Temperature == nil {
		bm.Temperature = baseCfg.Model.Temperature
	}
	if bm.Effort == "" {
		bm.Effort = baseCfg.Model.Effort
	}
	identity := selectModelIdentity(
		api.Model{Name: bm.Name, ID: bm.ID, Backend: bm.Backend},
		api.Model{Name: o.Model, Backend: api.Backend(o.Backend)},
	)
	m := bm
	m.Name, m.ID, m.Backend = identity.Name, identity.ID, identity.Backend
	if temperature != 0 {
		t := temperature
		m.Temperature = &t
	}
	m.Effort = api.Effort(firstNonEmpty(o.Effort, string(bm.Effort)))
	m.NoCache = o.NoCache || bm.NoCache || saved.NoCache
	m.Fallbacks = firstFallbacks(o.Fallback, bm.Fallbacks)

	requestedMode := registry.RuntimeMode("")
	if value := strings.TrimSpace(o.Mode); value != "" {
		var ok bool
		requestedMode, ok = registry.ParseRuntimeMode(value)
		if !ok {
			return base, baseCfg, fmt.Errorf("invalid --mode %q (valid: %s)", o.Mode, registry.RuntimeModeList())
		}
	}
	// Sandbox precedence: --sandbox > frontmatter (base.Sandbox) > global default.
	sandbox, err := resolveSandboxSelection(o.SandboxSelector(), base.Sandbox, loadSavedConfig().Sandbox)
	if err != nil {
		return base, baseCfg, err
	}
	if sandbox.Kind == registry.SandboxSRT {
		if requestedMode != "" && requestedMode != registry.ModeCLI {
			return base, baseCfg, fmt.Errorf("sandbox %q requires CLI mode, but --mode is %q", sandbox.Kind, requestedMode)
		}
		requestedMode = registry.ModeCLI
	}
	if requestedMode != "" {
		m.Mode = ""
		if strings.TrimSpace(o.Backend) == "" && !ai.ContainsRuntimeSelector(o.Model) {
			m.Backend = ""
		}
		if len(o.Fallback) == 0 {
			for i := range m.Fallbacks {
				m.Fallbacks[i].Backend = ""
				m.Fallbacks[i].Mode = ""
			}
		}
		m, err = m.WithMode(requestedMode)
		if err != nil {
			return base, baseCfg, err
		}
	}
	m, err = applyProviderDefaults(m, saved)
	if err != nil {
		return base, baseCfg, err
	}
	req.Model, err = ai.ResolveModelSelectors(m)
	if err != nil {
		return base, baseCfg, err
	}

	req.Budget.MaxTokens = firstPositive(o.MaxTokens, base.Budget.MaxTokens, baseCfg.Budget.MaxTokens, saved.MaxTokens, 4096)
	req.Budget.Cost = firstPositiveFloat(budget, base.Budget.Cost, baseCfg.Budget.Cost, saved.BudgetUSD)
	req.Budget.MaxTurns = firstPositive(o.MaxTurns, base.Budget.MaxTurns)
	req.Budget.Timeout = firstNonEmpty(o.Timeout, base.Budget.Timeout)

	if o.System != "" {
		req.Prompt.System = o.System
	}
	if o.AppendSystem != "" {
		req.Prompt.AppendSystem = o.AppendSystem
	}
	if len(o.Attach) > 0 {
		attachments, err := attachmentRefsFromFlags(o.Attach)
		if err != nil {
			return base, baseCfg, err
		}
		req.Prompt.Attachments = append(req.Prompt.Attachments, attachments...)
	}

	if o.PermissionMode != "" {
		req.Permissions.Mode = api.PermissionMode(o.PermissionMode)
	}
	if o.Edit && !req.Permissions.HasPreset(api.PresetEdit) {
		req.Permissions.Presets = append(req.Permissions.Presets, api.PresetEdit)
	}
	if o.AllowedTools != nil {
		req.Permissions.Tools.Allow = o.AllowedTools
	}
	if o.DisallowedTools != nil {
		req.Permissions.Tools.Deny = o.DisallowedTools
	}
	req.Permissions.MCP.Disabled = o.NoMCP || base.Permissions.MCP.Disabled || saved.NoMCP

	if o.SkillDirs != nil {
		req.Memory.Skills = o.SkillDirs
	}
	req.Memory.SkipHooks = o.NoHooks || base.Memory.SkipHooks || saved.NoHooks
	req.Memory.SkipSkills = o.NoSkills || base.Memory.SkipSkills || saved.NoSkills
	req.Memory.SkipUser = o.NoUser || base.Memory.SkipUser || saved.NoUser
	req.Memory.SkipProject = o.NoProject || base.Memory.SkipProject || saved.NoProject
	req.Memory.SkipMemory = o.NoMemory || base.Memory.SkipMemory || saved.NoMemory
	req.Memory.Bare = o.Bare || base.Memory.Bare

	if o.Resume != "" {
		req.SessionID = o.Resume
	}

	// Record the winning sandbox on the request when the flag overrode it, so the
	// serialized spec carries the choice the run was actually made with.
	if selector := o.SandboxSelector(); selector != "" {
		req.Sandbox = &api.SandboxRef{Backend: selector}
	}

	// Config mirrors the resolved model + budget; runtime-only knobs from CLI+saved.
	cfg := baseCfg
	cfg.Model = req.Model
	cfg.Budget = req.Budget
	cfg.APIKey = o.APIKey
	cfg.APIURL = firstNonEmpty(strings.TrimSpace(o.APIURL), baseCfg.APIURL)
	cfg.Sandbox = sandbox.Kind == registry.SandboxSRT || baseCfg.Sandbox
	if selection := sandboxSelectionConfig(sandbox); selection != nil {
		cfg.SandboxSelection = selection
	}
	cfg.NoCache = req.NoCache
	return req, cfg, nil
}

// normalizePromptContextDir resolves the complete Setup through its owning
// commons-db type before providers see the request.
func normalizePromptContextDir(req *ai.Request, cwd string) error {
	if cwd == "" {
		return fmt.Errorf("working directory is required")
	}
	setup := shell.Setup{}
	if req.Setup != nil {
		setup = *req.Setup
	}
	resolved, err := setup.Resolve(cwd)
	if err != nil {
		return err
	}
	req.Setup = &resolved
	return nil
}

// fallbackModelsFromFlags turns repeated (and optionally comma-separated) --fallback
// values into name-only fallback Models, in the order given.
func fallbackModelsFromFlags(flags []string) []api.Model {
	var out []api.Model
	for _, flag := range flags {
		for _, name := range strings.Split(flag, ",") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, api.Model{Name: name})
			}
		}
	}
	return out
}

// firstFallbacks implements the CLI-over-frontmatter precedence for the fallback
// list: the --fallback flags win when present, otherwise the frontmatter list stands.
func firstFallbacks(flags []string, frontmatter []api.Model) []api.Model {
	if models := fallbackModelsFromFlags(flags); len(models) > 0 {
		return models
	}
	return frontmatter
}

// firstNonEmpty returns the first non-empty string, or "" when all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// selectModelIdentity applies precedence from lowest to highest while keeping a
// model name and backend coupled. A higher-priority name clears a lower-priority
// backend unless that same layer explicitly supplies one.
func selectModelIdentity(layers ...api.Model) api.Model {
	var selected api.Model
	for _, layer := range layers {
		if layer.Name != "" {
			selected.Name = layer.Name
			selected.ID = layer.ID
			selected.Backend = layer.Backend
			continue
		}
		if layer.ID != "" {
			selected.ID = layer.ID
		}
		if layer.Backend != "" {
			selected.Backend = layer.Backend
		}
	}
	return selected
}

// firstPositive returns the first value > 0, or 0 when none qualify.
func firstPositive(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// firstPositiveFloat returns the first value > 0, or 0 when none qualify.
func firstPositiveFloat(vals ...float64) float64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}
