package provider

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	aitools "github.com/flanksource/captain/pkg/ai/tools"
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
	// run is the part of the request approvals are judged against: its sandbox,
	// posture, workspace and tool policy.
	run ai.Request
}

func postureFor(req ai.Request) codexPosture {
	// The isolation boundary comes from the sandbox, the posture from
	// permissions; escalation needs both to allow it.
	mode := api.SandboxKind("")
	if req.Sandbox != nil {
		mode = req.Sandbox.Mode
	}
	approval := req.Permissions.Mode
	return codexPosture{
		grantsEscalation: mode == api.SandboxOff ||
			((mode == api.SandboxDocker || mode == api.SandboxGitAgent) && approval == api.PermissionBypass),
		planMode: approval == api.PermissionPlan,
		run: ai.Request{
			Setup: req.Setup, Sandbox: req.Sandbox, Permissions: req.Permissions,
			ToolPreferences: req.ToolPreferences, ToolPolicy: req.ToolPolicy,
		},
	}
}

// allowsEscalation reports whether an approval request may be accepted.
func (p codexPosture) allowsEscalation() bool { return p.grantsEscalation && !p.planMode }

func (p codexPosture) toolPolicy() aitools.ResolveOptions {
	return aitools.ResolveOptions{Preferences: p.run.ToolPreferences, Policy: p.run.ToolPolicy}
}

// codexApprovalRoute is how one server→client approval method is answered: the
// native answers for a refusal and for a run with no callback, and how to turn
// the params into a request for the callback.
type codexApprovalRoute struct {
	tool string
	kind api.ApprovalKind
	// denied answers plan mode and the deny floor.
	denied any
	// fallback answers a run with no callback, or a legacy callback that skipped
	// the request: exactly what the run got before the callback was consulted.
	fallback any
	// cancelled, when set, answers a request whose turn ended while it waited.
	cancelled any
	build     func(req jsonrpc.ServerRequest, ts *turnState) (codexApproval, error)
}

// codexApproval is one request built for the callback, with the translation of
// its decision back into the native answer.
type codexApproval struct {
	request api.ApprovalRequest
	answer  func(api.ApprovalDecision) (any, error)
}

// handleApproval answers a server→client request from Codex.
//
// An approval request is codex asking to exceed the sandbox it was started
// with: accepting one runs a command outside the confinement or writes a file
// the sandbox denied. With an OnApproval callback attached the callback decides;
// without one only a run that declared full access has already granted the
// escalation, and every other posture declines and lets the turn continue, so
// the agent adapts rather than the run dying. Plan mode and the run's tool deny
// rules are floors no callback can lift. The decision vocabularies are codex's
// own, per `codex app-server generate-json-schema`.
func (c *CodexAppServer) handleApproval(req jsonrpc.ServerRequest) (any, *jsonrpc.RPCError) {
	posture := c.currentPosture()
	decision := codexDecisionAnswer(posture.allowsEscalation())
	switch req.Method {
	case "item/commandExecution/requestApproval":
		return c.resolveApproval(req, posture, codexApprovalRoute{
			tool: "exec_command", kind: api.ApprovalKindCommand,
			denied: codexDecisionAnswer(false), fallback: decision, build: buildCodexCommandApproval,
		})
	case "item/fileChange/requestApproval":
		return c.resolveApproval(req, posture, codexApprovalRoute{
			tool: "apply_patch", kind: api.ApprovalKindFilesystem,
			denied: codexDecisionAnswer(false), fallback: decision, build: posture.buildFileChangeApproval,
		})
	case "item/permissions/requestApproval":
		// Granting no additional permissions is right under every posture: the
		// thread already carries everything the run declared.
		return c.resolveApproval(req, posture, codexApprovalRoute{
			tool: "request_permissions", kind: api.ApprovalKindPermissions,
			denied: codexNoGrant(), fallback: codexNoGrant(), build: posture.buildPermissionsApproval,
		})
	case "mcpServer/elicitation/request":
		return c.resolveApproval(req, posture, codexApprovalRoute{
			tool: codexElicitationTool, kind: api.ApprovalKindElicitation,
			denied: codexElicitationAnswer("decline"), fallback: codexElicitationAnswer("decline"),
			cancelled: codexElicitationAnswer("cancel"), build: buildCodexElicitation,
		})
	case "item/tool/requestUserInput":
		answer, err := c.handleUserInput(req.Params)
		if err != nil {
			return nil, codexRPCError(err)
		}
		return answer, nil
	case "execCommandApproval", "applyPatchApproval":
		return nil, codexRPCError(fmt.Errorf("codex app-server sent the pre-v2 %s request; captain answers only the item/* approval methods", req.Method))
	default:
		return nil, &jsonrpc.RPCError{Code: -32601, Message: fmt.Sprintf("codex app-server request %s is not supported by captain", req.Method)}
	}
}

