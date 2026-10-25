package cli

import (
	"context"
	"fmt"
	"strings"

	clickyapi "github.com/flanksource/clicky/api"

	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/api"
)

// `captain permissions explain` shows what a prompt's permissions become on
// each runtime it may land on: the resolved access intent and the layer that
// set it, the compiled native posture per runtime, any refusal, and the
// portable form the converter suggests.

type PermissionsExplainOptions struct {
	Prompt  string   `args:"true" help:"Prompt name, ID or .prompt path"`
	Models  []string `flag:"model" help:"Runtime to explain for, e.g. agent:sonnet (repeatable); defaults to the prompt's own model" category:"Model selection"`
	Presets []string `flag:"preset" help:"Runtime preset layered into the prompt (repeatable)"`
}

type PermissionsExplainResult struct {
	Prompt     string                      `json:"prompt"`
	Access     *api.AccessState            `json:"access,omitempty"`
	Suggestion api.ConversionResult        `json:"suggestion"`
	Runtimes   []PermissionsExplainRuntime `json:"runtimes"`
}

// PermissionsExplainRuntime is one runtime's compiled posture, or why the run
// would be refused there.
type PermissionsExplainRuntime struct {
	Model    string            `json:"model"`
	Runtime  string            `json:"runtime,omitempty"`
	Report   *api.AccessReport `json:"report,omitempty"`
	Refused  string            `json:"refused,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
}

func RunPermissionsExplain(ctx context.Context, opts PermissionsExplainOptions) (any, error) {
	if strings.TrimSpace(opts.Prompt) == "" {
		return nil, fmt.Errorf("a prompt name, ID or .prompt path is required")
	}
	base, err := renderForExplain(ctx, opts, "")
	if err != nil {
		return nil, err
	}
	result := PermissionsExplainResult{Prompt: base.Name, Access: base.Resolution.Access, Suggestion: api.ConvertToAccess(base.Input)}
	models := opts.Models
	if len(models) == 0 {
		models = []string{""}
	}
	for _, model := range models {
		result.Runtimes = append(result.Runtimes, explainRuntime(ctx, opts, model, base))
	}
	return result, nil
}

func renderForExplain(ctx context.Context, opts PermissionsExplainOptions, model string) (PromptRenderResult, error) {
	runtime := AIRuntimeOptions{AIProviderOptions: AIProviderOptions{ModelFlags: aiflags.ModelFlags{Model: model}}}
	return RenderPrompt(ctx, PromptInvocation{Reference: opts.Prompt, Stdin: strings.NewReader(""),
		Options: AIPromptOptions{AIRuntimeOptions: runtime, Presets: opts.Presets}})
}

func explainRuntime(ctx context.Context, opts PermissionsExplainOptions, model string, base PromptRenderResult) PermissionsExplainRuntime {
	rendered := base
	if model != "" {
		var err error
		if rendered, err = renderForExplain(ctx, opts, model); err != nil {
			return PermissionsExplainRuntime{Model: model, Refused: err.Error()}
		}
	}
	runtime := api.RuntimeOf(rendered.Config.Model.Provider, rendered.Config.Model.Mode)
	entry := PermissionsExplainRuntime{Model: rendered.Config.Model.Name, Runtime: runtime.String(), Warnings: rendered.Resolution.Warnings}
	_, report, err := api.CompileAccess(runtime, rendered.Input)
	entry.Report = &report
	switch {
	case err != nil:
		entry.Refused = err.Error()
	case rendered.ValidationError != "":
		entry.Refused = rendered.ValidationError
	}
	return entry
}

func (r PermissionsExplainResult) Pretty() clickyapi.Text {
	text := clickyapi.Text{}.Append(r.Prompt, "font-bold").NewLine()
	if r.Access == nil {
		text = text.Append("access: none (each runtime keeps its own rules)", "text-gray-500").NewLine()
	} else {
		text = text.Append(fmt.Sprintf("access: %s from %s (%s dialect) in layer %q", r.Access.Level, r.Access.Source, r.Access.Dialect, r.Access.Layer)).NewLine()
	}
	for _, runtime := range r.Runtimes {
		text = text.NewLine().Append(runtime.Model+" ", "font-bold").Append(runtime.Runtime, "text-blue-400").NewLine()
		text = text.Add(runtime.pretty())
	}
	text = text.NewLine().Append("suggested portable form: ", "font-bold")
	if !r.Suggestion.Convertible {
		return text.Append(r.Suggestion.Reason, "text-gray-500")
	}
	text = text.Append(fmt.Sprintf("permissions: {access: %s}", r.Suggestion.Permissions.Access))
	for _, residual := range r.Suggestion.Residual {
		text = text.NewLine().Append("  kept: "+residual, "text-gray-500")
	}
	return text
}

func (r PermissionsExplainRuntime) pretty() clickyapi.Text {
	text := clickyapi.Text{}
	if r.Refused != "" {
		text = text.Append("  refused: ", "text-red-500").Append(r.Refused).NewLine()
	}
	if report := r.Report; report != nil {
		for _, line := range reportLines(*report) {
			text = text.Append("  " + line).NewLine()
		}
	}
	for _, warning := range r.Warnings {
		text = text.Append("  warning: "+warning, "text-yellow-500").NewLine()
	}
	return text
}

func reportLines(report api.AccessReport) []string {
	var lines []string
	add := func(label string, values ...string) {
		if len(values) > 0 && values[0] != "" {
			lines = append(lines, label+": "+strings.Join(values, ", "))
		}
	}
	add("dialect", string(report.Dialect))
	if report.Unchanged {
		lines = append(lines, "unchanged: compiled in its own dialect")
	}
	add("denied tools", report.DeniedTools...)
	add("sandbox", report.Sandbox)
	add("approval", report.Approval)
	add("mode", string(report.Mode))
	if report.MCPDisabled {
		lines = append(lines, "mcp: disabled")
	}
	add("exec policy", report.ExecPolicy)
	add("overridden", report.Overridden...)
	add("equivalent", report.Equivalences...)
	add("note", report.Notes...)
	return lines
}
