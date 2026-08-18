// HTTP surface for the agent-login sync — the mirror that lets a deployed agent
// reach a model provider with this host's own claude/codex logins.
//
// A sidecar with no credential enrolls, goes ready, and fails its first task, so
// the thing an operator most needs is to see what is published, where, and how
// long it stays valid. The CLI has had `sandbox credentials status|sync` for
// that; this exposes the same two calls plus the destinations they read from.
//
// `credentials sync` is clicky.MarkLocalOnly for a reason (cmd/captain/main.go):
// it reads this host's keychain and writes a credential into a directory or a
// cluster, which must never be reachable as unauthenticated REST under /api/v1.
// Nothing here weakens that — these are hand-written routes behind
// validateLocalConfigurationRequest, the same loopback-and-same-origin gate the
// deploy routes use, and they stay out of the auto-published executor surface.

package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/agentcreds"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/credsync"
)

func registerSandboxCredentialHandlers(mux *http.ServeMux) {
	mux.Handle("GET /api/captain/sandbox/credentials", handleCredentialsStatus())
	mux.Handle("PUT /api/captain/sandbox/credentials/config", handleCredentialsConfig())
	mux.Handle("POST /api/captain/sandbox/credentials/sync", handleCredentialsSync())
}

// credentialsView is the whole panel in one response: what is configured, and
// what each provider's login currently looks like.
type credentialsView struct {
	Config credentialsConfig  `json:"config"`
	Status []CredentialStatus `json:"status"`
	// Providers are the logins captain knows how to mirror, so the destination
	// editor offers them rather than asking an operator to spell them.
	Providers []string `json:"providers"`
	// DefaultSecret is what an unnamed Kubernetes destination resolves to.
	DefaultSecret string `json:"defaultSecret"`
	// DefaultMargin is what an unset refresh margin resolves to.
	DefaultMargin string `json:"defaultMargin"`
}

// credentialsConfig is the `credentials:` block of ~/.captain.yaml on the wire.
//
// It is a separate shape from captainconfig.CredentialDefaults rather than json
// tags on it, because that package stays yaml-only and a duration crossing JSON
// has to be "1h" — the form an operator writes in the file and passes to
// --refresh-margin — not the nanosecond count time.Duration marshals to.
type credentialsConfig struct {
	RefreshMargin string                  `json:"refreshMargin"`
	Publish       []credentialDestination `json:"publish"`
}

