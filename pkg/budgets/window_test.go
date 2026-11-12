// Tests where a budget window starts: relative datemath evaluated in UTC, and why an absolute,
// unparseable or future window is refused.
package budgets_test

import (
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/budgets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowStart(t *testing.T) {
	now := time.Date(2026, 11, 11, 15, 30, 0, 0, time.UTC)

	for window, want := range map[string]time.Time{
		"now/d":   time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
		"now/M":   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		"now-30d": now.AddDate(0, 0, -30),
		" now/d ": time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
	} {
		start, err := budgets.WindowStart(window, now)
		require.NoError(t, err, window)
		assert.Equal(t, want, start, window)
	}

	for window, reason := range map[string]string{
		"2026-11-01": `budget window "2026-11-01" must be relative to now`,
		"now+1d":     `budget window "now+1d" starts after now`,
		"now-x":      `invalid budget window "now-x": `,
	} {
		_, err := budgets.WindowStart(window, now)
		require.Error(t, err, window)
		assert.Contains(t, err.Error(), reason, window)
	}
}
