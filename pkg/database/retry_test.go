package database

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubSQLStateErr satisfies the sqlStateError interface that pgconn and lib/pq
// errors implement, so the classifier can be exercised without a live driver.
type stubSQLStateErr struct{ code string }

func (e stubSQLStateErr) Error() string    { return "sqlstate " + e.code }
func (e stubSQLStateErr) SQLState() string { return e.code }

func TestIsRetryableTxErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deadlock", err: stubSQLStateErr{"40P01"}, want: true},
		{name: "serialization failure", err: stubSQLStateErr{"40001"}, want: true},
		{name: "wrapped deadlock", err: fmt.Errorf("ingest: %w", stubSQLStateErr{"40P01"}), want: true},
		{name: "unique violation", err: stubSQLStateErr{"23505"}, want: false},
		{name: "lock timeout is migration-only", err: stubSQLStateErr{"55P03"}, want: false},
		{name: "plain error", err: errors.New("boom"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isRetryableTxErr(tt.err))
		})
	}
}

func TestRetryTransientTxSucceedsAfterTransientDeadlocks(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := retryTransientTx(t.Context(), "ingest", func() error {
		attempts++
		if attempts < 3 {
			return stubSQLStateErr{"40P01"}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, attempts, "must retry until the transaction commits")
}

func TestRetryTransientTxReturnsNonRetryableImmediately(t *testing.T) {
	t.Parallel()

	attempts := 0
	sentinel := stubSQLStateErr{"23505"}
	err := retryTransientTx(t.Context(), "ingest", func() error {
		attempts++
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, attempts, "a non-retryable error must not be retried")
}

func TestRetryTransientTxExhaustsAttempts(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := retryTransientTx(t.Context(), "ingest", func() error {
		attempts++
		return stubSQLStateErr{"40001"}
	})
	require.Error(t, err)
	assert.Equal(t, txRetryMaxAttempts, attempts)
	assert.Contains(t, err.Error(), fmt.Sprintf("gave up after %d attempts", txRetryMaxAttempts))
}

func TestRetryTransientTxHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	attempts := 0
	err := retryTransientTx(ctx, "ingest", func() error {
		attempts++
		cancel() // cancel before the backoff wait
		return stubSQLStateErr{"40P01"}
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, attempts, "cancellation during backoff must stop retrying")
}