// resolveApproval applies the floors, then asks the callback and translates its
// decision. The params are read only when a callback will see them, so a run
// with no callback answers exactly as it did before callbacks were consulted.
func (c *CodexAppServer) resolveApproval(req jsonrpc.ServerRequest, posture codexPosture, route codexApprovalRoute) (any, *jsonrpc.RPCError) {
	if posture.planMode {
		return route.denied, nil
	}
	_, denied, err := aitools.ApprovalDenial(posture.toolPolicy(), api.ApprovalRequest{Tool: route.tool, Kind: route.kind})
	if err != nil {
		return nil, codexRPCError(fmt.Errorf("codex %s approval: %w", route.kind, err))
	}
	if denied {
		return route.denied, nil
	}
	if c.cfg.OnApproval == nil {
		return route.fallback, nil
	}
	ts, err := c.approvalTurn(req.Params)
	if err != nil {
		return nil, codexRPCError(fmt.Errorf("codex %s approval: %w", route.kind, err))
	}
	approval, err := route.build(req, ts)
	if err != nil {
		return nil, codexRPCError(err)
	}
	request := approval.request
	request.SessionID, request.TurnID = ts.ids()
	if err := request.Validate(); err != nil {
		return nil, codexRPCError(fmt.Errorf("codex %s approval: %w", route.kind, err))
	}
	decision, err := c.cfg.OnApproval(ts.ctx, request)
	switch {
	case errors.Is(err, api.ErrLegacyApprovalSkipped):
		api.LogLegacyApprovalSkip(request.SessionID, request.Kind, err, fmt.Sprintf("%v", route.fallback))
		return route.fallback, nil
	case err != nil && route.cancelled != nil && ts.ctx.Err() != nil:
		return route.cancelled, nil
	case err != nil:
		return nil, codexRPCError(fmt.Errorf("codex %s approval %s: %w", request.Kind, request.ToolUseID, err))
	}
	if err := decision.Validate(request); err != nil {
		return nil, codexRPCError(fmt.Errorf("codex %s approval %s: %w", request.Kind, request.ToolUseID, err))
	}
	answer, err := approval.answer(decision)
	if err != nil {
		return nil, codexRPCError(fmt.Errorf("codex %s approval %s: %w", request.Kind, request.ToolUseID, err))
	}
	return answer, nil
}

// approvalTurn is the active turn a request belongs to. A request naming another
// thread or turn is refused rather than answered on the wrong run's behalf.
func (c *CodexAppServer) approvalTurn(raw json.RawMessage) (*turnState, error) {
	var ids struct {
		ThreadID string  `json:"threadId"`
		TurnID   *string `json:"turnId"`
	}
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}
	ts := c.currentTurn()
	if ts == nil || ts.ctx == nil {
		return nil, fmt.Errorf("no active turn")
	}
	threadID, turnID, err := ts.waitIDs(ts.ctx)
	if err != nil {
		return nil, err
	}
	if ids.ThreadID != threadID || (ids.TurnID != nil && *ids.TurnID != turnID) {
		return nil, fmt.Errorf("request names thread %q turn %q; active thread %q turn %q", ids.ThreadID, stringValue(ids.TurnID), threadID, turnID)
	}
	return ts, nil
}

func codexRPCError(err error) *jsonrpc.RPCError {
	return &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
}

func codexDecisionAnswer(allow bool) map[string]string {
	if allow {
		return map[string]string{"decision": "accept"}
	}
	return map[string]string{"decision": "decline"}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// codexGranularApprovalPolicy is the approval policy a run whose OnApproval
// callback answers every request kind sends instead of an asking string policy:
// the string forms never raise permission requests or MCP elicitations.
func codexGranularApprovalPolicy() map[string]any {
	return map[string]any{"granular": map[string]any{
		"sandbox_approval": true, "rules": true, "mcp_elicitations": true,
		"request_permissions": true, "skill_approval": false,
	}}
}

// applyApprovalPolicy switches an asking approvalPolicy in thread/start,
// thread/resume or turn/start params to the granular form when a current
// callback will answer. A legacy CanUseTool callback keeps the string policy,
// so its run raises no request kind it would only answer natively.
func (c *CodexAppServer) applyApprovalPolicy(params map[string]any) {
	if c.cfg.OnApproval == nil || c.cfg.LegacyApprovals() {
		return
	}
	policy, stated := params["approvalPolicy"].(string)
	if !stated {
		return
	}
	switch api.CodexApprovalPolicy(policy) {
	case api.CodexApprovalNever:
	case api.CodexApprovalOnRequest, api.CodexApprovalUntrusted:
		params["approvalPolicy"] = codexGranularApprovalPolicy()
	default:
		panic(fmt.Sprintf("unsupported translated Codex approval policy %q", policy))
	}
}
