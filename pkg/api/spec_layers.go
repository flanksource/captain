package api

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// SpecLayerScope identifies one deterministic level in a resolved runtime profile.
type SpecLayerScope string

const (
	SpecLayerGlobal  SpecLayerScope = "global"
	SpecLayerContext SpecLayerScope = "context"
	SpecLayerSurface SpecLayerScope = "surface"
	SpecLayerUser    SpecLayerScope = "user"
)

// SpecLayerSource identifies where a trace row came from: a reusable preset,
// the task-specific profile spec, a .prompt document's frontmatter, or the
// caller's request.
type SpecLayerSource string

const (
	SpecLayerSourcePreset  SpecLayerSource = "preset"
	SpecLayerSourceProfile SpecLayerSource = "profile"
	SpecLayerSourcePrompt  SpecLayerSource = "prompt"
	SpecLayerSourceRequest SpecLayerSource = "request"
)

// RunLimits are per-run ceilings applied after structural Spec defaults are layered.
type RunLimits struct {
	MaxInputTokens int    `json:"maxInputTokens,omitempty" yaml:"maxInputTokens,omitempty"`
	Budget         Budget `json:"budget,omitempty" yaml:"budget,omitempty"`
}

// UsageQuota is one independently enforced accumulated usage allowance.
type UsageQuota struct {
	Name         string         `json:"name" yaml:"name"`
	Scope        SpecLayerScope `json:"scope" yaml:"scope"`
	Layer        string         `json:"layer" yaml:"layer"`
	TokenLimit   int            `json:"tokenLimit,omitempty" yaml:"tokenLimit,omitempty"`
	TokensUsed   int            `json:"tokensUsed,omitempty" yaml:"tokensUsed,omitempty"`
	CostLimitUSD float64        `json:"costLimitUsd,omitempty" yaml:"costLimitUsd,omitempty"`
	CostUsedUSD  float64        `json:"costUsedUsd,omitempty" yaml:"costUsedUsd,omitempty"`
}

// RuntimeConstraints restrict values a later Spec layer may select.
type RuntimeConstraints struct {
	Models      []string              `json:"models,omitempty" yaml:"models,omitempty"`
	Limits      RunLimits             `json:"limits,omitempty" yaml:"limits,omitempty"`
	Quotas      []UsageQuota          `json:"quotas,omitempty" yaml:"quotas,omitempty"`
	Permissions PermissionConstraints `json:"permissions,omitempty" yaml:"permissions,omitempty"`
}

// SpecLayer is one named source of runtime defaults and constraints.
type SpecLayer struct {
	ID          string             `json:"id,omitempty" yaml:"id,omitempty"`
	Source      SpecLayerSource    `json:"source,omitempty" yaml:"source,omitempty"`
	Name        string             `json:"name" yaml:"name"`
	Scope       SpecLayerScope     `json:"scope" yaml:"scope"`
	Spec        Spec               `json:"spec,omitempty" yaml:"spec,omitempty"`
	Constraints RuntimeConstraints `json:"constraints,omitempty" yaml:"constraints,omitempty"`
}

