package cli

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/commons-db/shell"
)

type AIRuntimeNormalizeOptions struct {
	Spec  api.Spec
	Saved captainconfig.Config
	Cwd   string
}

// Normalize derives execution context before saved defaults and final validation.
func (o AIRuntimeOptions) Normalize(options AIRuntimeNormalizeOptions) (api.SpecNormalization, error) {
	spec := options.Spec
	selection, err := resolveSandboxSelection(sandboxSelectionOptions{Selector: o.SandboxSelector(), Spec: spec, Saved: options.Saved.Sandbox})
	if err != nil {
		return api.SpecNormalization{}, err
	}
	fields := api.FieldPresence{}
	if options.Cwd != "" {
		before := shell.Setup{}
		if spec.Setup != nil {
			before = *spec.Setup
		}
		if err := normalizePromptContextDir(&spec, options.Cwd); err != nil {
			return api.SpecNormalization{}, err
		}
		for path := range normalizedSetupFields(before, *spec.Setup) {
			fields[path] = true
		}
	}
	skills := slices.Clone(spec.Memory.Skills)
	foldSkillPolicies(&spec)
	if !reflect.DeepEqual(skills, spec.Memory.Skills) {
		fields["/memory/skills"] = true
	}
	if spec.Sandbox != nil || o.SandboxSelector() != "" || selection.Kind != registry.SandboxOff {
		ref := sandboxRefFromSelection(selection)
		if spec.Sandbox != nil {
			ref.Policy, ref.Agent, ref.Dispatch = spec.Sandbox.Policy, spec.Sandbox.Agent, spec.Sandbox.Dispatch
		}
		if spec.Sandbox == nil || spec.Sandbox.Mode != ref.Mode {
			fields["/sandbox/mode"] = true
		}
		if spec.Sandbox == nil || spec.Sandbox.Backend != ref.Backend {
			fields["/sandbox/backend"] = true
		}
		spec.Sandbox = &ref
	}
	if forced := sandboxForcedMode(selection.Kind); forced != "" {
		if raw := strings.TrimSpace(o.Mode); raw != "" {
			mode, ok := registry.ParseRuntimeMode(raw)
			if !ok || mode != forced {
				return api.SpecNormalization{}, fmt.Errorf("sandbox %q requires %s mode, but --mode is %q", selection.Kind, forced, raw)
			}
		}
		if err := normalizeSandboxMode(&spec.Model, forced, "/mode", fields); err != nil {
			return api.SpecNormalization{}, err
		}
		for i := range spec.Fallbacks {
			if err := normalizeSandboxMode(&spec.Fallbacks[i], forced, fmt.Sprintf("/fallbacks/%d/mode", i), fields); err != nil {
				return api.SpecNormalization{}, err
			}
		}
	}
	return api.SpecNormalization{Spec: spec, Fields: fields, Source: api.FieldSource{Kind: api.FieldSourceContext, Name: "runtime context"}}, nil
}

func normalizeSandboxMode(model *api.Model, mode api.RuntimeMode, path string, fields api.FieldPresence) error {
	if model.Mode != "" && model.Mode != mode {
		return fmt.Errorf("sandbox requires %s mode, but %s declares %q", mode, path, model.Mode)
	}
	if model.Mode != mode {
		model.Mode = mode
		fields[path] = true
	}
	return nil
}

func normalizedSetupFields(before, after shell.Setup) api.FieldPresence {
	fields := api.FieldPresence{}
	if before.Cwd != after.Cwd {
		fields["/setup/cwd"] = true
	}
	if before.BaseDir != after.BaseDir {
		fields["/setup/baseDir"] = true
	}
	for i := range before.DotEnv {
		if before.DotEnv[i] != after.DotEnv[i] {
			fields[fmt.Sprintf("/setup/dotenv/%d", i)] = true
		}
	}
	if before.Checkout != nil && after.Checkout != nil {
		if before.Checkout.Path != after.Checkout.Path {
			fields["/setup/checkout/path"] = true
		}
		if before.Checkout.Worktree != nil && after.Checkout.Worktree != nil && before.Checkout.Worktree.Path != after.Checkout.Worktree.Path {
			fields["/setup/checkout/worktree/path"] = true
		}
	}
	return fields
}
