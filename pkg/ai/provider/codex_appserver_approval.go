package provider

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	"github.com/flanksource/captain/pkg/api"
)

// codexPosture is the approval-relevant slice of a run's resolved permissions,
// recorded once per run so the server→client approval handler can answer from
// the policy the run declared.
type codexPosture struct {
	// grantsEscalation reports that the run asked for full access. It is the only
	// posture under which accepting an approval stays within what the caller
	// declared: every other posture bounds the agent more tightly than the thing
	// it is asking permission to do.
	grantsEscalation bool
	// planMode is a plan-only run, which must reach no side effect at all.
	planMode bool
}

func postureFor(req ai.Request) codexPosture {
	// The isolation boundary comes from the sandbox, the posture from
	// permissions; escalation needs both to allow it.
	mode := api.SandboxOff
	if req.Sandbox != nil {
		mode = req.Sandbox.Mode
	}
	approval := req.Permissions.Mode
	return codexPosture{
		grantsEscalation: mode == api.SandboxOff ||
			((mode == api.SandboxDocker || mode == api.SandboxGitAgent) && approval == api.PermissionBypass),
		planMode: approval == api.PermissionPlan,
	}
}

// allowsEscalation reports whether an approval request may be accepted.
func (p codexPosture) allowsEscalation() bool { return p.grantsEscalation && !p.planMode }

// handleApproval answers a server→client approval request from the run's
// posture.
//
// An approval request is codex asking to exceed the sandbox it was started
// with: accepting one runs a command outside the confinement or writes a file
// the sandbox denied. Answering "accept" unconditionally therefore made
// buildThreadStartParams' sandbox and approvalPolicy advisory — the model asked,
// captain said yes — so `mode: plan` and a read-only posture gated nothing on
// this backend while gating correctly on the exec path.
//
// Only a run that declared full access has already granted the escalation;
// every other posture declines and lets the turn continue, so the agent adapts
// rather than the run dying. The decision vocabularies are codex's own:
// accept|decline (item/*, v2) and approved|denied (the legacy methods), per
// `codex app-server generate-json-schema`.
func (c *CodexAppServer) handleApproval(method string, _ json.RawMessage) (any, *jsonrpc.RPCError) {
	allow := c.currentPosture().allowsEscalation()
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]string{"decision": codexDecision(allow, "accept", "decline")}, nil
	case "item/permissions/requestApproval":
		// Granting no additional permissions is right under every posture: the
		// thread already carries everything the run declared.
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}, nil
	case "item/tool/requestUserInput":
		return map[string]any{}, nil
	default:
		return map[string]string{"decision": codexDecision(allow, "approved", "denied")}, nil
	}
}

func codexDecision(allow bool, accept, decline string) string {
	if allow {
		return accept
	}
	return decline
}
