package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServeBaseURL(t *testing.T) {
	t.Setenv(ServeURLEnv, "")
	assert.Equal(t, DefaultServeURL, ServeBaseURL())
	t.Setenv(ServeURLEnv, "http://localhost:9999")
	assert.Equal(t, "http://localhost:9999", ServeBaseURL())
}

func TestMonitorHooksEnabled(t *testing.T) {
	t.Setenv(MonitorHooksEnv, "")
	assert.True(t, MonitorHooksEnabled(Spec{}), "default requests carry monitoring hooks")
	assert.True(t, MonitorHooksEnabled(Spec{Memory: Memory{SkipHooks: true}}),
		"SkipHooks governs user hooks, not captain's monitoring hooks")
	assert.False(t, MonitorHooksEnabled(Spec{Memory: Memory{Bare: true}}), "bare runs opt out")

	t.Setenv(MonitorHooksEnv, "off")
	assert.False(t, MonitorHooksEnabled(Spec{}), "CAPTAIN_MONITOR_HOOKS=off opts out")
}
