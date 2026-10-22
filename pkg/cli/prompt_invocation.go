package cli

import (
	"context"
	"errors"
	"io"
)

// PromptInvocation runs the same prompt sources and runtime options as the CLI.
type PromptInvocation struct {
	Reference string
	Options   AIPromptOptions
	Stdin     io.Reader
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
	return renderPromptCLI(ctx, invocation.Reference, invocation.Options, invocation.Options.Vars, stdin)
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
