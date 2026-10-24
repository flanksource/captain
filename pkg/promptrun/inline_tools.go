package promptrun

import (
	"fmt"

	"github.com/flanksource/captain/pkg/ai/tools/exectool"
	"github.com/flanksource/captain/pkg/api"
)

// bindInlineTools turns the spec's frontmatter-declared tools into caller tools
// on Config.Tools, so admission, approval binding and every provider see them
// exactly like a host's own. Each tool's placement is checked against what the
// run's sandbox can serve, and its credentials mode resolved, before anything
// runs. The tools' environments are prepared by the workspace hook, which Hooks
// orders after setup, and torn down when the run ends.
func bindInlineTools(in *Input) error {
	declared := in.Resolved.Spec.Tools
	if len(declared) == 0 {
		return nil
	}
	if in.Provider != nil {
		return fmt.Errorf("promptrun: the spec declares inline tools, but a supplied Provider owns its own tool wiring and would never expose them")
	}
	sandbox, err := runSandbox(*in)
	if err != nil {
		return err
	}
	for _, name := range declared.Names() {
		if err := sandbox.ValidateToolPlacement(declared[name].Placement); err != nil {
			return fmt.Errorf("promptrun: tools.%s: %w", name, err)
		}
	}
	workspace := exectool.NewWorkspace(exectool.WorkspaceOptions{Cwd: in.Resolved.Spec.Cwd(), Setup: in.Resolved.Spec.Setup})
	definitions, err := exectool.Definitions(declared, workspace)
	if err != nil {
		return fmt.Errorf("promptrun: %w", err)
	}
	for _, existing := range in.Config.Tools {
		if _, clash := declared[existing.Name]; clash {
			return fmt.Errorf("promptrun: inline tool %q collides with a caller tool the host supplies", existing.Name)
		}
	}
	in.Config.Tools = append(append([]api.ToolDefinition(nil), in.Config.Tools...), definitions...)
	in.inlineTools = workspace
	return nil
}

// runSandbox is the descriptor of the sandbox the run executes under: the
// resolved adapter selection, else the spec's own mode, else off. Off and native
// resolve to no adapter selection and share their tool placements, so a native
// default that left no trace on the spec is answered correctly as off.
func runSandbox(in Input) (*api.SandboxDescriptor, error) {
	kind := api.SandboxOff
	if selection := in.Config.ResolvedSandbox(); selection != nil {
		kind = selection.Kind
	} else if ref := in.Resolved.Spec.Sandbox; ref != nil && ref.Mode != "" {
		kind = ref.Mode
	}
	descriptor, ok := api.SandboxFor(kind)
	if !ok {
		return nil, fmt.Errorf("promptrun: unknown sandbox %q; want one of: %s", kind, api.SandboxKindList())
	}
	return descriptor, nil
}
