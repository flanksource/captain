package hookaction

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
)

// ApplySchemaHooks runs spec's output.hooks in order. Each receives the current
// output schema with the run's vars and metadata (merged with its rendered
// `with`) and returns the whole new schema of the same type, which replaces
// Prompt.TypeSafeQuestions or Prompt.SchemaJSON once it validates. The first
// failure stops the chain and leaves the schema as the last valid hook left it.
// notify, when set, reports each hook's outcome.
func ApplySchemaHooks(ctx context.Context, spec *api.Spec, runner Runner, notify func(string)) error {
	for i, hook := range spec.Prompt.SchemaHooks {
		label := hookLabel("output.hooks", i, hook)
		vars := map[string]any{
			"vars": orEmpty(spec.Prompt.Variables), "metadata": stringMap(spec.Prompt.Metadata),
			"labels": stringMap(spec.Labels), "model": spec.Name,
		}
		run, err := hook.EvalIf(api.HookEnvSchema, vars)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if !run {
			report(notify, fmt.Sprintf("[pre-run] %s skipped: if %q is false", label, hook.If))
			continue
		}
		if err := applySchemaHook(ctx, spec, hook, vars, runner); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		report(notify, fmt.Sprintf("[pre-run] %s replaced the %s output schema", label, schemaTypeOf(spec.Prompt)))
	}
	return nil
}

func applySchemaHook(ctx context.Context, spec *api.Spec, hook api.HookAction, vars map[string]any, runner Runner) error {
	prompt, cwd := &spec.Prompt, spec.Cwd()
	schemaType := schemaTypeOf(*prompt)
	current, err := currentSchema(*prompt, schemaType)
	if err != nil {
		return err
	}
	base := map[string]any{"schema": current, "schemaType": string(schemaType), "vars": vars["vars"], "metadata": vars["metadata"]}
	out, err := Run(ctx, Call{Action: hook, Vars: vars, Base: base, Cwd: cwd}, runner)
	if err != nil {
		return err
	}
	object, err := jsonObject(out)
	if err != nil {
		return fmt.Errorf("emitted schema: %w", err)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return fmt.Errorf("emitted schema: %w", err)
	}
	if err := validateSchema(schemaType, raw); err != nil {
		return fmt.Errorf("emitted schema: %w", err)
	}
	if schemaType == api.SchemaTypeTypeSafe {
		prompt.TypeSafeQuestions = raw
	} else {
		prompt.SchemaJSON = raw
	}
	return nil
}

func schemaTypeOf(prompt api.Prompt) api.SchemaType {
	if prompt.OutputSchemaType == "" && len(prompt.SchemaJSON) > 0 {
		return api.SchemaTypeJSONSchema
	}
	return prompt.OutputSchemaType
}

func currentSchema(prompt api.Prompt, schemaType api.SchemaType) (any, error) {
	var raw json.RawMessage
	switch schemaType {
	case api.SchemaTypeTypeSafe:
		raw = prompt.TypeSafeQuestions
	case api.SchemaTypeJSONSchema:
		raw = prompt.SchemaJSON
	default:
		return nil, fmt.Errorf("output.hooks need output.schemaType typesafe-schema or json-schema, got %q", schemaType)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("output.hooks need a declared %s output.schema to start from", schemaType)
	}
	var current any
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, fmt.Errorf("decode current output schema: %w", err)
	}
	return current, nil
}

// jsonObject is a hook action's output as a JSON object: a tool's decoded
// result, or text that decodes to one.
func jsonObject(out any) (map[string]any, error) {
	if text, ok := out.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return nil, fmt.Errorf("output is not JSON: %w", err)
		}
		out = decoded
	}
	object, ok := out.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("want a JSON object, got %T", out)
	}
	return object, nil
}

func validateSchema(schemaType api.SchemaType, raw json.RawMessage) error {
	if schemaType == api.SchemaTypeTypeSafe {
		return promptlib.ValidateTypeSafeQuestions(raw)
	}
	_, err := ai.ValidateStructuredJSON(raw, "{}")
	return err
}

func hookLabel(field string, index int, hook api.HookAction) string {
	if hook.Tool != "" {
		return fmt.Sprintf("%s[%d] (tool %s)", field, index, hook.Tool)
	}
	return fmt.Sprintf("%s[%d] (prompt %s)", field, index, hook.Prompt)
}

func report(notify func(string), text string) {
	if notify != nil {
		notify(text)
	}
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func stringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
