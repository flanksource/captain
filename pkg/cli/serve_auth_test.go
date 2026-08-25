package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authFixture is a middleware wired over one minted token, plus the state a
// test needs to tell what the chain did.
type authFixture struct {
	handler  http.Handler
	raw      string
	record   *captaintoken.Record
	reached  bool
	seen     captaintoken.Record
	touched  []string
	touchErr error
	lookupFn func(context.Context, string) (captaintoken.Record, error)
}

func newAuthFixture(t *testing.T, scope captaintoken.Scope) *authFixture {
	t.Helper()
	minted, err := captaintoken.Mint()
	require.NoError(t, err)

	fixture := &authFixture{raw: minted.Secret.Value()}
	fixture.record = &captaintoken.Record{
		ID: minted.ID, SecretHash: minted.Hash, Name: "worker-01", Scope: scope, Agent: "worker-01",
	}
	lookup := func(ctx context.Context, id string) (captaintoken.Record, error) {
		if fixture.lookupFn != nil {
			return fixture.lookupFn(ctx, id)
		}
		if id != fixture.record.ID {
			return captaintoken.Record{}, captaintoken.ErrUnknown
		}
		return *fixture.record, nil
	}
	middleware := TokenAuthMiddleware(TokenAuthConfig{
		Verifier: captaintoken.NewVerifier(lookup),
		Touch: func(_ context.Context, tokenID string) error {
			fixture.touched = append(fixture.touched, tokenID)
			return fixture.touchErr
		},
	})
	fixture.handler = middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.reached = true
		if record, ok := TokenFromContext(r.Context()); ok {
			fixture.seen = record
		}
		w.WriteHeader(http.StatusOK)
	}))
	return fixture
}

// call drives one request. remoteAddr chooses whether it looks local.
func (f *authFixture) call(method, path, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	f.reached, f.seen = false, captaintoken.Record{}
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = remoteAddr
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

const (
	loopbackAddr = "127.0.0.1:54321"
	remoteAddr   = "10.1.2.3:54321"
)

// The webapp, the local CLI and hook subprocesses all talk to this server from
// 127.0.0.1, and an EventSource stream cannot set an Authorization header — so
// requiring a token locally would break the UI rather than secure it.
func TestLoopbackNeedsNoToken(t *testing.T) {
	fixture := newAuthFixture(t, captaintoken.ScopeAPI)

	for _, addr := range []string{loopbackAddr, "[::1]:54321", "127.0.0.5:9"} {
		recorder := fixture.call(http.MethodPost, "/api/v1/sessions", addr, nil)
		assert.Equal(t, http.StatusOK, recorder.Code, "loopback %s should not need a token", addr)
		assert.True(t, fixture.reached)
	}

	// A loopback request carries no credential, so a handler must not be able to
	// mistake it for an authenticated one.
	_, ok := TokenFromContext(context.Background())
	assert.False(t, ok)
	assert.Empty(t, fixture.touched, "a loopback request touches no token")
}

// The loopback exemption rests entirely on RemoteAddr. Behind a same-host
// reverse proxy every request would arrive from 127.0.0.1 and inherit it, so
// the forwarding headers a proxy adds have to revoke it.
func TestForwardedRequestIsNotTreatedAsLoopback(t *testing.T) {
	fixture := newAuthFixture(t, captaintoken.ScopeAPI)

	for _, header := range []string{"X-Forwarded-For", "X-Real-Ip", "X-Forwarded-Host", "Forwarded"} {
		recorder := fixture.call(http.MethodPost, "/api/v1/sessions", loopbackAddr, map[string]string{header: "203.0.113.9"})
		assert.Equalf(t, http.StatusUnauthorized, recorder.Code,
			"%s means the request did not originate on this machine", header)
		assert.False(t, fixture.reached)
	}
}

func TestRemoteRequestWithoutATokenIsChallenged(t *testing.T) {
	fixture := newAuthFixture(t, captaintoken.ScopeAPI)

	for _, header := range []string{"", "Basic dXNlcjpwYXNz", "Bearer", "Bearer   "} {
		headers := map[string]string{}
		if header != "" {
			headers["Authorization"] = header
		}
		recorder := fixture.call(http.MethodPost, "/api/v1/sessions", remoteAddr, headers)
		require.Equal(t, http.StatusUnauthorized, recorder.Code)
		assert.Equal(t, `Bearer realm="captain"`, recorder.Header().Get("WWW-Authenticate"))
		assert.Contains(t, recorder.Body.String(), "captain token create", "the challenge should say how to get one")
		assert.False(t, fixture.reached)
	}
}

func TestRemoteRequestWithAValidTokenReachesTheHandler(t *testing.T) {
	fixture := newAuthFixture(t, captaintoken.ScopeAPI)

	recorder := fixture.call(http.MethodPost, "/api/v1/sessions", remoteAddr,
		map[string]string{"Authorization": "Bearer " + fixture.raw})

	require.Equal(t, http.StatusOK, recorder.Code)
	require.True(t, fixture.reached)
	assert.Equal(t, "worker-01", fixture.seen.Agent, "the handler needs to know which identity is calling")
	assert.Equal(t, []string{fixture.record.ID}, fixture.touched)
}

// A leaked agent token must not reach the executor, which runs captain
// commands — that separation is the whole reason there are two scopes.
func TestGitScopedTokenIsRefusedByTheCommandAPI(t *testing.T) {
	fixture := newAuthFixture(t, captaintoken.ScopeGit)
	authorized := map[string]string{"Authorization": "Bearer " + fixture.raw}

	recorder := fixture.call(http.MethodPost, "/api/v1/sessions", remoteAddr, authorized)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "api scope")
	assert.False(t, fixture.reached)

	// The same token is exactly right for the endpoint it was minted for.
	recorder = fixture.call(http.MethodPost, "/git/mailboxes/aaa.git/git-receive-pack", remoteAddr, authorized)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, fixture.reached)
}

