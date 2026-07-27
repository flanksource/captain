package database

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	txRetryMaxAttempts = 5
	txRetryBackoff     = 200 * time.Millisecond
)

// isRetryableTxErr reports whether err is a transient transaction failure that
// re-running an idempotent transaction can clear: a detected deadlock (40P01)
// or a serialization failure (40001). A concurrent migration taking ACCESS
// EXCLUSIVE locks on tables an ingest transaction is writing is the canonical
// cause. The SQLSTATE is read through the SQLState() interface that both
// jackc/pgconn and lib/pq errors satisfy, matching isUniqueViolation.
func isRetryableTxErr(err error) bool {
	var sqlState sqlStateError
	if !errors.As(err, &sqlState) {
		return false
	}
	switch sqlState.SQLState() {
	case "40P01", "40001":
		return true
	default:
		return false
	}
}

// retryTransientTx runs fn up to txRetryMaxAttempts times while it fails with a
// retryable transaction error, backing off linearly between attempts. fn MUST
// be idempotent — it re-runs the whole transaction on each attempt. Context
// cancellation aborts the wait and returns ctx.Err().
func retryTransientTx(ctx context.Context, desc string, fn func() error) error {
	var lastErr error
	for attempt := 1; attempt <= txRetryMaxAttempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if !isRetryableTxErr(err) {
			return err
		}
		lastErr = err
		if attempt < txRetryMaxAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * txRetryBackoff):
			}
		}
	}
	return fmt.Errorf("%s: gave up after %d attempts under lock contention: %w", desc, txRetryMaxAttempts, lastErr)
}
