package aichat

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type requestError struct {
	status int
	text   string
}

func (e requestError) Error() string { return e.text }

func requestErrorStatus(err error) int {
	if typed, ok := err.(requestError); ok {
		return typed.status
	}
	return http.StatusBadRequest
}

func enforceRuntimeSettings(request ChatRequest, settings RuntimeSettings) error {
	if settings.MonthlyBudgetUSD > 0 && settings.CurrentMonthCostUSD >= settings.MonthlyBudgetUSD {
		return requestError{status: http.StatusPaymentRequired, text: fmt.Sprintf(
			"chat monthly cost budget exhausted: $%.4f used of $%.4f",
			settings.CurrentMonthCostUSD, settings.MonthlyBudgetUSD,
		)}
	}
	if settings.MonthlyTokenBudget > 0 && settings.CurrentMonthTokens >= settings.MonthlyTokenBudget {
		return requestError{status: http.StatusPaymentRequired, text: fmt.Sprintf(
			"chat monthly token budget exhausted: %d used of %d",
			settings.CurrentMonthTokens, settings.MonthlyTokenBudget,
		)}
	}
	if settings.MaxInputTokens <= 0 {
		return nil
	}
	raw, err := json.Marshal(struct {
		Messages     []UIMessage       `json:"messages,omitempty"`
		Context      string            `json:"context,omitempty"`
		ContextItems []ChatContextItem `json:"contextItems,omitempty"`
	}{Messages: request.Messages, Context: request.Context, ContextItems: request.ContextItems})
	if err != nil {
		return fmt.Errorf("estimate chat input tokens: %w", err)
	}
	estimated := (len(raw) + 3) / 4
	if estimated > settings.MaxInputTokens {
		return requestError{status: http.StatusRequestEntityTooLarge, text: fmt.Sprintf(
			"chat input is about %d tokens, exceeding the configured per-turn limit of %d",
			estimated, settings.MaxInputTokens,
		)}
	}
	return nil
}
