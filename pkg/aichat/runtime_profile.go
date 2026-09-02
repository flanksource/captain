package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

// RuntimeProfile is the request-scoped, hierarchically resolved application
// configuration for a chat. Resolved carries the effective Spec, constraints,
// and ordered provenance; provider credentials remain runtime-only.
type RuntimeProfile struct {
	System         string
	Resolved       api.ResolvedSpec
	ProviderConfig api.Config
}

// RuntimeProfileOptions contains request-scoped runtime profile selections.
type RuntimeProfileOptions struct {
	Ref string
}

// RuntimeProfileOption applies one request-scoped profile selection.
type RuntimeProfileOption func(*RuntimeProfileOptions)

// WithRuntimeProfileRef selects a runtime profile by catalog id or name.
func WithRuntimeProfileRef(ref string) RuntimeProfileOption {
	return func(options *RuntimeProfileOptions) {
		options.Ref = strings.TrimSpace(ref)
	}
}

// ApplyRuntimeProfileOptions applies options in order and ignores nil options.
func ApplyRuntimeProfileOptions(options ...RuntimeProfileOption) RuntimeProfileOptions {
	var applied RuntimeProfileOptions
	for _, option := range options {
		if option != nil {
			option(&applied)
		}
	}
	return applied
}

// RuntimeProfileProvider supplies request-scoped application profiles. It
// rejects a selection it cannot honour with RequestError; every other failure
// is reported as an internal error.
type RuntimeProfileProvider interface {
	RuntimeProfile(context.Context, ...RuntimeProfileOption) (RuntimeProfile, error)
}

type RuntimeProfileProviderFunc func(context.Context, ...RuntimeProfileOption) (RuntimeProfile, error)

func (f RuntimeProfileProviderFunc) RuntimeProfile(ctx context.Context, options ...RuntimeProfileOption) (RuntimeProfile, error) {
	return f(ctx, options...)
}

// runtimeProfile loads the selected server-owned profile and validates it before
// request fields are layered onto it, so profile defects remain server errors at
// the HTTP boundary. A selection the deployment cannot serve is the caller's
// error rather than something to ignore.
func (s *Service) runtimeProfile(ctx context.Context, options ...RuntimeProfileOption) (RuntimeProfile, error) {
	selection := ApplyRuntimeProfileOptions(options...)
	if s.options.Profile == nil {
		if selection.Ref != "" {
			return RuntimeProfile{}, RequestError(http.StatusBadRequest, fmt.Sprintf(
				"runtime profile %q cannot be selected: this deployment serves no runtime profiles", selection.Ref,
			))
		}
		return RuntimeProfile{}, nil
	}
	profile, err := s.options.Profile.RuntimeProfile(ctx, options...)
	if err != nil {
		return RuntimeProfile{}, err
	}
	if len(profile.Resolved.Trace) == 0 {
		if !api.IsEmpty(profile.Resolved.Spec) || !api.IsEmpty(profile.Resolved.Constraints) {
			return RuntimeProfile{}, fmt.Errorf("chat runtime profile must include its resolution trace")
		}
		return profile, nil
	}
	resolved, err := api.ResolveSpecLayers(profile.Resolved.Trace...)
	if err != nil {
		return RuntimeProfile{}, fmt.Errorf("resolve chat runtime profile: %w", err)
	}
	profile.Resolved = resolved
	return profile, nil
}

type requestError struct {
	status int
	text   string
}

func (e requestError) Error() string { return e.text }

// RequestError carries the HTTP status a chat handler answers with. A
// RuntimeProfileProvider returns it to reject a caller's selection as a client
// error; any other error it returns stays an internal error.
func RequestError(status int, message string) error {
	return requestError{status: status, text: message}
}

func runtimeProfileStatus(err error) int {
	var typed requestError
	if errors.As(err, &typed) {
		return typed.status
	}
	return http.StatusInternalServerError
}

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
