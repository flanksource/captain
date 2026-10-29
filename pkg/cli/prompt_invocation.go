package cli

import (
	"context"
	"errors"
	"github.com/flanksource/captain/pkg/ai"
	"io"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/task"
)

// PromptInvocation runs the same prompt sources and runtime options as the CLI.
type PromptInvocation struct {
	Prepared  *PreparedPrompt
	Reference string
	Options   AIPromptOptions
	Stdin     io.Reader
	// Tools are caller tools exposed to the agent on every runtime the prompt
	// resolves to. Rendering fails when one of those runtimes cannot expose them.
	Tools []api.ToolDefinition
	// Group places a launched run's task in the caller's own run group instead
	// of a `prompt <name>` group of its own; the group's concurrency then governs
	// it. Name names that task (default: the prompt's name); each variant of a
	// multi-runtime prompt joins as `<Name>:<selector>`. Only LaunchPrompt reads
	// them.
	Group   *task.Group
	Name    string
	OnEvent func(int, ai.Event)
}

// LaunchPrompt starts the prompt as an async run, the same run an HTTP
// `prompt run` starts, and returns its handle without waiting for it:
// PromptRunHandler serves RunID, and AwaitPromptRun blocks on it. A
// multi-runtime prompt launches a batch whose BatchID is not itself a run; each
// Runs[i].RunID is.
func LaunchPrompt(ctx context.Context, invocation PromptInvocation) (PromptRunResult, error) {
	var rendered PromptRenderResult
	if invocation.Prepared != nil {
		rendered = invocation.Prepared.rendered
	} else {
		var err error
		rendered, err = RenderPrompt(ctx, invocation)
		if err != nil {
			return PromptRunResult{}, err
		}
	}
	logRuntimeWarnings(rendered.Resolution.Warnings)
	return launchRendered(ctx, promptLaunch{
		ID: invocation.Reference, Rendered: rendered, Group: invocation.Group, Name: invocation.Name, OnEvent: invocation.OnEvent,
	})
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

// RunPrompt renders and runs the prompt synchronously. Input files
// (Options.InputFiles, and --attach under a batch) run as one run, or as one
// run per batch when the spec sets batch.
func RunPrompt(ctx context.Context, invocation PromptInvocation) (PromptRunResult, error) {
	if len(invocation.Options.InputFiles) > 0 || len(invocation.Options.Attach) > 0 {
		return runInputFiles(ctx, invocation)
	}
	rendered, err := RenderPrompt(ctx, invocation)
	if err != nil {
		return PromptRunResult{}, err
	}
	return runRendered(ctx, rendered, invocation.Options)
}

// runRendered runs a rendered prompt, or reports the skip its input hooks
// asked for without calling a model or recording a run.
func runRendered(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions) (PromptRunResult, error) {
	if rendered.Skip != nil {
		return skippedRunResult(rendered), nil
	}
	if rendered.ValidationError != "" {
		return PromptRunResult{}, errors.New(rendered.ValidationError)
	}
	logRuntimeWarnings(rendered.Resolution.Warnings)
	return executeSyncRun(ctx, rendered, opts)
}

func skippedRunResult(rendered PromptRenderResult) PromptRunResult {
	return PromptRunResult{Status: "skipped", SkipReason: rendered.Skip.Reason, Model: rendered.Model, Provider: rendered.Provider, Mode: rendered.Mode}
}