// ResolvedSpec is Captain's effective runtime profile plus ordered provenance.
type ResolvedSpec struct {
	Spec        Spec                       `json:"spec" yaml:"spec"`
	Constraints RuntimeConstraints         `json:"constraints" yaml:"constraints"`
	Trace       []SpecLayer                `json:"trace" yaml:"trace"`
	Warnings    []string                   `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Provenance  map[string]FieldProvenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

// PromptSpecLayer adapts parsed .prompt frontmatter into the normal surface layer.
func PromptSpecLayer(name string, spec Spec) SpecLayer {
	return SpecLayer{Name: name, Source: SpecLayerSourcePrompt, Scope: SpecLayerSurface, Spec: spec}
}

// RequestSpecLayer adapts a caller's per-run overrides into the user layer, the
// last layer resolved so it wins over every authored default.
func RequestSpecLayer(name string, spec Spec) SpecLayer {
	return SpecLayer{Name: name, Source: SpecLayerSourceRequest, Scope: SpecLayerUser, Spec: spec}
}

// OrderSpecLayers copies the stack into effective scope order, preserving ties.
func OrderSpecLayers(input ...SpecLayer) []SpecLayer {
	layers := append([]SpecLayer(nil), input...)
	slices.SortStableFunc(layers, func(left, right SpecLayer) int {
		return scopeRank(left.Scope) - scopeRank(right.Scope)
	})
	return layers
}

// ComposeSpecLayers overlays raw defaults and constraints without resolving a runtime.
func ComposeSpecLayers(options ResolveSpecOptions) (ComposedSpec, error) {
	if err := ValidateSpecLayers(options.Layers...); err != nil {
		return ComposedSpec{}, err
	}
	layers := OrderSpecLayers(options.Layers...)
	limitSources := budgetLimitSources{}
	resolved := ComposedSpec{Trace: make([]SpecLayer, 0, len(layers)), Provenance: map[string]FieldProvenance{}}
	for _, layer := range layers {
		resolved.recordLayer(layer)
		resolved.Spec = resolved.Spec.Merge(layer.Spec)
		if len(layer.Constraints.Models) > 0 {
			resolved.Constraints.Models = intersectModels(resolved.Constraints.Models, layer.Constraints.Models)
			if len(resolved.Constraints.Models) == 0 {
				return ComposedSpec{}, fmt.Errorf("spec layer %q leaves the effective model catalog empty", layer.Name)
			}
		}
		limits, err := strictRunLimits(resolved.Constraints.Limits, layer.Constraints.Limits)
		if err != nil {
			return ComposedSpec{}, fmt.Errorf("spec layer %q limits: %w", layer.Name, err)
		}
		resolved.Constraints.Limits = limits
		permissions, err := strictPermissionConstraints(resolved.Constraints.Permissions, layer.Constraints.Permissions)
		if err != nil {
			return ComposedSpec{}, fmt.Errorf("spec layer %q permission constraints: %w", layer.Name, err)
		}
		resolved.Constraints.Permissions = permissions
		limitSources.record(layer, limits.Budget)
		for _, quota := range layer.Constraints.Quotas {
			quota.Name = strings.TrimSpace(quota.Name)
			quota.Scope = layer.Scope
			quota.Layer = layer.Name
			resolved.Constraints.Quotas = append(resolved.Constraints.Quotas, quota)
		}
		resolved.Trace = append(resolved.Trace, cloneSpecLayer(layer))
	}

	if options.Saved != nil || options.RequireModel || options.Normalize != nil {
		if err := resolved.expandModel(); err != nil {
			return ComposedSpec{}, err
		}
	}
	if err := resolved.applyDefaults(options); err != nil {
		return ComposedSpec{}, err
	}
	budget, err := strictBudget(resolved.Spec.Budget, resolved.Constraints.Limits.Budget)
	if err != nil {
		return ComposedSpec{}, fmt.Errorf("effective run budget: %w", err)
	}
	resolved.Spec.Budget = budget
	resolved.recordLimits(limitSources)
	if err := validatePermissionConstraints(resolved.Spec, resolved.Constraints.Permissions, resolved.Trace); err != nil {
		return ComposedSpec{}, err
	}
	return resolved, nil
}

// AllowsModel reports whether a model belongs to the effective restrictive catalog.
func (resolved ResolvedSpec) AllowsModel(model Model) bool {
	return allowsModel(resolved.Constraints.Models, model)
}

func allowsModel(models []string, model Model) bool {
	if len(models) == 0 {
		return true
	}
	for _, allowed := range models {
		if modelSelectorMatches(allowed, model) {
			return true
		}
	}
	return false
}

func validateSpecLayer(layer SpecLayer) error {
	if strings.TrimSpace(layer.Name) == "" {
		return fmt.Errorf("spec layer name is required")
	}
	if scopeRank(layer.Scope) < 0 {
		return fmt.Errorf("spec layer %q has invalid scope %q", layer.Name, layer.Scope)
	}
	if _, err := strictRunLimits(RunLimits{}, layer.Constraints.Limits); err != nil {
		return fmt.Errorf("spec layer %q limits: %w", layer.Name, err)
	}
	if err := layer.Constraints.Permissions.Validate(); err != nil {
		return fmt.Errorf("spec layer %q permission constraints: %w", layer.Name, err)
	}
	seenModels := map[string]bool{}
	for _, model := range layer.Constraints.Models {
		model = strings.TrimSpace(model)
		if model == "" {
			return fmt.Errorf("spec layer %q model catalog contains an empty selector", layer.Name)
		}
		if seenModels[model] {
			return fmt.Errorf("spec layer %q model catalog repeats %q", layer.Name, model)
		}
		seenModels[model] = true
	}
	seenQuotas := map[string]bool{}
	for _, quota := range layer.Constraints.Quotas {
		name := strings.TrimSpace(quota.Name)
		if name == "" {
			return fmt.Errorf("spec layer %q quota name is required", layer.Name)
		}
		if layer.Scope != SpecLayerGlobal && layer.Scope != SpecLayerContext {
			return fmt.Errorf("spec layer %q quota %q requires global or context scope", layer.Name, name)
		}
		if seenQuotas[name] {
			return fmt.Errorf("spec layer %q repeats quota %q", layer.Name, name)
		}
		seenQuotas[name] = true
		if quota.TokenLimit < 0 || quota.TokensUsed < 0 || quota.CostLimitUSD < 0 || quota.CostUsedUSD < 0 {
			return fmt.Errorf("spec layer %q quota %q cannot contain negative usage or limits", layer.Name, name)
		}
	}
	return nil
}

func scopeRank(scope SpecLayerScope) int {
	switch scope {
	case SpecLayerGlobal:
		return 0
	case SpecLayerContext:
		return 1
	case SpecLayerSurface:
		return 2
	case SpecLayerUser:
		return 3
	default:
		return -1
	}
}

func intersectModels(current, restrictive []string) []string {
	if len(current) == 0 {
		out := make([]string, 0, len(restrictive))
		for _, model := range restrictive {
			out = append(out, strings.TrimSpace(model))
		}
		return out
	}
	allowed := make(map[string]bool, len(restrictive))
	for _, model := range restrictive {
		allowed[strings.TrimSpace(model)] = true
	}
	out := make([]string, 0, len(current))
	for _, model := range current {
		model = strings.TrimSpace(model)
		if allowed[model] {
			out = append(out, model)
		}
	}
	return out
}

func strictRunLimits(current, next RunLimits) (RunLimits, error) {
	if current.MaxInputTokens < 0 || next.MaxInputTokens < 0 {
		return RunLimits{}, fmt.Errorf("maxInputTokens must be non-negative")
	}
	budget, err := strictBudget(current.Budget, next.Budget)
	if err != nil {
		return RunLimits{}, err
	}
	return RunLimits{
		MaxInputTokens: strictPositiveInt(current.MaxInputTokens, next.MaxInputTokens),
		Budget:         budget,
	}, nil
}

func strictBudget(current, next Budget) (Budget, error) {
	if err := current.Validate(); err != nil {
		return Budget{}, err
	}
	if err := next.Validate(); err != nil {
		return Budget{}, err
	}
	timeout, err := strictTimeout(current.Timeout, next.Timeout)
	if err != nil {
		return Budget{}, err
	}
	return Budget{
		Cost:      strictPositiveFloat(current.Cost, next.Cost),
		MaxTokens: strictPositiveInt(current.MaxTokens, next.MaxTokens),
		MaxTurns:  strictPositiveInt(current.MaxTurns, next.MaxTurns),
		Timeout:   timeout,
	}, nil
}

func strictPositiveInt(left, right int) int {
	if left == 0 || right > 0 && right < left {
		return right
	}
	return left
}

func strictPositiveFloat(left, right float64) float64 {
	if left == 0 || right > 0 && right < left {
		return right
	}
	return left
}

func strictTimeout(left, right string) (string, error) {
	leftDuration, err := parseOptionalDuration(left)
	if err != nil {
		return "", err
	}
	rightDuration, err := parseOptionalDuration(right)
	if err != nil {
		return "", err
	}
	if leftDuration == 0 || rightDuration > 0 && rightDuration < leftDuration {
		return strings.TrimSpace(right), nil
	}
	return strings.TrimSpace(left), nil
}

func parseOptionalDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid positive timeout %q", value)
	}
	return duration, nil
}

func validateResolvedModels(resolved ResolvedSpec) error {
	model := resolved.Spec.Model
	if strings.TrimSpace(model.Name) == "" {
		return nil
	}
	if !resolved.AllowsModel(model) {
		return fmt.Errorf("selected model %q is outside the effective model catalog", model.Name)
	}
	for _, fallback := range model.Fallbacks {
		if !resolved.AllowsModel(fallback) {
			return fmt.Errorf("fallback model %q is outside the effective model catalog", fallback.Name)
		}
	}
	return nil
}

func modelSelectorMatches(selector string, model Model) bool {
	selector = strings.TrimSpace(selector)
	if selector == model.Name || selector == model.ID {
		return true
	}
	actual, actualErr := ResolveModel(model)
	if actualErr != nil {
		return false
	}
	allowed, allowedErr := ResolveModel(Model{Name: selector, Mode: actual.Mode})
	return allowedErr == nil && allowed.Name == actual.Name && allowed.Mode == actual.Mode && allowed.Provider == actual.Provider
}

func cloneSpecLayer(layer SpecLayer) SpecLayer {
	layer.Spec = Spec{}.Merge(layer.Spec)
	layer.Constraints.Models = append([]string(nil), layer.Constraints.Models...)
	layer.Constraints.Quotas = append([]UsageQuota(nil), layer.Constraints.Quotas...)
	layer.Constraints.Permissions = layer.Constraints.Permissions.clone()
	return layer
}
