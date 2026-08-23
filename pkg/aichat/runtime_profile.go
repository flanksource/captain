package aichat

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/flanksource/captain/pkg/api"
)

type requestError struct {
	status int
	text   string
}

func (e requestError) Error() string { return e.text }

func requestErrorStatus(err error) int {
	var typed requestError
	if errors.As(err, &typed) {
		return typed.status
	}
	if errors.Is(err, ErrThreadRuntimeConflict) {
		return http.StatusConflict
	}
	if errors.Is(err, ErrThreadNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

func enforceRuntimeProfile(request ChatRequest, resolved api.ResolvedSpec) error {
	if err := enforceRuntimeQuotas(resolved); err != nil {
		return err
	}
	maxInputTokens := resolved.Constraints.Limits.MaxInputTokens
	if maxInputTokens <= 0 {
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
	if estimated > maxInputTokens {
		return requestError{status: http.StatusRequestEntityTooLarge, text: fmt.Sprintf(
			"chat input is about %d tokens, exceeding the configured per-turn limit of %d",
			estimated, maxInputTokens,
		)}
	}
	return nil
}

func enforceRuntimeQuotas(resolved api.ResolvedSpec) error {
	for _, quota := range resolved.Constraints.Quotas {
		if quota.CostLimitUSD > 0 && quota.CostUsedUSD >= quota.CostLimitUSD {
			return requestError{status: http.StatusPaymentRequired, text: fmt.Sprintf(
				"chat %s quota %q from layer %q exhausted: $%.4f used of $%.4f",
				quota.Scope, quota.Name, quota.Layer, quota.CostUsedUSD, quota.CostLimitUSD,
			)}
		}
		if quota.TokenLimit > 0 && quota.TokensUsed >= quota.TokenLimit {
			return requestError{status: http.StatusPaymentRequired, text: fmt.Sprintf(
				"chat %s quota %q from layer %q exhausted: %d tokens used of %d",
				quota.Scope, quota.Name, quota.Layer, quota.TokensUsed, quota.TokenLimit,
			)}
		}
	}
	return nil
}
