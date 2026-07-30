package cli

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// disabledSelectionsRequest is the full opt-out set. The endpoint is idempotent
// on purpose: the whoami page flips many switches in quick succession, and a
// whole-set PUT can never leave the file in a half-applied state the way a
// per-entry add/remove pair could.
type disabledSelectionsRequest struct {
	Modes     []string `json:"modes"`
	Providers []string `json:"providers"`
	Backends  []string `json:"backends"`
	Models    []string `json:"models"`
	Efforts   []string `json:"efforts"`
}

func registerDisabledHandlers(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/captain/ai/disabled", handleDisabledSelections)
}

// InstallDisabledSelections loads ~/.captain.yaml and installs its opt-out set
// process-wide. The root command calls it once before any subcommand runs, so a
// CLI invocation honours the same disables the whoami page writes. Loading the
// config is deliberately side-effect free, which is why this is explicit.
func InstallDisabledSelections() error {
	config, _, err := captainconfig.Load()
	if err != nil {
		return err
	}
	config.ApplyToRegistry()
	return nil
}

func handleDisabledSelections(w http.ResponseWriter, r *http.Request) {
	if err := validateLocalConfigurationRequest(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var request disabledSelectionsRequest
	if err := decodeConfigurationRequest(w, r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	selections := captainconfig.DisabledSelections{
		Modes:     normalizeTokens(request.Modes, true),
		Providers: normalizeTokens(request.Providers, true),
		Backends:  normalizeTokens(request.Backends, true),
		Models:    normalizeTokens(request.Models, false),
		Efforts:   normalizeTokens(request.Efforts, true),
	}
	saved, _, err := captainconfig.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := validateDisabledSelections(saved.AI, selections); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		cfg.AI.Disabled = selections
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The registry global is what every resolution path reads, so install the new
	// set before answering: the page refetches immediately after this call.
	api.SetDisabled(selections.Set())
	writeConfigurationJSON(w, disabledSelectionsRequest(selections))
}

// normalizeTokens trims, drops blanks and de-duplicates case-insensitively.
// Enum axes are canonicalized to lower case; model ids keep the case they were
// written in, since they are only ever displayed back and matched insensitively.
func normalizeTokens(values []string, lower bool) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		token := strings.TrimSpace(value)
		if token == "" {
			continue
		}
		key := strings.ToLower(token)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if lower {
			token = key
		}
		out = append(out, token)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateDisabledSelections rejects tokens that name nothing, and sets that
// would leave captain with nothing to run. Model ids are deliberately not
// checked against the catalog: the catalog is probe-dependent, and an entry for
// a model that is currently unreachable is a legitimate standing preference.
func validateDisabledSelections(saved captainconfig.AIDefaults, selections captainconfig.DisabledSelections) error {
	for _, mode := range selections.Modes {
		if _, ok := api.ParseRuntimeMode(mode); !ok {
			return fmt.Errorf("unknown runtime mode %q; expected one of: %s", mode, api.RuntimeModeList())
		}
	}
	for _, provider := range selections.Providers {
		if !configurableAPIBackend(api.Backend(provider)) {
			return fmt.Errorf("unknown provider %q; expected one of: anthropic, openai, gemini, deepseek", provider)
		}
	}
	for _, backend := range selections.Backends {
		if !knownBackend(api.Backend(backend)) {
			return fmt.Errorf("unknown backend %q; expected one of: %s", backend, api.BackendList())
		}
	}
	for _, effort := range selections.Efforts {
		if err := api.Effort(effort).Validate(); err != nil {
			return err
		}
	}
	set := selections.Set()
	if len(set.EnabledEfforts()) == 0 {
		return fmt.Errorf("cannot disable every reasoning effort; leave at least one enabled")
	}
	if len(selections.Providers) >= len(configurableProviders()) {
		return fmt.Errorf("cannot disable every provider; leave at least one enabled")
	}
	// A flagless run resolves through ActiveProvider, so it is not enough that
	// some backend somewhere survives: the provider that run would land on has to
	// keep one. Disabling all four anthropic backends individually leaves
	// ActiveProvider on anthropic with nothing to serve it.
	saved.Disabled = selections
	active := api.Backend(saved.ActiveProvider())
	for _, backend := range api.AllBackends() {
		if backend.Provider() == active && !set.Backend(backend) {
			return nil
		}
	}
	return fmt.Errorf("this would leave the active provider %q with no enabled backend; re-enable a mode or backend for it, or disable the provider outright so another one takes over", active)
}

func knownBackend(backend api.Backend) bool {
	for _, candidate := range api.AllBackends() {
		if candidate == backend {
			return true
		}
	}
	return false
}
