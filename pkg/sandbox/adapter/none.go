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
type noneSandbox struct{}

// None is the SandboxFactory for the identity adapter, exported so tests that
// stub the "none" registration can restore it.
func None(api.SandboxConfig) (api.Sandbox, error) { return noneSandbox{}, nil }

func init() { api.RegisterSandbox(api.SandboxNone, None) }

func (noneSandbox) Kind() api.SandboxKind { return api.SandboxNone }

func (noneSandbox) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	return &api.SandboxSession{}, nil
}

func (noneSandbox) Close() error { return nil }
