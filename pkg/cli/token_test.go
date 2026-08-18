package cli

import (
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A credential lifetime is naturally expressed in days, which time.ParseDuration
// does not accept — so the day form has to work, and the hour form has to keep
// working alongside it.
func TestParseTokenLifetime(t *testing.T) {
	tests := []struct {
		value string
		want  time.Duration
	}{
		{"90d", 90 * 24 * time.Hour},
		{"1d", 24 * time.Hour},
		{"720h", 720 * time.Hour},
		{"30m", 30 * time.Minute},
		{" 7d ", 7 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			before := time.Now()
			expiresAt, err := parseTokenLifetime(tt.value)
			require.NoError(t, err)
			require.NotNil(t, expiresAt)
			// Compare against an independently computed instant, allowing for the
			// clock advancing between the two Now() reads.
			assert.WithinDuration(t, before.Add(tt.want), *expiresAt, time.Second)
		})
	}

	t.Run("an empty lifetime means the token never expires", func(t *testing.T) {
		expiresAt, err := parseTokenLifetime("  ")
		require.NoError(t, err)
		assert.Nil(t, expiresAt)
	})

	// A lifetime that is already spent would create a token that can never be
	// used; refusing it names the mistake instead of storing it.
	for _, value := range []string{"0d", "-1d", "0h", "-720h"} {
		t.Run("refuses "+value, func(t *testing.T) {
			_, err := parseTokenLifetime(value)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "must be positive")
		})
	}

	for _, value := range []string{"ninety", "90days", "d", "90 d", "90y"} {
		t.Run("refuses "+value, func(t *testing.T) {
			_, err := parseTokenLifetime(value)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "90d")
		})
	}
}

// A listing has to distinguish a credential that lapsed from one that was
// deliberately retired: they call for different actions.
func TestTokenStatus(t *testing.T) {
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	tests := []struct {
		name  string
		token database.APIToken
		want  string
	}{
		{"no expiry or revocation", database.APIToken{}, "active"},
		{"expiring later", database.APIToken{ExpiresAt: &future}, "active"},
		{"expired", database.APIToken{ExpiresAt: &past}, "expired"},
		{"revoked without a reason", database.APIToken{RevokedAt: &past}, "revoked"},
		{"revoked with a reason", database.APIToken{
			RevokedAt: &past, RevocationReason: "agent decommissioned",
		}, "revoked: agent decommissioned"},
		{"revocation outranks expiry", database.APIToken{
			RevokedAt: &past, ExpiresAt: &past,
		}, "revoked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tokenStatus(tt.token))
		})
	}
}

// One column answers "who can push as this?" for both token shapes, so a pool
// with no members yet has to say so rather than render as an empty cell that
// reads like a bound token missing its agent.
func TestTokenAgentColumn(t *testing.T) {
	assert.Equal(t, "worker-01", tokenAgentColumn(database.APIToken{Agent: "worker-01"}))
	assert.Equal(t, "(pool, no members yet)", tokenAgentColumn(database.APIToken{Pool: true}))
	assert.Equal(t, "prod-pool-01, prod-pool-02", tokenAgentColumn(database.APIToken{
		Pool: true, PoolAgents: []string{"prod-pool-01", "prod-pool-02"},
	}))
}
