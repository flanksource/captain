package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

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

// handleUserInput answers item/tool/requestUserInput through the callback. A
// Codex question cannot be declined natively, so a refusal is an RPC error.
func (c *CodexAppServer) handleUserInput(raw json.RawMessage) (map[string]any, error) {
	request, err := parseCodexUserInput(raw)
	if err != nil {
		return nil, err
	}
	if c.cfg.OnApproval == nil {
		return nil, fmt.Errorf("codex question request needs a question broker (Config.OnApproval)")
	}
	ts, err := c.approvalTurn(raw)
	if err != nil {
		return nil, fmt.Errorf("codex question request: %w", err)
	}
	approval, err := codexQuestionApproval(request, raw)
	if err != nil {
		return nil, err
	}
	questions := approval.Questions
	if reason, denied, err := aitools.ApprovalDenial(c.currentPosture().toolPolicy(), approval); err != nil || denied {
		if err != nil {
			return nil, fmt.Errorf("codex question %s: %w", request.ItemID, err)
		}
		return nil, fmt.Errorf("codex question %s refused: %s", request.ItemID, reason)
	}
	ctx := ts.ctx
	if request.AutoResolutionMs != nil {
		if *request.AutoResolutionMs == 0 {
			return map[string]any{"answers": map[string]any{}}, nil
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*request.AutoResolutionMs)*time.Millisecond)
		defer cancel()
	}
	decision, err := c.cfg.OnApproval(ctx, approval)
	if err != nil {
		if request.AutoResolutionMs != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && ts.ctx.Err() == nil {
			return map[string]any{"answers": map[string]any{}}, nil
		}
		return nil, fmt.Errorf("codex question %s: %w", request.ItemID, err)
	}
	if !decision.Allow {
		return nil, fmt.Errorf("codex question %s rejected: %s", request.ItemID, strings.TrimSpace(decision.Message))
	}
	answers, err := codexUserInputAnswers(questions, decision.UpdatedInput["answers"])
	if err != nil {
		return nil, fmt.Errorf("codex question %s: %w", request.ItemID, err)
	}
	return map[string]any{"answers": answers}, nil
}

func parseCodexUserInput(raw json.RawMessage) (codexUserInputRequest, error) {
	var request codexUserInputRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return request, fmt.Errorf("codex question request: %w", err)
	}
	if request.IsBlocking == nil || !*request.IsBlocking {
		return request, fmt.Errorf("codex question request: nonblocking questions are unsupported")
	}
	if request.ThreadID == "" || request.TurnID == "" || request.ItemID == "" || len(request.Questions) == 0 {
		return request, fmt.Errorf("codex question request needs threadId, turnId, itemId, and questions")
	}
	if request.AutoResolutionMs != nil && *request.AutoResolutionMs < 0 {
		return request, fmt.Errorf("codex question request has negative autoResolutionMs")
	}
	return request, nil
}

// codexQuestionApproval carries each question through, secret ones included:
// whether a secret may be asked is the broker's decision, not the provider's.
func codexQuestionApproval(request codexUserInputRequest, raw json.RawMessage) (api.ApprovalRequest, error) {
	input, err := codexInput(raw)
	if err != nil {
		return api.ApprovalRequest{}, fmt.Errorf("codex question input: %w", err)
	}
	questions := make([]api.TerminalQuestion, 0, len(request.Questions))
	seen := make(map[string]struct{}, len(request.Questions))
	for _, question := range request.Questions {
		if question.ID == "" || strings.TrimSpace(question.Question) == "" {
			return api.ApprovalRequest{}, fmt.Errorf("codex question request has an empty question id or text")
		}
		if _, exists := seen[question.ID]; exists {
			return api.ApprovalRequest{}, fmt.Errorf("codex question request repeats id %q", question.ID)
		}
		seen[question.ID] = struct{}{}
		// MultiSelect: Codex takes a list of answers per question whatever the
		// question was, so every one of them accepts several.
		questions = append(questions, api.TerminalQuestion{
			ID: question.ID, Text: strings.TrimSpace(question.Question), MultiSelect: true, Secret: question.IsSecret,
		})
	}
	return api.ApprovalRequest{
		Tool: "AskUserQuestion", Input: input, ToolUseID: request.ItemID, SessionID: request.ThreadID,
		Kind: api.ApprovalKindQuestion, Questions: questions, LegacyContract: true,
	}, nil
}

// codexUserInputAnswers shapes the broker's answers the way app-server wants
// them: keyed by the question id Codex issued, each a list of chosen texts.
func codexUserInputAnswers(questions []api.TerminalQuestion, value any) (map[string]any, error) {
	resolved, err := api.AnswersForQuestions(questions, value)
	if err != nil {
		return nil, fmt.Errorf("broker response: %w", err)
	}
	answers := make(map[string]any, len(resolved))
	for _, answer := range resolved {
		answers[answer.Question.ID] = map[string]any{"answers": answer.Choices}
	}
	return answers, nil
}
