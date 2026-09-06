package api

import "fmt"

// ValidateStructure checks supplied fields without requiring a model or prompt,
// resolving aliases, inspecting files, or testing runtime capabilities.
func (s Spec) ValidateStructure() error {
	if err := s.ValidateOptions(); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	expanded, err := s.Expand()
	if err != nil {
		return fmt.Errorf("model: %w", err)
	}
	for index, model := range append([]Model{expanded}, expanded.Fallbacks...) {
		model, err = model.Expand()
		if err == nil {
			err = model.ValidateOptions()
		}
		if err != nil {
			return fmt.Errorf("model candidate %d: %w", index, err)
		}
	}
	if err := s.ValidateRequestMode(); err != nil {
		return err
	}
	if s.ToolApproval != nil {
		if err := s.ToolApproval.Validate(); err != nil {
			return fmt.Errorf("tool approval: %w", err)
		}
	}
	if len(s.Messages) > 0 {
		if err := ValidateMessages(s.Messages); err != nil {
			return fmt.Errorf("messages: %w", err)
		}
	}
	if err := s.Prompt.SchemaStrictness.Validate(); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	for i, attachment := range s.Prompt.Attachments {
		if err := attachment.Validate(); err != nil {
			return fmt.Errorf("prompt attachment %d: %w", i+1, err)
		}
	}
	return s.validateRuntimeFields()
}

func (s Spec) validateRuntimeFields() error {
	if err := s.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if err := s.Permissions.Validate(); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	if err := s.ToolPreferences.Validate(); err != nil {
		return err
	}
	if err := s.ToolPolicy.Validate(); err != nil {
		return err
	}
	if err := s.Workflow.Validate(); err != nil {
		return fmt.Errorf("workflow: %w", err)
	}
	if s.Sandbox != nil {
		if err := s.Sandbox.Validate(); err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
	}
	return nil
}
