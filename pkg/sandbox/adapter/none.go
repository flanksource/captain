// Package adapter holds the sandbox adapter implementations and their
// registrations against pkg/api's sandbox registry. Importing it (blank is
// enough) makes every adapter constructible through api.NewSandbox — the same
// pattern as database/sql drivers and pkg/ai/provider's init().
package adapter

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
)

// noneSandbox is the identity adapter: the run executes as a bare child process
// on the host, exactly as every run did before adapters existed. It declares no
// capabilities, so the exec seam falls through to an unwrapped command.
type identitySandbox struct{ kind api.SandboxKind }

// None is the SandboxFactory for the identity adapter, exported so tests that
// stub the "none" registration can restore it.
func Off(api.SandboxConfig) (api.Sandbox, error) {
	return identitySandbox{kind: api.SandboxOff}, nil
}

func Native(api.SandboxConfig) (api.Sandbox, error) {
	return identitySandbox{kind: api.SandboxNative}, nil
}

func init() {
	api.RegisterSandbox(api.SandboxOff, Off)
	api.RegisterSandbox(api.SandboxNative, Native)
}

func (s identitySandbox) Kind() api.SandboxKind { return s.kind }

func (identitySandbox) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	return &api.SandboxSession{}, nil
}

func (identitySandbox) Close() error { return nil }
