package cli

import (
	"context"
	"errors"
	"io"

	"github.com/flanksource/captain/pkg/api"
)

// PromptInvocation runs the same prompt sources and runtime options as the CLI.
type PromptInvocation struct {
	Reference string
	Options   AIPromptOptions
	Stdin     io.Reader
	// Tools are caller tools exposed to the agent on every runtime the prompt
	// resolves to. Rendering fails when one of those runtimes cannot expose them.
	Tools []api.ToolDefinition
}

// LaunchPrompt starts the prompt as an async run, the same run an HTTP
// `prompt run` starts, and returns its handle without waiting for it:
// PromptRunHandler serves RunID, and AwaitPromptRun blocks on it. A
// multi-runtime prompt launches a batch whose BatchID is not itself a run; each
// Runs[i].RunID is.
func LaunchPrompt(ctx context.Context, invocation PromptInvocation) (PromptRunResult, error) {
	rendered, err := RenderPrompt(ctx, invocation)
	if err != nil {
		return PromptRunResult{}, err
	}
	logRuntimeWarnings(rendered.Resolution.Warnings)
	return launchRendered(ctx, invocation.Reference, rendered, false)
}

func RenderPrompt(ctx context.Context, invocation PromptInvocation) (PromptRenderResult, error) {
	var stdin string
	var err error
	if invocation.Stdin == nil {
		stdin, err = readStdinIfCLI(ctx)
	} else {
		var data []byte
		data, err = io.ReadAll(invocation.Stdin)
		stdin = string(data)
	}
	if err != nil {
		return PromptRenderResult{}, err
	}
	rendered, err := renderPromptCLI(ctx, invocation.Reference, invocation.Options, invocation.Options.Vars, stdin)
	if err != nil {
		return PromptRenderResult{}, err
	}
	return rendered.withCallerTools(invocation.Tools)
}

func RunPrompt(ctx context.Context, invocation PromptInvocation) (PromptRunResult, error) {
	rendered, err := RenderPrompt(ctx, invocation)
	if err != nil {
		return PromptRunResult{}, err
	}
	if rendered.ValidationError != "" {
		return PromptRunResult{}, errors.New(rendered.ValidationError)
	}
	logRuntimeWarnings(rendered.Resolution.Warnings)
	return executeSyncRun(ctx, rendered, invocation.Options)
}
