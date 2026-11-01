package api

import "fmt"

// validateHookTools checks every hook against the spec: a schema or input hook
// runs a readonly tool or a prompt, and a tool a hook names is declared in tools.
func (s Spec) validateHookTools() error {
	if err := s.validateReadonlyHooks("output.hooks", "a schema hook", HookEnvSchema, s.Prompt.SchemaHooks); err != nil {
		return err
	}
	if err := s.validateReadonlyHooks("input.hooks", "an input hook", HookEnvInput, s.Prompt.InputHooks); err != nil {
		return err
	}
	if s.Workflow == nil {
		return nil
	}
	for i, step := range s.Workflow.After {
		if _, ok := s.Tools[step.Tool]; step.Tool != "" && !ok {
			return fmt.Errorf("workflow.after[%d].tool %q is not declared in tools", i, step.Tool)
		}
	}
	return nil
}

// validateReadonlyHooks checks hooks that also run on render, so they must not
// change anything: each runs a declared readonly tool or a prompt.
func (s Spec) validateReadonlyHooks(field, noun string, env HookEnv, hooks []HookAction) error {
	for i, hook := range hooks {
		if err := hook.Validate(env); err != nil {
			return fmt.Errorf("%s[%d]: %w", field, i, err)
		}
		if len(hook.Commands) > 0 {
			return fmt.Errorf("%s[%d]: %s runs a tool or a prompt, not commands", field, i, noun)
		}
		if hook.Tool == "" {
			continue
		}
		tool, ok := s.Tools[hook.Tool]
		if !ok {
			return fmt.Errorf("%s[%d].tool %q is not declared in tools", field, i, hook.Tool)
		}
		if !tool.ReadOnly {
			return fmt.Errorf("%s[%d].tool %q is not readonly: %s also runs on render, so it must not change anything", field, i, hook.Tool, noun)
		}
	}
	return nil
}
