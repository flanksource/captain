package aichat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	Presets    []string
	PresetsSet bool
	// Ref is deprecated and ignored by providers.
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

// WithRuntimePresets selects ordered runtime presets by catalog id or name.
func WithRuntimePresets(refs []string) RuntimeProfileOption {
	return func(options *RuntimeProfileOptions) {
		options.Presets = append([]string(nil), refs...)
		if refs != nil && options.Presets == nil {
			options.Presets = []string{}
		}
		options.PresetsSet = true
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
		if len(selection.Presets) > 0 {
			return RuntimeProfile{}, RequestError(http.StatusBadRequest, fmt.Sprintf(
				"runtime presets %q cannot be selected: this deployment serves no runtime presets", strings.Join(selection.Presets, ","),
			))
		}
		return RuntimeProfile{Composed: api.ComposedSpec{Warnings: deprecatedProfileWarnings(selection.Ref)}}, nil
	}
	providerOptions := make([]RuntimeProfileOption, 0, 1)
	if selection.PresetsSet {
		providerOptions = append(providerOptions, WithRuntimePresets(selection.Presets))
	}
	profile, err := s.options.Profile.RuntimeProfile(ctx, providerOptions...)
	if err != nil {
		return RuntimeProfile{}, err
	}
	if len(profile.Composed.Trace) == 0 && profile.Saved == nil && !api.IsEmpty(profile.Composed.Spec) {
		return RuntimeProfile{}, fmt.Errorf("chat runtime profile must include its composition trace")
	}
	warnings := append([]string(nil), profile.Composed.Warnings...)
	composed, err := api.ComposeSpecLayers(api.ResolveSpecOptions{Layers: profile.Composed.Trace, Saved: profile.Saved})
	if err != nil {
		return RuntimeProfile{}, fmt.Errorf("resolve chat runtime profile: %w", err)
	}
	profile.Composed = composed
	profile.Composed.Warnings = warnings
	for _, warning := range deprecatedProfileWarnings(selection.Ref) {
		if !slices.Contains(profile.Composed.Warnings, warning) {
			profile.Composed.Warnings = append(profile.Composed.Warnings, warning)
		}
	}
	return profile, nil
}

func deprecatedProfileWarnings(ref string) []string {
	if strings.TrimSpace(ref) == "" {
		return nil
	}
	return []string{api.RuntimeProfileDeprecationWarning}
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
	options := runtimeSelectionOptions(w, request)
	if _, err := s.runtimeProfile(request.Context(), options...); err != nil {
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
	options := runtimeSelectionOptions(w, request)
	if _, err := s.runtimeProfile(request.Context(), options...); err != nil {
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

func runtimeSelectionOptions(w http.ResponseWriter, request *http.Request) []RuntimeProfileOption {
	query := request.URL.Query()
	options := make([]RuntimeProfileOption, 0, 2)
	if refs, present := query["preset"]; present {
		options = append(options, WithRuntimePresets(refs))
	}
	if ref := strings.TrimSpace(query.Get("runtimeProfile")); ref != "" {
		serviceLog.Warnf("%s", api.RuntimeProfileDeprecationWarning)
		w.Header().Set("Warning", `299 Captain "`+api.RuntimeProfileDeprecationWarning+`"`)
		options = append(options, WithRuntimeProfileRef(ref))
	}
	return options
}
