// Package hookrun executes a hook .prompt with the model its frontmatter
// declares. It is the model-rendered half shared by every hook event owner
// (pkg/plans, pkg/promptcontext); it lives apart from pkg/ai/prompt because
// building an agent needs pkg/ai/middleware, which itself imports the prompt
// package.
package hookrun

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/middleware"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
)

// Options names one execution of a hook prompt.
type Options struct {
	// Name is the request name the model call is reported under.
	Name string
	// Field selects one string field of the structured output; empty uses the
	// response text.
	Field string
	// Data renders the hook prompt.
	Data map[string]any
}

// Execute renders tmpl with opts.Data and runs it through an agent built from
// the frontmatter's model, returning the response text or opts.Field.
func Execute(ctx context.Context, tmpl *promptlib.Template, opts Options) (output string, err error) {
	req, cfg, err := tmpl.Render(promptlib.RenderOptions{Data: opts.Data})
	if err != nil {
		return "", err
	}
	agent, err := middleware.NewAgent(cfg)
	if err != nil {
		return "", fmt.Errorf("create agent: %w", err)
	}
	defer func() {
		if closeErr := agent.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close agent: %w", closeErr)
		}
	}()
	resp, err := agent.ExecutePrompt(ctx, ai.PromptRequest{Name: opts.Name, Spec: req})
	if err != nil {
		return "", err
	}
	if opts.Field == "" {
		return resp.Result, nil
	}
	return structuredField(resp.StructuredData, opts.Field)
}

func structuredField(data any, field string) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("encode structured output: %w", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", fmt.Errorf("structured output is not an object: %s", raw)
	}
	value, ok := fields[field].(string)
	if !ok {
		return "", fmt.Errorf("structured output has no string field %q: %s", field, raw)
	}
	return value, nil
}
