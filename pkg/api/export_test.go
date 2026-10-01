package api

import "sync"

// ResolveApprovalsForTest runs the normalization NewProvider applies.
func (c *Config) ResolveApprovalsForTest() error { return c.resolveApprovals() }

// SetCompatLogForTest captures the compatibility shim's warnings and resets its
// once-per-process state, returning a restore func for DeferCleanup.
func SetCompatLogForTest(warnf, debugf func(string, ...any)) func() {
	previous := compat
	compat.warnf, compat.debugf = warnf, debugf
	compat.entryPoints, compat.skips = &sync.Map{}, &sync.Map{}
	return func() { compat = previous }
}
