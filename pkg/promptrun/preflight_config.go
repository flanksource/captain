package promptrun

import (
	"fmt"
	"reflect"

	"github.com/flanksource/captain/pkg/api"
)

func constructsProvider(in Input) bool {
	return in.Provider == nil && (!in.Request.IsVerifyOnly() || declaresPrompts(in.Request.Workflow) && in.Verify.Provider == nil)
}

func validateVerificationIsolation(in Input) error {
	if !in.Request.IsVerifyOnly() || in.Provider != nil || constructsProvider(in) {
		return nil
	}
	if sandbox := in.Request.Sandbox; sandbox != nil && sandbox.Mode != api.SandboxOff {
		return fmt.Errorf("promptrun: verify-only execution without a run provider cannot apply sandbox mode %q", sandbox.Mode)
	}
	if selection := in.Config.SandboxSelection; selection != nil && selection.Kind != api.SandboxOff {
		return fmt.Errorf("promptrun: verify-only execution without a run provider cannot apply Config.SandboxSelection %q", selection.Kind)
	}
	return nil
}

func validateConstructionConfig(in Input) error {
	if err := in.Config.Budget.Validate(); err != nil {
		return fmt.Errorf("promptrun Config budget: %w", err)
	}
	if in.Config.CallerTools != nil {
		if err := in.Config.CallerTools.Validate(); err != nil {
			return err
		}
	}
	selection := in.Config.SandboxSelection
	if selection != nil {
		if _, ok := api.SandboxFor(selection.Kind); !ok {
			return fmt.Errorf("unknown sandbox kind %q; want one of: %s", selection.Kind, api.SandboxKindList())
		}
		if err := selection.Dispatch.Validate(); err != nil {
			return err
		}
	}
	if declared := in.Request.Sandbox; declared != nil {
		if selection == nil && (declared.Mode == api.SandboxDocker || declared.Mode == api.SandboxGitAgent) {
			return fmt.Errorf("sandbox mode %q requires a resolved Config.SandboxSelection before running", declared.Mode)
		}
		if selection != nil && declared.Mode != selection.Kind {
			return fmt.Errorf("sandbox mode %q does not match Config.SandboxSelection kind %q", declared.Mode, selection.Kind)
		}
		if selection != nil {
			if declared.Backend != "" && declared.Backend != selection.Name {
				return fmt.Errorf("sandbox backend %q does not match Config.SandboxSelection name %q", declared.Backend, selection.Name)
			}
			if declared.Agent != "" && declared.Agent != selection.Agent {
				return fmt.Errorf("sandbox agent %q does not match Config.SandboxSelection agent %q", declared.Agent, selection.Agent)
			}
			if declared.Dispatch != nil && !reflect.DeepEqual(declared.Dispatch, selection.Dispatch) {
				return fmt.Errorf("sandbox dispatch policy does not match Config.SandboxSelection dispatch policy")
			}
		}
	}
	return nil
}
