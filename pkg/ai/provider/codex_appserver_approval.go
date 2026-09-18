package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
	mode := api.SandboxKind("")
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
func (c *CodexAppServer) handleApproval(method string, params json.RawMessage) (any, *jsonrpc.RPCError) {
	allow := c.currentPosture().allowsEscalation()
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]string{"decision": codexDecision(allow, "accept", "decline")}, nil
	case "item/permissions/requestApproval":
		// Granting no additional permissions is right under every posture: the
		// thread already carries everything the run declared.
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}, nil
	case "item/tool/requestUserInput":
		answer, err := c.handleUserInput(params)
		if err != nil {
			return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
		}
		return answer, nil
	default:
		return map[string]string{"decision": codexDecision(allow, "approved", "denied")}, nil
	}
}

type codexUserInputRequest struct {
	ThreadID         string `json:"threadId"`
	TurnID           string `json:"turnId"`
	ItemID           string `json:"itemId"`
	IsBlocking       *bool  `json:"isBlocking"`
	AutoResolutionMs *int   `json:"autoResolutionMs"`
	Questions        []struct {
		ID       string `json:"id"`
		Question string `json:"question"`
		IsSecret bool   `json:"isSecret"`
	} `json:"questions"`
}

func (c *CodexAppServer) handleUserInput(raw json.RawMessage) (map[string]any, error) {
	var request codexUserInputRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fmt.Errorf("codex question request: %w", err)
	}
	if request.IsBlocking == nil || !*request.IsBlocking {
		return nil, fmt.Errorf("codex question request: nonblocking questions are unsupported")
	}
	if request.ThreadID == "" || request.TurnID == "" || request.ItemID == "" || len(request.Questions) == 0 {
		return nil, fmt.Errorf("codex question request needs threadId, turnId, itemId, and questions")
	}
	if c.cfg.CanUseTool == nil {
		return nil, fmt.Errorf("codex question request needs a question broker (Config.CanUseTool)")
	}
	ts := c.currentTurn()
	if ts == nil || ts.ctx == nil {
		return nil, fmt.Errorf("codex question request has no active turn")
	}
	threadID, turnID, err := ts.waitIDs(ts.ctx)
	if err != nil {
		return nil, err
	}
	if request.ThreadID != threadID || request.TurnID != turnID {
		return nil, fmt.Errorf("codex question request names thread %q turn %q; active thread %q turn %q", request.ThreadID, request.TurnID, threadID, turnID)
	}
	ids := make(map[string]struct{}, len(request.Questions))
	for _, question := range request.Questions {
		if question.ID == "" || strings.TrimSpace(question.Question) == "" {
			return nil, fmt.Errorf("codex question request has an empty question id or text")
		}
		if question.IsSecret {
			return nil, fmt.Errorf("codex question %q is secret and cannot use the durable question broker", question.ID)
		}
		if _, exists := ids[question.ID]; exists {
			return nil, fmt.Errorf("codex question request repeats id %q", question.ID)
		}
		ids[question.ID] = struct{}{}
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("codex question input: %w", err)
	}
	ctx := ts.ctx
	if request.AutoResolutionMs != nil {
		if *request.AutoResolutionMs < 0 {
			return nil, fmt.Errorf("codex question request has negative autoResolutionMs")
		}
		if *request.AutoResolutionMs == 0 {
			return map[string]any{"answers": map[string]any{}}, nil
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*request.AutoResolutionMs)*time.Millisecond)
		defer cancel()
	}
	decision, err := c.cfg.CanUseTool(ctx, api.PermissionRequest{
		Tool: "AskUserQuestion", Input: input, ToolUseID: request.ItemID, SessionID: request.ThreadID,
	})
	if err != nil {
		if request.AutoResolutionMs != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && ts.ctx.Err() == nil {
			return map[string]any{"answers": map[string]any{}}, nil
		}
		return nil, fmt.Errorf("codex question %s: %w", request.ItemID, err)
	}
	if !decision.Allow {
		return nil, fmt.Errorf("codex question %s rejected: %s", request.ItemID, strings.TrimSpace(decision.Message))
	}
	answers, err := codexUserInputAnswers(ids, decision.UpdatedInput["answers"])
	if err != nil {
		return nil, fmt.Errorf("codex question %s: %w", request.ItemID, err)
	}
	return map[string]any{"answers": answers}, nil
}

func codexUserInputAnswers(ids map[string]struct{}, value any) (map[string]any, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("broker response needs answers keyed by question id")
	}
	if len(raw) != len(ids) {
		return nil, fmt.Errorf("broker response has %d answers for %d questions", len(raw), len(ids))
	}
	answers := make(map[string]any, len(ids))
	for id := range ids {
		value, exists := raw[id]
		if !exists {
			return nil, fmt.Errorf("broker response is missing question %q", id)
		}
		var choices []string
		switch answer := value.(type) {
		case string:
			choices = []string{answer}
		case []string:
			choices = answer
		case []any:
			for _, choice := range answer {
				text, ok := choice.(string)
				if !ok {
					return nil, fmt.Errorf("broker response for %q contains a non-text answer", id)
				}
				choices = append(choices, text)
			}
		default:
			return nil, fmt.Errorf("broker response for %q has unsupported answer type %T", id, value)
		}
		if len(choices) == 0 {
			return nil, fmt.Errorf("broker response for %q is empty", id)
		}
		for i, choice := range choices {
			if strings.TrimSpace(choice) == "" {
				return nil, fmt.Errorf("broker response for %q has a blank answer", id)
			}
			choices[i] = strings.TrimSpace(choice)
		}
		answers[id] = map[string]any{"answers": choices}
	}
	return answers, nil
}

func codexDecision(allow bool, accept, decline string) string {
	if allow {
		return accept
	}
	return decline
}
