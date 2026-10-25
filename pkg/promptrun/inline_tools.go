package promptrun

import (
	"fmt"

	"github.com/flanksource/commons-db/shell"

	"github.com/flanksource/captain/pkg/ai"
	aitools "github.com/flanksource/captain/pkg/ai/tools"
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
	placed, sidecar := api.InlineTools{}, api.InlineTools{}
	for _, name := range declared.Names() {
		tool := declared[name]
		placement, err := sandbox.ResolveToolPlacement(tool.Placement)
		if err != nil {
			return fmt.Errorf("promptrun: tools.%s: %w", name, err)
		}
		tool.Placement = placement
		if placement == api.ToolPlacementSidecar {
			sidecar[name] = tool
			continue
		}
		placed[name] = tool
	}
	if err := checkSidecarTools(sidecar, in.Resolved.Spec); err != nil {
		return err
	}
	if len(sidecar) > 0 {
		// The relocating executor ships these declarations as they resolved
		// here, so the sidecar never re-derives a placement or credentials mode.
		resolved := api.InlineTools{}
		for name, tool := range declared {
			resolved[name] = tool
		}
		for name, tool := range sidecar {
			resolved[name] = tool
		}
		in.Resolved.Spec.Tools = resolved
	}
	if len(placed) == 0 {
		return nil
	}
	workspace := exectool.NewWorkspace(exectool.WorkspaceOptions{Cwd: in.Resolved.Spec.Cwd(), Setup: in.Resolved.Spec.Setup})
	definitions, err := exectool.Definitions(placed, workspace)
	if err != nil {
		return fmt.Errorf("promptrun: %w", err)
	}
	for _, existing := range in.Config.Tools {
		if _, clash := placed[existing.Name]; clash {
			return fmt.Errorf("promptrun: inline tool %q collides with a caller tool the host supplies", existing.Name)
		}
	}
	in.Config.Tools = append(append([]api.ToolDefinition(nil), in.Config.Tools...), definitions...)
	in.inlineTools = workspace
	return nil
}

// BindInlineTools binds a spec's inline tools for a host that drives its
// provider itself rather than through Run: a chat, whose turns share one tools
// environment. The tools are added to cfg.Tools and the returned workspace is
// nil when the spec declares none. The host points it at where setup left the
// chat (Workspace.SetRun), prepares it before the first turn and closes it
// when the chat ends. As Run's preflight does, a tool that would ask is
// refused on a runtime that needs a broker when cfg attaches none.
func BindInlineTools(spec *api.Spec, cfg *ai.Config) (*exectool.Workspace, error) {
	in := Input{Resolved: api.ResolvedSpec{Spec: *spec}, Config: *cfg}
	if err := bindInlineTools(&in); err != nil {
		return nil, err
	}
	if in.inlineTools == nil {
		return nil, nil
	}
	candidates, err := ai.ResolveCandidates(executingModel(in))
	if err != nil {
		return nil, fmt.Errorf("promptrun model: %w", err)
	}
	admission, err := admitCallerTools(in, in.Resolved.Spec)
	if err != nil {
		return nil, err
	}
	for _, model := range candidates {
		if err := admission.check(model); err != nil {
			return nil, err
		}
	}
	*spec, *cfg = in.Resolved.Spec, in.Config
	return in.inlineTools, nil
}

// checkSidecarTools refuses, before anything is dispatched, a sidecar tool the
// sidecar could not run as declared. Sidecar tools are not bound here — the
// sidecar's runner binds them next to the worktree — so this is the only
// point at which the supervisor can still say no. A sidecar tool inherits
// nothing from the run's setup, which is the supervisor's; it runs under
// run-task, where no one is present to approve a call. Each tool's resolved
// credentials mode is recorded on it.
func checkSidecarTools(tools api.InlineTools, spec api.Spec) error {
	if len(tools) == 0 {
		return nil
	}
	for _, name := range tools.Names() {
		tool := tools[name]
		credentials, err := tool.ResolveCredentials(api.ToolPlacementSidecar, shell.Setup{})
		if err != nil {
			return fmt.Errorf("promptrun: tools.%s: %w", name, err)
		}
		tool.Credentials = credentials
		tools[name] = tool
	}
	definitions, err := exectool.Definitions(tools, exectool.NewWorkspace(exectool.WorkspaceOptions{}))
	if err != nil {
		return fmt.Errorf("promptrun: %w", err)
	}
	resolved, err := aitools.ResolveDefinitions(definitions, aitools.ResolveOptions{Preferences: spec.ToolPreferences, Policy: spec.ToolPolicy})
	if err != nil {
		return fmt.Errorf("promptrun: %w", err)
	}
	for _, definition := range resolved {
		if definition.NeedsApproval() {
			return fmt.Errorf("promptrun: tools.%s needs approval, but a sidecar tool runs where no one can approve it; set permission: allow, mark it readonly, or place it on the supervisor", definition.Name)
		}
	}
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
