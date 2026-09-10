package aichat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// RuntimeProfile is the request-scoped application configuration for a chat.
// Composed carries structurally validated defaults and raw layers; runtime
// capability validation waits for the complete chat request.
type RuntimeProfile struct {
	System         string
	Composed       api.ComposedSpec
	Saved          *captainconfig.AIDefaults
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
	if len(profile.Composed.Trace) == 0 && profile.Saved == nil && !api.IsEmpty(profile.Composed.Spec) {
		return RuntimeProfile{}, fmt.Errorf("chat runtime profile must include its composition trace")
	}
	composed, err := api.ComposeSpecLayers(api.ResolveSpecOptions{Layers: profile.Composed.Trace, Saved: profile.Saved})
	if err != nil {
		return RuntimeProfile{}, fmt.Errorf("resolve chat runtime profile: %w", err)
	}
	profile.Composed = composed
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

func (s *Service) handleRuntimes(w http.ResponseWriter, request *http.Request) {
	// The profile is loaded for its validation: a selection this deployment
	// cannot serve is the caller's error, answered before any catalog is read.
	if _, err := s.runtimeProfile(request.Context(), WithRuntimeProfileRef(request.URL.Query().Get("runtimeProfile"))); err != nil {
		http.Error(w, fmt.Sprintf("load chat runtime profile: %v", err), runtimeProfileStatus(err))
		return
	}
	runtimes, err := s.resolver.Runtimes(request.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := s.annotateConfiguredRuntimes(request.Context(), runtimes); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := writeJSON(w, http.StatusOK, runtimes); err != nil {
		serviceLog.Errorf("write chat runtimes response: %v", err)
	}
}

func (s *Service) handleModels(w http.ResponseWriter, request *http.Request) {
	if _, err := s.runtimeProfile(request.Context(), WithRuntimeProfileRef(request.URL.Query().Get("runtimeProfile"))); err != nil {
		http.Error(w, fmt.Sprintf("load chat runtime profile: %v", err), runtimeProfileStatus(err))
		return
	}
	models, err := s.resolver.Models(request.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := s.annotateConfiguredModels(request.Context(), models); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := writeJSON(w, http.StatusOK, models); err != nil {
		serviceLog.Errorf("write chat models response: %v", err)
	}
}
