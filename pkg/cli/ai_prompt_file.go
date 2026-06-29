package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
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
	default:
		return nil, false, fmt.Errorf("prompt required: pass a .prompt file, --prompt/-p text, or pipe via stdin")
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
	m := bm
	m.Name = firstNonEmpty(o.Model, bm.Name, saved.Model)
	m.Backend = api.Backend(firstNonEmpty(o.Backend, string(bm.Backend), saved.Backend))
	if temperature != 0 {
		t := temperature
		m.Temperature = &t
	}
	m.Effort = api.Effort(firstNonEmpty(o.Effort, string(bm.Effort), saved.ReasoningEffort))
	req.Model = m

	req.Budget.MaxTokens = firstPositive(o.MaxTokens, base.Budget.MaxTokens, saved.MaxTokens, 4096)
	req.Budget.Cost = firstPositiveFloat(budget, base.Budget.Cost, saved.BudgetUSD)

	if o.System != "" {
		req.Prompt.System = o.System
	}
	if o.AppendSystem != "" {
		req.Prompt.AppendSystem = o.AppendSystem
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
	if o.MaxTurns > 0 {
		req.MaxTurns = o.MaxTurns
	}

	// Config mirrors the resolved model + budget; runtime-only knobs from CLI+saved.
	cfg := baseCfg
	cfg.Model = req.Model
	cfg.Budget = req.Budget
	cfg.APIKey = o.APIKey
	cfg.NoCache = o.NoCache || saved.NoCache
	return req, cfg, nil
}

// normalizePromptContextDir makes the prompt command's workspace explicit before
// providers see the request. Empty means the invocation cwd; relative values are
// interpreted from that same cwd so SDK child-process directories cannot change
// the meaning of context.dir: .
func normalizePromptContextDir(req *ai.Request, cwd string) error {
	if cwd == "" {
		return fmt.Errorf("working directory is required")
	}
	if !filepath.IsAbs(cwd) {
		abs, err := filepath.Abs(cwd)
		if err != nil {
			return fmt.Errorf("resolve working directory %q: %w", cwd, err)
		}
		cwd = abs
	}
	if req.Context.Dir == "" {
		req.Context.Dir = filepath.Clean(cwd)
		return nil
	}
	if filepath.IsAbs(req.Context.Dir) {
		req.Context.Dir = filepath.Clean(req.Context.Dir)
		return nil
	}
	req.Context.Dir = filepath.Clean(filepath.Join(cwd, req.Context.Dir))
	return nil
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