func TestAPIScopedTokenIsRefusedByTheGitEndpoint(t *testing.T) {
	fixture := newAuthFixture(t, captaintoken.ScopeAPI)

	recorder := fixture.call(http.MethodGet, "/git/mailboxes/aaa.git/info/refs", remoteAddr,
		map[string]string{"Authorization": "Bearer " + fixture.raw})

	require.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "git scope")
}

func TestRejectionsDistinguishWhatTheHolderCanAct(t *testing.T) {
	revokedAt := time.Now().Add(-time.Minute)
	expiredAt := time.Now().Add(-time.Minute)

	tests := []struct {
		name   string
		mutate func(*authFixture)
		body   string
	}{
		{"revoked", func(f *authFixture) { f.record.RevokedAt = &revokedAt }, "revoked"},
		{"expired", func(f *authFixture) { f.record.ExpiresAt = &expiredAt }, "expired"},
		// An unknown id and a wrong secret get one answer: distinguishing them
		// tells a prober which half of the credential they got right.
		{"unknown", func(f *authFixture) { f.record.ID = "someotherid" }, "invalid captain token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newAuthFixture(t, captaintoken.ScopeAPI)
			tt.mutate(fixture)

			recorder := fixture.call(http.MethodPost, "/api/v1/sessions", remoteAddr,
				map[string]string{"Authorization": "Bearer " + fixture.raw})

			require.Equal(t, http.StatusUnauthorized, recorder.Code)
			assert.Equal(t, `Bearer realm="captain"`, recorder.Header().Get("WWW-Authenticate"))
			assert.Contains(t, recorder.Body.String(), tt.body)
			assert.False(t, fixture.reached)
		})
	}

	t.Run("a garbage credential never reaches the store", func(t *testing.T) {
		fixture := newAuthFixture(t, captaintoken.ScopeAPI)
		var lookups int
		fixture.lookupFn = func(context.Context, string) (captaintoken.Record, error) {
			lookups++
			return captaintoken.Record{}, captaintoken.ErrUnknown
		}
		recorder := fixture.call(http.MethodPost, "/api/v1/sessions", remoteAddr,
			map[string]string{"Authorization": "Bearer not-a-captain-token"})
		assert.Equal(t, http.StatusUnauthorized, recorder.Code)
		assert.Zero(t, lookups)
	})
}

