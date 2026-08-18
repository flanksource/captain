package database

import (
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTokenDB(t *testing.T, name string) *DB {
	t.Helper()
	handle := dbtest.ForT(t, dbtest.Options{Name: name})
	db, err := Open(t.Context(), WithDSN(handle.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// secretHalf is the part of a credential that must never be recoverable from
// storage. Parse keeps it unexported, so a caller outside the package splits it
// the same way the wire format documents.
func secretHalf(t *testing.T, raw string) string {
	t.Helper()
	_, secret, ok := strings.Cut(strings.TrimPrefix(raw, captaintoken.Prefix+"_"), ".")
	require.True(t, ok, "credential %q is not in cptn_<id>.<secret> form", raw)
	return secret
}

// The stored row is what an attacker gets from a database dump. Rendering the
// whole row as text catches the secret landing in any column, not just the one
// this test happened to think of.
func TestAPITokenSecretNeverReachesTheDatabase(t *testing.T) {
	db := openTokenDB(t, "captain_api_token_secrecy")

	token, raw, err := db.CreateAPIToken(t.Context(), CreateAPITokenInput{
		Name: "worker-01", Scope: captaintoken.ScopeGit, Agent: "worker-01",
	})
	require.NoError(t, err)

	var dump string
	require.NoError(t, db.Gorm().WithContext(t.Context()).
		Raw("SELECT captain_api_tokens::text FROM captain_api_tokens WHERE token_id = ?", token.TokenID).
		Scan(&dump).Error)

	require.NotEmpty(t, dump)
	assert.NotContains(t, dump, raw.Value(), "the whole credential is stored verbatim")
	assert.NotContains(t, dump, secretHalf(t, raw.Value()), "the secret half is stored verbatim")
	assert.Contains(t, dump, token.TokenID, "the public id is stored, and is how a lookup finds the row")
	assert.Contains(t, dump, "argon2id", "the stored hash should be the argon2id encoding")
}

// The store and the verifier are two halves of one mechanism; this exercises
// them together against real Postgres rather than a stub lookup.
func TestAPITokenVerifiesThroughTheStoreAndStaysReusable(t *testing.T) {
	db := openTokenDB(t, "captain_api_token_verify")
	verifier := captaintoken.NewVerifier(db.LookupAPIToken)

	_, raw, err := db.CreateAPIToken(t.Context(), CreateAPITokenInput{
		Name: "worker-01", Scope: captaintoken.ScopeGit, Agent: "worker-01",
	})
	require.NoError(t, err)

	// Durability is the whole point of replacing the single-use join token: a
	// sidecar that restarts re-presents the same credential.
	for attempt := 1; attempt <= 3; attempt++ {
		record, err := verifier.Verify(t.Context(), raw.Value())
		require.NoErrorf(t, err, "presentation %d", attempt)
		assert.Equal(t, "worker-01", record.Agent)
	}

	// A git-scoped agent token must not reach the API, which executes commands.
	_, err = verifier.VerifyScope(t.Context(), raw.Value(), captaintoken.ScopeAPI)
	assert.ErrorIs(t, err, captaintoken.ErrScope)

	_, err = verifier.Verify(t.Context(), captaintoken.Prefix+"_nosuchid.secret")
	assert.ErrorIs(t, err, captaintoken.ErrUnknown, "an unknown id must not be distinguishable from a wrong secret")
}

// Revocation has to bite on the very next request. The verifier caches a
// successful KDF for 30s, so a cache that also cached liveness would leave a
// revoked credential working for that window.
func TestRevokedAPITokenFailsOnTheNextRequestDespiteTheVerifierCache(t *testing.T) {
	db := openTokenDB(t, "captain_api_token_revoke")
	verifier := captaintoken.NewVerifier(db.LookupAPIToken)

	token, raw, err := db.CreateAPIToken(t.Context(), CreateAPITokenInput{
		Name: "worker-01", Scope: captaintoken.ScopeGit, Agent: "worker-01",
	})
	require.NoError(t, err)

	_, err = verifier.Verify(t.Context(), raw.Value())
	require.NoError(t, err, "the token should work before it is revoked, so the cache is warm")

	require.NoError(t, db.RevokeAPIToken(t.Context(), token.TokenID, "agent decommissioned"))

	_, err = verifier.Verify(t.Context(), raw.Value())
	assert.ErrorIs(t, err, captaintoken.ErrRevoked)

	// Revoking twice satisfies the caller's intent rather than erroring on a race.
	require.NoError(t, db.RevokeAPIToken(t.Context(), token.TokenID, "again"))

	stored, err := db.GetAPIToken(t.Context(), token.TokenID)
	require.NoError(t, err)
	require.NotNil(t, stored.RevokedAt)
	assert.Equal(t, "agent decommissioned", stored.RevocationReason, "the first reason stands")

	err = db.RevokeAPIToken(t.Context(), "nosuchid", "")
	assert.ErrorIs(t, err, ErrAPITokenNotFound)
}

func TestExpiredAPITokenIsRejected(t *testing.T) {
	db := openTokenDB(t, "captain_api_token_expiry")
	verifier := captaintoken.NewVerifier(db.LookupAPIToken)

	expiresAt := time.Now().Add(time.Hour)
	token, raw, err := db.CreateAPIToken(t.Context(), CreateAPITokenInput{
		Name: "worker-01", Scope: captaintoken.ScopeGit, Agent: "worker-01", ExpiresAt: &expiresAt,
	})
	require.NoError(t, err)
	_, err = verifier.Verify(t.Context(), raw.Value())
	require.NoError(t, err)

	// Age the whole row rather than sleeping through the hour. created_at moves
	// with it because the table refuses an expiry that precedes creation.
	require.NoError(t, db.Gorm().WithContext(t.Context()).Exec(`
		UPDATE captain_api_tokens
		SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour'
		WHERE token_id = ?`, token.TokenID).Error)

	_, err = verifier.Verify(t.Context(), raw.Value())
	assert.ErrorIs(t, err, captaintoken.ErrExpired)

	past := time.Now().Add(-time.Minute)
	_, _, err = db.CreateAPIToken(t.Context(), CreateAPITokenInput{
		Name: "worker-02", Scope: captaintoken.ScopeGit, Agent: "worker-02", ExpiresAt: &past,
	})
	assert.ErrorIs(t, err, ErrAPITokenInvalid, "a token that is born expired is a mistake, not a valid state")
}

// One credential serving a scaled Deployment is the reason pool tokens exist.
// The supervisor derives every member name, so a client cannot invent one.
func TestPoolTokenAdmitsMembersAndKeepsTheirNamesAcrossRestarts(t *testing.T) {
	db := openTokenDB(t, "captain_api_token_pool")

	token, _, err := db.CreateAPIToken(t.Context(), CreateAPITokenInput{
		Name: "prod-pool", Scope: captaintoken.ScopeGit, Pool: true, MaxAgents: 2,
	})
	require.NoError(t, err)

	first, err := db.AdmitAPITokenAgent(t.Context(), token.TokenID, "")
	require.NoError(t, err)
	assert.Equal(t, "prod-pool-01", first)

	second, err := db.AdmitAPITokenAgent(t.Context(), token.TokenID, "")
	require.NoError(t, err)
	assert.Equal(t, "prod-pool-02", second)

	// A restart re-presents the name the member persisted, and must not consume
	// a third slot — otherwise a rescheduled pod would exhaust the pool.
	returning, err := db.AdmitAPITokenAgent(t.Context(), token.TokenID, first)
	require.NoError(t, err)
	assert.Equal(t, first, returning)

	_, err = db.AdmitAPITokenAgent(t.Context(), token.TokenID, "")
	assert.ErrorIs(t, err, ErrAPITokenPoolFull)

	// A name the supervisor never issued is not admitted under it; the pool is
	// full, so the attempt is refused rather than quietly granted.
	_, err = db.AdmitAPITokenAgent(t.Context(), token.TokenID, "attacker-chosen")
	assert.ErrorIs(t, err, ErrAPITokenPoolFull)

	stored, err := db.GetAPIToken(t.Context(), token.TokenID)
	require.NoError(t, err)
	assert.Equal(t, []string{"prod-pool-01", "prod-pool-02"}, stored.PoolAgents)
}

func TestBoundAPITokenAdmitsOnlyItsOwnAgent(t *testing.T) {
	db := openTokenDB(t, "captain_api_token_bound")

	token, _, err := db.CreateAPIToken(t.Context(), CreateAPITokenInput{
		Name: "worker-01", Scope: captaintoken.ScopeGit, Agent: "worker-01",
	})
	require.NoError(t, err)

	name, err := db.AdmitAPITokenAgent(t.Context(), token.TokenID, "")
	require.NoError(t, err)
	assert.Equal(t, "worker-01", name)

	name, err = db.AdmitAPITokenAgent(t.Context(), token.TokenID, "worker-01")
	require.NoError(t, err)
	assert.Equal(t, "worker-01", name)

	_, err = db.AdmitAPITokenAgent(t.Context(), token.TokenID, "worker-02")
	assert.ErrorIs(t, err, ErrAPITokenInvalid, "a bound token must not be able to act as another agent")

	_, err = db.AdmitAPITokenAgent(t.Context(), "nosuchid", "")
	assert.ErrorIs(t, err, captaintoken.ErrUnknown)
}

// An unbounded pool still has to stop somewhere, and the derived names have to
// stay inside the 64-character ref segment R8.3 splits on.
func TestUnboundedPoolDerivesNamesThatRemainValidRefSegments(t *testing.T) {
	db := openTokenDB(t, "captain_api_token_unbounded")

	token, _, err := db.CreateAPIToken(t.Context(), CreateAPITokenInput{
		Name: "prod-pool", Scope: captaintoken.ScopeGit, Pool: true,
	})
	require.NoError(t, err)

	for range 3 {
		name, err := db.AdmitAPITokenAgent(t.Context(), token.TokenID, "")
		require.NoError(t, err)
		require.NoError(t, captaintoken.ValidateName(name))
	}

	stored, err := db.GetAPIToken(t.Context(), token.TokenID)
	require.NoError(t, err)
	assert.Equal(t, 0, stored.MaxAgents, "an unbounded pool records no cap")
	assert.Len(t, stored.PoolAgents, 3)
}

// The identity combinations the table's CHECK enforces are validated in prose
// first, so an operator sees the reason rather than a constraint violation.
func TestCreateAPITokenRefusesImpossibleIdentities(t *testing.T) {
	db := openTokenDB(t, "captain_api_token_identity")

	tests := []struct {
		name  string
		input CreateAPITokenInput
		want  string
	}{
		{"an api token bound to an agent", CreateAPITokenInput{
			Name: "ci", Scope: captaintoken.ScopeAPI, Agent: "ci",
		}, "neither pooled nor bound"},
		{"a pooled api token", CreateAPITokenInput{
			Name: "ci", Scope: captaintoken.ScopeAPI, Pool: true,
		}, "neither pooled nor bound"},
		{"a git token naming no agent", CreateAPITokenInput{
			Name: "worker", Scope: captaintoken.ScopeGit,
		}, "must name the agent"},
		{"a pool token also bound to an agent", CreateAPITokenInput{
			Name: "prod-pool", Scope: captaintoken.ScopeGit, Pool: true, Agent: "worker-01",
		}, "names its members as they arrive"},
		{"max-agents on a bound token", CreateAPITokenInput{
			Name: "worker-01", Scope: captaintoken.ScopeGit, Agent: "worker-01", MaxAgents: 4,
		}, "max-agents applies to a pool token"},
		{"a name that cannot be a ref segment", CreateAPITokenInput{
			Name: "Prod Pool", Scope: captaintoken.ScopeGit, Pool: true,
		}, "token name"},
		{"a pool name too long to leave room for a member suffix", CreateAPITokenInput{
			Name: strings.Repeat("a", maxPoolTokenNameLen+1), Scope: captaintoken.ScopeGit, Pool: true,
		}, "at most 60 characters"},
		{"an unknown scope", CreateAPITokenInput{
			Name: "worker-01", Scope: "admin", Agent: "worker-01",
		}, "must be git or api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := db.CreateAPIToken(t.Context(), tt.input)
			require.ErrorIs(t, err, ErrAPITokenInvalid)
			assert.Contains(t, err.Error(), tt.want)
		})
	}

	// An api-scoped token needs no agent at all, which is the one combination
	// the git scope forbids.
	_, _, err := db.CreateAPIToken(t.Context(), CreateAPITokenInput{Name: "ci", Scope: captaintoken.ScopeAPI})
	require.NoError(t, err)
}

func TestListAPITokensFiltersIncludingPoolMembership(t *testing.T) {
	db := openTokenDB(t, "captain_api_token_list")

	bound, _, err := db.CreateAPIToken(t.Context(), CreateAPITokenInput{
		Name: "worker-01", Scope: captaintoken.ScopeGit, Agent: "worker-01",
	})
	require.NoError(t, err)
	pool, _, err := db.CreateAPIToken(t.Context(), CreateAPITokenInput{
		Name: "prod-pool", Scope: captaintoken.ScopeGit, Pool: true,
	})
	require.NoError(t, err)
	_, _, err = db.CreateAPIToken(t.Context(), CreateAPITokenInput{Name: "ci", Scope: captaintoken.ScopeAPI})
	require.NoError(t, err)

	member, err := db.AdmitAPITokenAgent(t.Context(), pool.TokenID, "")
	require.NoError(t, err)

	all, err := db.ListAPITokens(t.Context(), ListAPITokensFilter{})
	require.NoError(t, err)
	assert.Len(t, all, 3)

	byScope, err := db.ListAPITokens(t.Context(), ListAPITokensFilter{Scope: captaintoken.ScopeAPI})
	require.NoError(t, err)
	require.Len(t, byScope, 1)
	assert.Equal(t, "ci", byScope[0].Name)

	byAgent, err := db.ListAPITokens(t.Context(), ListAPITokensFilter{Agent: "worker-01"})
	require.NoError(t, err)
	require.Len(t, byAgent, 1)
	assert.Equal(t, bound.TokenID, byAgent[0].TokenID)

	// A pool member is named in pool_agents, not in the bound agent column; a
	// search that only looked at the column would report the agent as unknown.
	byMember, err := db.ListAPITokens(t.Context(), ListAPITokensFilter{Agent: member})
	require.NoError(t, err)
	require.Len(t, byMember, 1)
	assert.Equal(t, pool.TokenID, byMember[0].TokenID)

	require.NoError(t, db.RevokeAPIToken(t.Context(), bound.TokenID, "retired"))

	live, err := db.ListAPITokens(t.Context(), ListAPITokensFilter{})
	require.NoError(t, err)
	assert.Len(t, live, 2, "a listing answers what can reach this captain right now")

	withRevoked, err := db.ListAPITokens(t.Context(), ListAPITokensFilter{IncludeRevoked: true})
	require.NoError(t, err)
	assert.Len(t, withRevoked, 3)

	none, err := db.ListAPITokens(t.Context(), ListAPITokensFilter{Agent: "worker-99"})
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.NotNil(t, none, "an empty result must marshal as [] rather than null")
}

// Git smart-HTTP makes several requests per push, so an unthrottled touch would
// turn one push into a burst of writes carrying no extra information.
func TestTouchAPITokenIsThrottled(t *testing.T) {
	db := openTokenDB(t, "captain_api_token_touch")

	token, _, err := db.CreateAPIToken(t.Context(), CreateAPITokenInput{
		Name: "worker-01", Scope: captaintoken.ScopeGit, Agent: "worker-01",
	})
	require.NoError(t, err)

	stored, err := db.GetAPIToken(t.Context(), token.TokenID)
	require.NoError(t, err)
	assert.Nil(t, stored.LastUsedAt, "an unused token has no last-used stamp")

	require.NoError(t, db.TouchAPIToken(t.Context(), token.TokenID))
	first, err := db.GetAPIToken(t.Context(), token.TokenID)
	require.NoError(t, err)
	require.NotNil(t, first.LastUsedAt)

	require.NoError(t, db.TouchAPIToken(t.Context(), token.TokenID))
	second, err := db.GetAPIToken(t.Context(), token.TokenID)
	require.NoError(t, err)
	require.NotNil(t, second.LastUsedAt)
	assert.Equal(t, *first.LastUsedAt, *second.LastUsedAt, "a touch inside the interval must not rewrite the row")

	// Age the stamp past the interval; the next touch should advance it.
	require.NoError(t, db.Gorm().WithContext(t.Context()).Exec(
		"UPDATE captain_api_tokens SET last_used_at = now() - interval '1 hour' WHERE token_id = ?",
		token.TokenID).Error)
	require.NoError(t, db.TouchAPIToken(t.Context(), token.TokenID))
	third, err := db.GetAPIToken(t.Context(), token.TokenID)
	require.NoError(t, err)
	require.NotNil(t, third.LastUsedAt)
	assert.True(t, third.LastUsedAt.After(*second.LastUsedAt), "a touch past the interval records the new use")

	require.NoError(t, db.TouchAPIToken(t.Context(), "nosuchid"), "touching an unknown token is a no-op, not an error")
}
