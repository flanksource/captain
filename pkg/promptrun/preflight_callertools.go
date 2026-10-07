package promptrun

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

// callerToolListLimit bounds how many tool names an admission error spells out.
const callerToolListLimit = 10

// callerToolAdmission is what preflight learned about a constructed run's caller
// tools, independent of which candidate model ends up executing them.
type callerToolAdmission struct {
	// tools reports that the run carries Config.Tools.
	tools bool
	// brokered reports that an approval broker answers ask calls.
	brokered bool
	// asking names the caller tools — and the spec rules — that resolve to ask.
	asking []string
}

// admitCallerTools resolves the run's caller tools through the same function
// every provider resolves them with, so preflight and execution cannot disagree
// about which calls will need approval. A supplied provider owns its own tool
// wiring, so only a run promptrun constructs is admitted here.
func admitCallerTools(in Input, spec api.Spec) (callerToolAdmission, error) {
	binding, err := in.Config.Approvals()
	if err != nil {
		return callerToolAdmission{}, err
	}
	if !constructsProvider(in) {
		return callerToolAdmission{}, nil
	}
	out := callerToolAdmission{tools: len(in.Config.Tools) > 0, brokered: binding.Func != nil || in.Approvals != nil}
	if !out.tools && in.Config.CallerTools == nil {
		// No caller tool is served, so an ask rule has nothing to govern.
		return out, nil
	}
	out.asking, err = askingCallerTools(in.Config.Tools, spec)
	return out, err
}

// askingCallerTools returns, sorted, every caller tool that resolves to ask plus
// every spec preference or rule that says ask. The rules are kept because a
// Config.CallerTools endpoint serves tools preflight cannot enumerate: there the
// rule is the only evidence a call will need approval.
func askingCallerTools(definitions []api.ToolDefinition, spec api.Spec) ([]string, error) {
	permissions, err := aitools.ResolveToolPermissions(definitions, aitools.ResolveOptions{Preferences: spec.ToolPreferences, Policy: spec.ToolPolicy})
	if err != nil {
		return nil, fmt.Errorf("promptrun caller tools: %w", err)
	}
	asking := map[string]bool{}
	for name, policy := range permissions {
		if policy == api.ToolPolicyAsk {
			asking[name] = true
		}
	}
	for key, policy := range spec.ToolPreferences {
		if policy == api.ToolPolicyAsk {
			asking[key] = true
		}
	}
	for i, rule := range spec.ToolPolicy {
		if rule.Policy != api.ToolPolicyAsk {
			continue
		}
		if len(rule.Name) > 0 {
			asking[strings.Join(rule.Name, ",")] = true
		} else {
			asking[fmt.Sprintf("toolPolicy[%d]", i)] = true
		}
	}
	return slices.Sorted(maps.Keys(asking)), nil
}

// check refuses a candidate model on which the run's caller tools could not
// work: MCP is their transport, the runtime must be able to expose them, and an
// ask must have a broker to answer it.
func (a callerToolAdmission) check(spec api.Spec, model api.Model) error {
	runtime := api.RuntimeOf(model.Provider, model.Mode)
	if a.tools && spec.Permissions.MCP.Disabled {
		return fmt.Errorf("promptrun: MCP is disabled, but caller tools reach the agent over captain's MCP endpoint, so the run on %q would have none of them; enable MCP or drop Config.Tools", runtime)
	}
	if a.tools && !api.SupportsCallerTools(model.Provider, model.Mode) {
		return fmt.Errorf("promptrun: runtime %q cannot expose caller tools, so the agent would have none of them; choose an agent-mode model (e.g. agent:sonnet)", runtime)
	}
	if a.brokered || len(a.asking) == 0 {
		return nil
	}
	if api.PermissionCapabilitiesFor(runtime).ToolPolicySupport(api.ProvenanceCaller, api.ToolPolicyAsk).Kind != api.SupportRequiresBroker {
		return nil
	}
	return fmt.Errorf("promptrun: caller tools %s resolve to ask on %q but no approval broker is attached; allow them with a permission set (--perms) or attach Config.OnApproval (e.g. a terminal broker)", callerToolList(a.asking), runtime)
}

func callerToolList(names []string) string {
	if len(names) <= callerToolListLimit {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:callerToolListLimit], ", "), len(names)-callerToolListLimit)
}