// "I cannot tell" is not "you are not allowed". A 401 during a database outage
// sends an operator hunting a phantom auth bug instead of the downtime.
func TestAStoreOutageIsNotAnAuthenticationFailure(t *testing.T) {
	fixture := newAuthFixture(t, captaintoken.ScopeAPI)
	fixture.lookupFn = func(context.Context, string) (captaintoken.Record, error) {
		return captaintoken.Record{}, errors.New("dial tcp 127.0.0.1:7432: connection refused")
	}

	recorder := fixture.call(http.MethodPost, "/api/v1/sessions", remoteAddr,
		map[string]string{"Authorization": "Bearer " + fixture.raw})

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "connection refused", "an internal failure should not leak the DSN")
	assert.False(t, fixture.reached)
}

// The SPA has nowhere to put a bearer token, so the pages a browser loads stay
// open; the API and git subtrees that act on the host are protected.
func TestTheAPIAndGitSubtreesAreProtected(t *testing.T) {
	fixture := newAuthFixture(t, captaintoken.ScopeAPI)

	for _, path := range []string{"/", "/health", "/assets/index.js"} {
		recorder := fixture.call(http.MethodGet, path, remoteAddr, nil)
		assert.Equalf(t, http.StatusOK, recorder.Code, "%s should not require a token", path)
	}

	for _, path := range []string{
		"/api/openapi.json", "/api/v1/sessions", "/api/captain/prompt/runs", "/api/chat",
		"/api/attachments/aaa", "/git/mailboxes/aaa.git/info/refs",
	} {
		recorder := fixture.call(http.MethodGet, path, remoteAddr, nil)
		assert.Equalf(t, http.StatusUnauthorized, recorder.Code, "%s must require a token", path)
	}

	// A path that merely starts with the same letters is not the API subtree.
	recorder := fixture.call(http.MethodGet, "/apiary", remoteAddr, nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
}

// Recording that a token was used is bookkeeping. Failing it would turn a
// working push into a 500 for no reason a caller could act on.
func TestATouchFailureDoesNotFailTheRequest(t *testing.T) {
	fixture := newAuthFixture(t, captaintoken.ScopeAPI)
	fixture.touchErr = errors.New("write conflict")

	recorder := fixture.call(http.MethodPost, "/api/v1/sessions", remoteAddr,
		map[string]string{"Authorization": "Bearer " + fixture.raw})

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, fixture.reached)
}

// A chain wired without a verifier must refuse rather than pass an
// unauthenticated request through to the command executor.
func TestAMisconfiguredChainRefusesRatherThanOpensUp(t *testing.T) {
	var reached bool
	handler := TokenAuthMiddleware(TokenAuthConfig{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	request.RemoteAddr = remoteAddr
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.False(t, reached)
}

// An unparseable RemoteAddr must fail closed: the cost of failing closed is a
// token, the cost of failing open is command execution.
func TestAnUnparseableRemoteAddressIsTreatedAsRemote(t *testing.T) {
	fixture := newAuthFixture(t, captaintoken.ScopeAPI)

	for _, addr := range []string{"", "not-an-address", "example.com:443"} {
		recorder := fixture.call(http.MethodPost, "/api/v1/sessions", addr, nil)
		assert.Equalf(t, http.StatusUnauthorized, recorder.Code, "RemoteAddr %q should not be trusted as local", addr)
	}
}
