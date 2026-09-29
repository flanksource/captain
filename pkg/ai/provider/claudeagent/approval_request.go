package claudeagent

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

// canUseToolParams is the agent.ts can_use_tool request payload.
type canUseToolParams struct {
	Tool      string         `json:"tool"`
	Input     map[string]any `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
}

// canUseToolResult is the decision agent.ts maps onto an SDK PermissionResult.
// Interrupt denies the tool and ends the turn.
type canUseToolResult struct {
	Allow        bool           `json:"allow"`
	Message      string         `json:"message,omitempty"`
	UpdatedInput map[string]any `json:"updatedInput,omitempty"`
	Interrupt    bool           `json:"interrupt,omitempty"`
}

func deniedTool(message string) canUseToolResult {
	return canUseToolResult{Allow: false, Message: message}
}

// handleCanUseTool routes a tool-permission request to the active turn's
// OnApproval callback. The EventPermission for it is emitted by the seam that
// binds the callback, not here. With no callback it allows the tool, matching
// the default for non-brokered runs; with a callback but no active turn there is
// nobody to answer on, so it fails closed.
func (p *Provider) handleCanUseTool(params json.RawMessage) (any, *jsonrpc.RPCError) {
	var in canUseToolParams
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, &jsonrpc.RPCError{Code: -32602, Message: "invalid can_use_tool params: " + err.Error()}
	}
	ts := p.activeTurn()
	if ts == nil {
		if p.cfg.OnApproval != nil {
			return deniedTool(fmt.Sprintf("claude-agent: no active turn to answer the %q approval, so it is denied", in.Tool)), nil
		}
		return canUseToolResult{Allow: true, UpdatedInput: in.Input}, nil
	}
	request, err := canUseToolRequest(in, p.currentSessionID())
	if err != nil {
		return deniedTool(err.Error()), nil
	}
	// The plan-mode terminal signal is answered here, never brokered: nothing is
	// awaiting a human. The tool_use already streamed, so the turn still ends in
	// a plan terminal outcome.
	if decision, handled := ai.PlanTerminalPermission(ts.planMode, request); handled {
		return canUseToolResult{Allow: decision.Allow, Message: decision.Message}, nil
	}
	// A denied tool is refused before anyone is asked: no approval overrides it.
	if reason, denied, err := aitools.ApprovalDenial(ts.toolPolicy, request); err != nil || denied {
		if err != nil {
			reason = err.Error()
		}
		return deniedTool(reason), nil
	}
	if ts.onApproval == nil {
		return canUseToolResult{Allow: true, UpdatedInput: in.Input}, nil
	}
	decision, err := ts.onApproval(ts.ctx, request)
	if err != nil {
		return deniedTool(err.Error()), nil
	}
	return canUseToolAnswer(request, decision), nil
}

// canUseToolAnswer translates a decision the provider re-validates against the
// request, so a decision the host should never have sent is a deny naming why.
func canUseToolAnswer(request ai.ApprovalRequest, decision ai.ApprovalDecision) canUseToolResult {
	if err := decision.Validate(request); err != nil {
		return deniedTool(err.Error())
	}
	if decision.Interrupt {
		return canUseToolResult{Allow: false, Message: decision.Message, Interrupt: true}
	}
	updated := decision.UpdatedInput
	if request.Kind == api.ApprovalKindQuestion && decision.Allow {
		var err error
		if updated, err = askQuestionInput(request.Input, updated); err != nil {
			return deniedTool(err.Error())
		}
	}
	return canUseToolResult{Allow: decision.Allow, Message: decision.Message, UpdatedInput: updated}
}

// claudeApprovalScopes is every scope a Claude approval offers: durable
// "always" answers go through permission presets, never the SDK's own rules.
var claudeApprovalScopes = []api.ApprovalScope{api.ApprovalScopeRequest}

// fileToolOperations maps Claude's file tools onto the operation they perform
// and the input key naming the file.
var fileToolOperations = map[string]struct {
	operation api.FilesystemOperation
	pathKey   string
}{
	"Read":         {api.FilesystemRead, "file_path"},
	"Edit":         {api.FilesystemEdit, "file_path"},
	"MultiEdit":    {api.FilesystemEdit, "file_path"},
	"Write":        {api.FilesystemWrite, "file_path"},
	"NotebookEdit": {api.FilesystemEdit, "notebook_path"},
}

// canUseToolRequest builds the approval request for one canUseTool call, with
// the kind and payload the tool's input describes. Every canUseTool request was
// delivered by the pre-rename contract, so all of them are LegacyContract.
func canUseToolRequest(in canUseToolParams, sessionID string) (ai.ApprovalRequest, error) {
	request := ai.ApprovalRequest{
		Tool: in.Tool, Input: in.Input, ToolUseID: in.ToolUseID, SessionID: sessionID,
		Kind: api.ApprovalKindTool, Interruptible: true, SupportedScopes: claudeApprovalScopes,
		Info: &api.ToolInfo{Name: in.Tool}, LegacyContract: true,
	}
	if err := describeToolInput(&request); err != nil {
		return ai.ApprovalRequest{}, err
	}
	return request, request.Validate()
}

// describeToolInput sets the kind and payload for the tools whose input names
// what they touch. Anything else stays a plain tool approval.
func describeToolInput(request *ai.ApprovalRequest) error {
	input := request.Input
	switch request.Tool {
	case "Bash":
		command, err := requiredInputString(request.Tool, input, "command")
		if err != nil {
			return err
		}
		unsandboxed := input["dangerouslyDisableSandbox"] == true
		request.Kind, request.Escalates = api.ApprovalKindCommand, unsandboxed
		request.Command = &api.CommandApproval{Command: command, Unsandboxed: unsandboxed}
	case "WebFetch":
		network, err := webFetchNetwork(input)
		if err != nil {
			return err
		}
		request.Kind, request.Network = api.ApprovalKindNetwork, network
	case askUserQuestionTool:
		questions, err := api.TerminalQuestionsFromInput(input)
		if err != nil {
			return fmt.Errorf("%s input: %w", askUserQuestionTool, err)
		}
		request.Kind, request.Questions = api.ApprovalKindQuestion, questions
	case exitPlanModeTool:
		plan, err := api.TerminalPlanFromInput(input)
		if err != nil {
			return err
		}
		request.Kind, request.Plan = api.ApprovalKindPlan, plan
	default:
		file, ok := fileToolOperations[request.Tool]
		if !ok {
			return nil
		}
		path, err := requiredInputString(request.Tool, input, file.pathKey)
		if err != nil {
			return err
		}
		request.Kind = api.ApprovalKindFilesystem
		request.Filesystem = &api.FilesystemApproval{Operation: file.operation, Paths: []string{path}}
	}
	return nil
}

func webFetchNetwork(input map[string]any) (*api.NetworkApproval, error) {
	raw, err := requiredInputString("WebFetch", input, "url")
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("WebFetch url %q: %w", raw, err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("WebFetch url %q has no host", raw)
	}
	return &api.NetworkApproval{Host: parsed.Hostname(), Protocol: parsed.Scheme, URL: raw}, nil
}

const (
	askUserQuestionTool = "AskUserQuestion"
	// exitPlanModeTool reaches the callback only outside a plan-only run; inside
	// one it is the turn's terminal outcome (ai.PlanTerminalPermission).
	exitPlanModeTool = "ExitPlanMode"
)

// askQuestionInput shapes a broker's answers the way AskUserQuestion wants them:
// the input the agent asked with, plus one answer per question keyed by that
// question's exact text — a string, or a list when the question is multi-select.
//
// It rebuilds from the agent's own input rather than forwarding what the host
// sent, because the SDK rejects an updated input whose other fields differ from
// the original, and a rejected input leaves the run waiting on an unanswerable
// question. A decision carrying no answers is a plain approval and passes through.
func askQuestionInput(asked, updated map[string]any) (map[string]any, error) {
	if updated == nil || updated["answers"] == nil {
		return updated, nil
	}
	questions, err := api.TerminalQuestionsFromInput(asked)
	if err != nil {
		return nil, fmt.Errorf("AskUserQuestion input: %w", err)
	}
	resolved, err := api.AnswersForQuestions(questions, updated["answers"])
	if err != nil {
		return nil, fmt.Errorf("AskUserQuestion answers: %w", err)
	}
	answers := make(map[string]any, len(resolved))
	for _, answer := range resolved {
		if answer.Question.MultiSelect {
			answers[answer.Question.Text] = answer.Choices
			continue
		}
		answers[answer.Question.Text] = answer.Choices[0]
	}
	input := make(map[string]any, len(asked)+1)
	for key, value := range asked {
		input[key] = value
	}
	input["answers"] = answers
	return input, nil
}

func requiredInputString(tool string, input map[string]any, key string) (string, error) {
	value, ok := input[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("%s input has no %q", tool, key)
	}
	return value, nil
}
