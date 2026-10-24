// Package exectool turns frontmatter-declared inline tools (api.InlineTools)
// into caller tool definitions whose handlers run the tool's script through the
// commons-db shell seam: tool input as JSON on stdin, the result on stdout.
package exectool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/clicky/task"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/shell"
	dp "github.com/google/dotprompt/go/dotprompt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// maxErrorOutput bounds the stderr returned to the model with a failure.
const maxErrorOutput = 4 << 10

// Definitions builds one caller tool per inline tool, in name order, and binds
// each to the workspace that prepares its environment. Schemas are converted
// and compiled, and credentials modes resolved, here — so a bad declaration
// fails the run before the model ever sees the tool.
func Definitions(tools api.InlineTools, workspace *Workspace) ([]api.ToolDefinition, error) {
	if err := tools.Validate(); err != nil {
		return nil, err
	}
	definitions := make([]api.ToolDefinition, 0, len(tools))
	for _, name := range tools.Names() {
		definition, err := definition(name, tools[name], workspace)
		if err != nil {
			return nil, fmt.Errorf("tools.%s: %w", name, err)
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func definition(name string, tool api.InlineTool, workspace *Workspace) (api.ToolDefinition, error) {
	if err := workspace.bind(name, tool); err != nil {
		return api.ToolDefinition{}, err
	}
	input, err := InputSchema(tool.Input)
	if err != nil {
		return api.ToolDefinition{}, err
	}
	output, err := OutputSchema(tool.Output)
	if err != nil {
		return api.ToolDefinition{}, err
	}
	timeout, err := tool.ParseTimeout()
	if err != nil {
		return api.ToolDefinition{}, err
	}
	plan, err := tool.Retry.Plan()
	if err != nil {
		return api.ToolDefinition{}, err
	}
	readOnly, destructive := tool.Hints()
	run := runner{name: name, tool: tool, workspace: workspace, output: output, timeout: timeout, plan: plan}
	return api.ToolDefinition{
		Name:              name,
		Description:       tool.Description,
		InputSchema:       input,
		ReadOnlyHint:      readOnly,
		DestructiveHint:   destructive,
		DefaultPermission: tool.Permission,
		Handler:           run.handle,
	}, nil
}

// InputSchema converts a Picoschema or JSON Schema declaration into the object
// schema a tool takes; no declaration takes an empty object.
func InputSchema(declared any) (map[string]any, error) {
	if declared == nil {
		return api.ObjectSchema(nil), nil
	}
	raw, err := convertSchema("input", declared)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("input schema: %w", err)
	}
	return schema, nil
}

// OutputSchema converts the declared output schema; nil means the tool's
// stdout is returned as text.
func OutputSchema(declared any) (json.RawMessage, error) {
	if declared == nil {
		return nil, nil
	}
	raw, err := convertSchema("output", declared)
	if err != nil {
		return nil, err
	}
	// An empty document validates against any schema that compiles.
	if _, err := ai.ValidateStructuredJSON(raw, "{}"); err != nil {
		return nil, fmt.Errorf("output schema: %w", err)
	}
	return raw, nil
}

func convertSchema(section string, declared any) (json.RawMessage, error) {
	schema, err := dp.Picoschema(declared, &dp.PicoschemaOptions{})
	if err != nil {
		return nil, fmt.Errorf("%s schema: %w", section, err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("%s schema: %w", section, err)
	}
	return raw, nil
}

type runner struct {
	name      string
	tool      api.InlineTool
	workspace *Workspace
	output    json.RawMessage
	timeout   time.Duration
	plan      api.RetryPlan
}

func (r runner) handle(ctx context.Context, input map[string]any) (any, error) {
	stdin, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("%s: encode input: %w", r.name, err)
	}
	env, err := r.workspace.environment(r.name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.name, err)
	}
	var stdout string
	err = task.Retry(ctx, r.retryConfig(), func(ctx context.Context, _ int) error {
		stdout, err = r.attempt(ctx, env, stdin)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.name, err)
	}
	return r.result(stdout)
}

func (r runner) attempt(ctx context.Context, env *environment, stdin []byte) (string, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	dbctx := dbcontext.NewContext(attemptCtx)
	cmd, err := shell.CreateCommandFromScript(dbctx, r.tool.Exec)
	if err != nil {
		return "", err
	}
	cmd.Stdin = strings.NewReader(string(stdin))
	details, err := env.setup.Run(dbctx, cmd, shell.RunOptions{Wrap: env.wrap})
	switch {
	case err == nil:
		return details.Stdout, nil
	case ctx.Err() != nil:
		return "", context.Cause(ctx)
	case errors.Is(attemptCtx.Err(), context.DeadlineExceeded):
		return "", &execFailure{timedOut: true, timeout: r.timeout}
	case details != nil && details.ExitCode > 0:
		return "", &execFailure{exitCode: details.ExitCode, stderr: tail(details.Stderr)}
	default:
		return "", err
	}
}

func (r runner) retryConfig() task.RetryConfig {
	return task.RetryConfig{
		MaxRetries:    r.plan.Attempts - 1,
		BaseDelay:     r.plan.Delay,
		MaxDelay:      r.plan.MaxDelay,
		BackoffFactor: r.plan.Backoff,
		Retryable: func(err error) bool {
			var failure *execFailure
			if !errors.As(err, &failure) {
				return false
			}
			return failure.timedOut || len(r.plan.ExitCodes) == 0 || slices.Contains(r.plan.ExitCodes, failure.exitCode)
		},
	}
}

func (r runner) result(stdout string) (any, error) {
	if r.output == nil {
		return stdout, nil
	}
	violations, err := ai.ValidateStructuredJSON(r.output, stdout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.name, err)
	}
	if violations != "" {
		return nil, fmt.Errorf("%s: output does not match the declared schema: %s", r.name, violations)
	}
	var document any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		return nil, fmt.Errorf("%s: output is not JSON: %w", r.name, err)
	}
	return document, nil
}

// execFailure is an attempt that ran and failed: the only kind retry considers.
type execFailure struct {
	exitCode int
	stderr   string
	timedOut bool
	timeout  time.Duration
}

func (f *execFailure) Error() string {
	if f.timedOut {
		return fmt.Sprintf("timed out after %s", f.timeout)
	}
	if f.stderr == "" {
		return fmt.Sprintf("exited with code %d", f.exitCode)
	}
	return fmt.Sprintf("exited with code %d: %s", f.exitCode, f.stderr)
}

func tail(output string) string {
	if len(output) <= maxErrorOutput {
		return output
	}
	return "[output truncated]\n" + output[len(output)-maxErrorOutput:]
}