type credentialDestination struct {
	Providers []string `json:"providers,omitempty"`
	Directory string   `json:"directory,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Secret    string   `json:"secret,omitempty"`
	Context   string   `json:"kubeContext,omitempty"`
}

// isKubernetes distinguishes the two destination kinds the UI presents as one
// row. A directory and a Secret are mutually exclusive per entry, which
// CredentialPublish.Validate enforces on the way back in.
func (d credentialDestination) isKubernetes() bool {
	return strings.TrimSpace(d.Directory) == ""
}

func handleCredentialsStatus() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read-only, but still gated: the response names this host's directories
		// and the clusters it publishes into.
		if err := validateLocalConfigurationRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		saved, _, err := captainconfig.Load()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), preflightTimeout)
		defer cancel()
		// A provider that cannot be read is a row with a reason, not a failed
		// request: "codex: not logged in" beside a healthy claude row is the
		// whole point of the panel.
		status, err := RunCredentialsStatus(ctx, CredentialsOptions{})
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadGateway))
			return
		}
		writeServeJSON(w, http.StatusOK, credentialsView{
			Config:        credentialsConfigFrom(saved.Credentials),
			Status:        status,
			Providers:     supportedCredentialProviders(),
			DefaultSecret: credsync.DefaultSecretName,
			DefaultMargin: credsync.DefaultMargin.String(),
		})
	})
}

// handleCredentialsConfig replaces the `credentials:` block.
//
// A whole-block PUT rather than a patch: Publish is a list whose entries have no
// stable identity, so a partial update would have to invent one and would make
// "remove the last destination" indistinguishable from "send nothing".
func handleCredentialsConfig() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateLocalConfigurationRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		var request credentialsConfig
		if err := decodeServeJSONBody(w, r, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defaults, err := credentialDefaultsFrom(request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Validated before the write, so a destination naming nowhere is refused
		// here rather than silently publishing nothing on the next tick.
		if err := defaults.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved, _, err := captainconfig.Load()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		saved.Credentials = defaults
		if err := captainconfig.Save(saved); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Echoed back through the same conversion the GET uses, so the form
		// renders what was actually stored rather than what it sent.
		writeServeJSON(w, http.StatusOK, credentialsConfigFrom(saved.Credentials))
	})
}

// handleCredentialsSync publishes once, now.
//
// The saved destinations are used unless the body names one, which mirrors the
// CLI: a destination can be tried once without editing the file first.
func handleCredentialsSync() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateLocalConfigurationRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		var request credentialDestination
		if err := decodeServeJSONBody(w, r, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, err := RunCredentialsSync(r.Context(), CredentialsOptions{
			Providers: request.Providers,
			Directory: strings.TrimSpace(request.Directory),
			Namespace: strings.TrimSpace(request.Namespace),
			Secret:    strings.TrimSpace(request.Secret),
			Context:   strings.TrimSpace(request.Context),
		})
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadGateway))
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	})
}

// supportedCredentialProviders names the logins captain can mirror, so the
// destination editor offers them instead of accepting a typo that resolves to
// "publish nothing".
func supportedCredentialProviders() []string {
	providers := agentcreds.Providers()
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		names = append(names, string(provider))
	}
	return names
}

// credentialsConfigFrom flattens the stored block onto the wire shape.
func credentialsConfigFrom(defaults captainconfig.CredentialDefaults) credentialsConfig {
	view := credentialsConfig{Publish: make([]credentialDestination, 0, len(defaults.Publish))}
	if defaults.RefreshMargin > 0 {
		view.RefreshMargin = defaults.RefreshMargin.String()
	}
	for _, publish := range defaults.Publish {
		destination := credentialDestination{
			Providers: publish.Providers,
			Directory: publish.Directory,
		}
		if publish.Kubernetes != nil {
			destination.Namespace = publish.Kubernetes.Namespace
			destination.Secret = publish.Kubernetes.Secret
			destination.Context = publish.Kubernetes.Context
		}
		view.Publish = append(view.Publish, destination)
	}
	return view
}

// credentialDefaultsFrom is the inverse, and the only place a submitted
// duration is parsed.
func credentialDefaultsFrom(request credentialsConfig) (captainconfig.CredentialDefaults, error) {
	defaults := captainconfig.CredentialDefaults{
		Publish: make([]captainconfig.CredentialPublish, 0, len(request.Publish)),
	}
	if margin := strings.TrimSpace(request.RefreshMargin); margin != "" {
		parsed, err := time.ParseDuration(margin)
		if err != nil {
			return defaults, fmt.Errorf(
				"refresh margin %q is not a duration such as 1h or 30m", request.RefreshMargin)
		}
		if parsed < 0 {
			return defaults, fmt.Errorf("refresh margin must not be negative, got %q", request.RefreshMargin)
		}
		defaults.RefreshMargin = parsed
	}
	for _, destination := range request.Publish {
		publish := captainconfig.CredentialPublish{Providers: destination.Providers}
		if destination.isKubernetes() {
			publish.Kubernetes = &captainconfig.CredentialSecretRef{
				Context:   strings.TrimSpace(destination.Context),
				Namespace: strings.TrimSpace(destination.Namespace),
				Secret:    strings.TrimSpace(destination.Secret),
			}
		} else {
			publish.Directory = strings.TrimSpace(destination.Directory)
		}
		defaults.Publish = append(defaults.Publish, publish)
	}
	return defaults, nil
}
