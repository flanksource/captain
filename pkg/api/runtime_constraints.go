package api

import "fmt"

// RuntimeConstraintViolation identifies the constraint that rejected a run.
type RuntimeConstraintViolation string

const (
	RuntimeConstraintModel        RuntimeConstraintViolation = "model"
	RuntimeConstraintFallback     RuntimeConstraintViolation = "fallback"
	RuntimeConstraintInputTokens  RuntimeConstraintViolation = "input_tokens"
	RuntimeConstraintTokenQuota   RuntimeConstraintViolation = "token_quota"
	RuntimeConstraintCostQuota    RuntimeConstraintViolation = "cost_quota"
	RuntimeConstraintInvalidInput RuntimeConstraintViolation = "invalid_input"
)

// RuntimeConstraintError carries structured details about a rejected run.
type RuntimeConstraintError struct {
	Violation            RuntimeConstraintViolation
	Model                Model
	Quota                UsageQuota
	EstimatedInputTokens int
	MaxInputTokens       int
}

func (e *RuntimeConstraintError) Error() string {
	switch e.Violation {
	case RuntimeConstraintModel:
		return fmt.Sprintf("selected model %q is outside the effective model catalog", e.Model.Name)
	case RuntimeConstraintFallback:
		return fmt.Sprintf("fallback model %q is outside the effective model catalog", e.Model.Name)
	case RuntimeConstraintInputTokens:
		return fmt.Sprintf("input is about %d tokens, exceeding the configured limit of %d", e.EstimatedInputTokens, e.MaxInputTokens)
	case RuntimeConstraintTokenQuota:
		return fmt.Sprintf("%s quota %q from layer %q is exhausted: %d tokens used of %d", e.Quota.Scope, e.Quota.Name, e.Quota.Layer, e.Quota.TokensUsed, e.Quota.TokenLimit)
	case RuntimeConstraintCostQuota:
		return fmt.Sprintf("%s quota %q from layer %q is exhausted: $%.4f used of $%.4f", e.Quota.Scope, e.Quota.Name, e.Quota.Layer, e.Quota.CostUsedUSD, e.Quota.CostLimitUSD)
	case RuntimeConstraintInvalidInput:
		return fmt.Sprintf("estimated input tokens must be non-negative, got %d", e.EstimatedInputTokens)
	default:
		return fmt.Sprintf("unknown runtime constraint violation %q", e.Violation)
	}
}

// ValidateRuntimeConstraints checks model selection, quotas, and input limits.
func ValidateRuntimeConstraints(resolved ResolvedSpec, model Model, estimatedInputTokens int) error {
	if estimatedInputTokens < 0 {
		return &RuntimeConstraintError{
			Violation: RuntimeConstraintInvalidInput, EstimatedInputTokens: estimatedInputTokens,
		}
	}
	if model.Name != "" && !resolved.AllowsModel(model) {
		return &RuntimeConstraintError{Violation: RuntimeConstraintModel, Model: model}
	}
	for _, fallback := range model.Fallbacks {
		if !resolved.AllowsModel(fallback) {
			return &RuntimeConstraintError{Violation: RuntimeConstraintFallback, Model: fallback}
		}
	}
	for _, quota := range resolved.Constraints.Quotas {
		if quota.CostLimitUSD > 0 && quota.CostUsedUSD >= quota.CostLimitUSD {
			return &RuntimeConstraintError{Violation: RuntimeConstraintCostQuota, Quota: quota}
		}
		if quota.TokenLimit > 0 && quota.TokensUsed >= quota.TokenLimit {
			return &RuntimeConstraintError{Violation: RuntimeConstraintTokenQuota, Quota: quota}
		}
	}
	maxInputTokens := resolved.Constraints.Limits.MaxInputTokens
	if maxInputTokens > 0 && estimatedInputTokens > maxInputTokens {
		return &RuntimeConstraintError{
			Violation: RuntimeConstraintInputTokens, EstimatedInputTokens: estimatedInputTokens, MaxInputTokens: maxInputTokens,
		}
	}
	return nil
}
