// Token authentication for `captain serve`.
//
// Every route on this server used to be unauthenticated, and the only thing
// standing in front of /api/v1 — which executes arbitrary captain commands —
// was that the listener bound localhost. Hosting a git endpoint here means the
// server has to be reachable off-box, so that protection is gone and something
// has to replace it.

package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/captain/pkg/gitagent"
)

// gitPathPrefix is the subtree the git smart-HTTP transport is served
// under. Taken from the transport rather than restated, so the routing, the
// auth scope and the database-context exemption cannot drift apart.
const gitPathPrefix = gitagent.GitHTTPPrefix

// tokenContextKey carries the verified credential to the handler.
type tokenContextKey struct{}

// TokenFromContext returns the credential a request authenticated with. ok is
// false for a loopback request, which carries none — a handler that needs an
// identity must say so rather than assume one.
func TokenFromContext(ctx context.Context) (captaintoken.Record, bool) {
	record, ok := ctx.Value(tokenContextKey{}).(captaintoken.Record)
	return record, ok
}

// TokenAuthConfig is what the middleware needs from storage.
type TokenAuthConfig struct {
	Verifier *captaintoken.Verifier
	// Touch records that a token was used. It runs only after a credential
	// verifies, and its failure never fails the request: bookkeeping must not
	// turn a working push into a 500.
	Touch func(ctx context.Context, tokenID string) error
}

// TokenAuthMiddleware requires a captain token for requests that arrive from
// off this machine.
//
// Loopback is exempt, so the local webapp, CLI and hook subprocesses are
// untouched. That is not just convenience: an EventSource stream cannot set an
// Authorization header, so requiring one would break the UI for the ordinary
// local case. The exemption rests entirely on RemoteAddr, which is why a
// request carrying proxy forwarding headers is treated as remote wherever it
// connected from — otherwise anything behind a same-host reverse proxy would
// inherit it.
func TokenAuthMiddleware(config TokenAuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope, protected := requiredScope(r.URL.Path)
			if !protected || isLoopbackRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			if config.Verifier == nil {
				// Reachable only if serve wired the chain without a verifier.
				// Refusing beats passing an unauthenticated request through to
				// the command executor.
				http.Error(w, "captain token verification is not configured", http.StatusServiceUnavailable)
				return
			}
			raw, ok := captaintoken.BearerFromHeader(r.Header.Get("Authorization"))
			if !ok {
				writeTokenChallenge(w, http.StatusUnauthorized,
					"this endpoint requires a captain token; mint one with `captain token create`")
				return
			}
			record, err := config.Verifier.VerifyScope(r.Context(), raw, scope)
			if err != nil {
				writeTokenRejection(w, err, scope)
				return
			}
			if config.Touch != nil {
				if err := config.Touch(r.Context(), record.ID); err != nil {
					log.Warnf("record captain token use: %v", err)
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenContextKey{}, record)))
		})
	}
}

// requiredScope reports which scope a path needs, and whether it is protected
// at all. The SPA and its assets are deliberately open: a browser loading the
// UI from another machine has nowhere to put a bearer token.
func requiredScope(path string) (captaintoken.Scope, bool) {
	switch {
	case strings.HasPrefix(path, gitPathPrefix):
		return captaintoken.ScopeGit, true
	case path == "/api" || strings.HasPrefix(path, "/api/"):
		return captaintoken.ScopeAPI, true
	default:
		return "", false
	}
}

// writeTokenRejection maps a verification failure to a status.
//
// The distinction that matters is between "this credential is not good" and "I
// could not tell": a database outage must not read as an authentication
// failure, or an operator chases a phantom auth bug through a downtime.
func writeTokenRejection(w http.ResponseWriter, err error, want captaintoken.Scope) {
	switch {
	case errors.Is(err, captaintoken.ErrScope):
		// The credential is real, so naming the scope it lacks is not a probing
		// aid — and it is the one thing that tells the holder what to fix.
		http.Error(w, "this captain token does not carry the "+string(want)+" scope", http.StatusForbidden)
	case errors.Is(err, captaintoken.ErrRevoked):
		writeTokenChallenge(w, http.StatusUnauthorized, "this captain token has been revoked")
	case errors.Is(err, captaintoken.ErrExpired):
		writeTokenChallenge(w, http.StatusUnauthorized, "this captain token has expired")
	case errors.Is(err, captaintoken.ErrUnknown), errors.Is(err, captaintoken.ErrMalformed):
		// One answer for an unknown id and a wrong secret: telling a prober
		// which half they got right halves the search.
		writeTokenChallenge(w, http.StatusUnauthorized, "invalid captain token")
	default:
		log.Errorf("verify captain token: %v", err)
		http.Error(w, "cannot verify captain tokens right now", http.StatusServiceUnavailable)
	}
}

func writeTokenChallenge(w http.ResponseWriter, status int, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="captain"`)
	http.Error(w, message, status)
}

// forwardedHeaders are set by proxies. Their presence means the request did not
// originate on this machine, whatever RemoteAddr says.
var forwardedHeaders = []string{"X-Forwarded-For", "X-Real-Ip", "X-Forwarded-Host", "Forwarded"}

func isLoopbackRequest(r *http.Request) bool {
	for _, header := range forwardedHeaders {
		if strings.TrimSpace(r.Header.Get(header)) != "" {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// An address that will not parse is treated as remote: failing closed costs
	// a token, failing open costs command execution.
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
