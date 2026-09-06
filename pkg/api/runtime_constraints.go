package api

import (
	"fmt"
	"strings"
)

// RuntimeConstraintViolation identifies the constraint that rejected a run.
type RuntimeConstraintViolation string

const (
	RuntimeConstraintModel        RuntimeConstraintViolation = "model"
	RuntimeConstraintFallback     RuntimeConstraintViolation = "fallback"
	RuntimeConstraintInputTokens  RuntimeConstraintViolation = "input_tokens"
	RuntimeConstraintTokenQuota   RuntimeConstraintViolation = "token_quota"
	RuntimeConstraintCostQuota    RuntimeConstraintViolation = "cost_quota"
	RuntimeConstraintPermission   RuntimeConstraintViolation = "permission"
	RuntimeConstraintInvalidInput RuntimeConstraintViolation = "invalid_input"
)

// RuntimeConstraintError carries structured details about a rejected run.
type RuntimeConstraintError struct {
	Violation            RuntimeConstraintViolation
	Model                Model
	Quota                UsageQuota
	EstimatedInputTokens int
	MaxInputTokens       int
	Field                string
	Actual               string
	Constraint           string
	ActualLayer          string
	ConstraintLayer      string
	ConstraintSource     SpecLayerSource
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
	case RuntimeConstraintPermission:
		actual := e.Actual
		if actual == "" {
			actual = "<unset>"
		}
		if e.ActualLayer != "" && e.ConstraintLayer != "" {
			if e.ConstraintSource != "" {
				return fmt.Sprintf("spec layer %q %s %q exceeds constraint %q from %s layer %q", e.ActualLayer, e.Field, actual, e.Constraint, e.ConstraintSource, e.ConstraintLayer)
			}
			return fmt.Sprintf("spec layer %q %s %q exceeds constraint %q from layer %q", e.ActualLayer, e.Field, actual, e.Constraint, e.ConstraintLayer)
		}
		return fmt.Sprintf("%s %q exceeds effective permission constraint %q", e.Field, actual, e.Constraint)
	case RuntimeConstraintInvalidInput:
		return fmt.Sprintf("estimated input tokens must be non-negative, got %d", e.EstimatedInputTokens)
	default:
		return fmt.Sprintf("unknown runtime constraint violation %q", e.Violation)
	}
}

// ValidateRuntimeConstraints checks the actual run against model, budget, quota
// and input ceilings. It never clamps a copy while leaving execution unbounded.
func ValidateRuntimeConstraints(resolved ResolvedSpec, model Model, estimatedInputTokens int) error {
	if err := resolved.Constraints.Validate(); err != nil {
		return err
	}
	if err := validatePermissionConstraints(resolved.Spec, resolved.Constraints.Permissions, resolved.Trace); err != nil {
		return err
	}
	if err := validateBudgetLimits(resolved.Spec.Budget, resolved.Constraints.Limits.Budget); err != nil {
		return err
	}
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

// Validate checks effective constraints without resolving models or merging layers.
func (constraints RuntimeConstraints) Validate() error {
	if _, err := strictRunLimits(RunLimits{}, constraints.Limits); err != nil {
		return fmt.Errorf("runtime constraints: %w", err)
	}
	if err := constraints.Permissions.Validate(); err != nil {
		return fmt.Errorf("runtime constraints: %w", err)
	}
	for _, selector := range constraints.Models {
		if strings.TrimSpace(selector) == "" {
			return fmt.Errorf("runtime constraints model catalog contains an empty selector")
		}
	}
	for _, quota := range constraints.Quotas {
		if strings.TrimSpace(quota.Name) == "" {
			return fmt.Errorf("runtime constraints quota name is required")
		}
		if quota.Scope != SpecLayerGlobal && quota.Scope != SpecLayerContext {
			return fmt.Errorf("runtime constraints quota %q requires global or context scope", quota.Name)
		}
		if quota.TokenLimit < 0 || quota.TokensUsed < 0 || quota.CostLimitUSD < 0 || quota.CostUsedUSD < 0 {
			return fmt.Errorf("runtime constraints quota %q cannot contain negative usage or limits", quota.Name)
		}
	}
	return nil
}

func validateBudgetLimits(actual, limit Budget) error {
	if err := actual.Validate(); err != nil {
		return err
	}
	for _, ceiling := range []struct {
		name          string
		actual, limit float64
	}{
		{"cost", actual.Cost, limit.Cost},
		{"maxTokens", float64(actual.MaxTokens), float64(limit.MaxTokens)},
		{"maxTurns", float64(actual.MaxTurns), float64(limit.MaxTurns)},
	} {
		if ceiling.limit > 0 && (ceiling.actual == 0 || ceiling.actual > ceiling.limit) {
			return fmt.Errorf("budget.%s %v exceeds the effective limit %v (zero is unbounded)", ceiling.name, ceiling.actual, ceiling.limit)
		}
	}
	actualTimeout, err := actual.ParseTimeout()
	if err != nil {
		return err
	}
	limitTimeout, err := limit.ParseTimeout()
	if err != nil {
		return err
	}
	if limitTimeout > 0 && (actualTimeout == 0 || actualTimeout > limitTimeout) {
		return fmt.Errorf("budget.timeout %s exceeds the effective limit %s", actualTimeout, limitTimeout)
	}
	return nil
}
