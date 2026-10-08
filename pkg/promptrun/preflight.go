package promptrun

import (
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
)

// Preflight validates one run without constructing providers or invoking hooks.
// Capability warnings are separate from invalid-state errors. Input estimates
// use four bytes per token; they are a guardrail, not an exact tokenizer count.
func Preflight(in Input) ([]string, error) {
	admission, err := preflight(in)
	return admission.warnings, err
}

type admission struct {
	model    api.Model
	timeout  time.Duration
	warnings []string
}

func preflight(in Input) (admission, error) {
	var out admission
	if err := validateApprovalOptions(in, false); err != nil {
		return out, err
	}
	if err := in.Resolved.Spec.ValidateRunnable(); err != nil {
		return out, fmt.Errorf("promptrun: %w", err)
	}
	var err error
	if out.timeout, err = runTimeout(in); err != nil {
		return out, err
	}
	if err := validateCommitOwnership(in); err != nil {
		return out, err
	}
	if in.MaxIterations < 0 {
		return out, fmt.Errorf("promptrun: Input.MaxIterations must be non-negative")
	}
	if in.Scope != "" && !in.Scope.Valid() {
		return out, fmt.Errorf("promptrun: invalid scope %q", in.Scope)
	}
	if err := validateVerificationIsolation(in); err != nil {
		return out, err
	}
	out.model = executingModel(in)
	if out.model.Provider != nil {
		if err := api.RequireToolPolicySupport(out.model.Provider, out.model.Mode, in.Resolved.Spec.Permissions); err != nil {
			return out, err
		}
	}
	needsModel := !in.Resolved.Spec.IsVerifyOnly() || declaresPrompts(in.Resolved.Spec.Workflow)
	if constructsProvider(in) {
		candidates, err := ai.ResolveCandidates(out.model)
		if err != nil {
			return out, fmt.Errorf("promptrun model: %w", err)
		}
		out.model = candidates[0]
		out.model.Fallbacks = candidates[1:]
	}
	if err := verify.ValidateDeclarations(in.Resolved.Spec.Workflow, verify.DeclarationOptions{Provider: in.Verify.Provider, Model: out.model.Name}); err != nil {
		return out, err
	}
	spec := in.Resolved.Spec
	if spec.Name == "" {
		spec.Name = out.model.Name
	}
	if err := spec.Validate(); err != nil {
		return out, fmt.Errorf("promptrun: %w", err)
	}
	spec.Model = out.model
	if err := validateAttachments(spec.Prompt.Attachments, out.model); err != nil {
		return out, err
	}
	if needsModel {
		if err := out.model.Validate(); err != nil {
			return out, fmt.Errorf("promptrun model: %w", err)
		}
		out.warnings, err = validateRuntime(in, spec)
		if err != nil {
			return out, err
		}
	}
	spec.Budget.Timeout = out.timeout.String()
	return out, nil
}

func validateRuntime(in Input, spec api.Spec) ([]string, error) {
	if constructsProvider(in) {
		if err := validateConstructionConfig(in); err != nil {
			return nil, err
		}
	}
	warnings, err := api.ValidateRuntimeSpec(spec)
	if err != nil {
		return warnings, fmt.Errorf("promptrun: %w", err)
	}
	callers, err := admitCallerTools(in, spec)
	if err != nil {
		return warnings, err
	}
	for _, model := range append([]api.Model{spec.Model}, spec.Fallbacks...) {
		if constructsProvider(in) && in.Config.SandboxSelection != nil {
			descriptor, _ := api.SandboxFor(in.Config.SandboxSelection.Kind)
			if err := descriptor.ValidateMode(model.Mode); err != nil {
				return warnings, err
			}
		}
		if err := callers.check(model); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}
