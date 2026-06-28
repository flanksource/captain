package provider

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
)

// CodexAppServer drives `codex app-server` over JSON-RPC stdio, replacing the
// `codex exec --json` path in codex_cli.go.
//
// NOTE: this is a stub. The implementation is filled in by WS-G. The exported
// signatures (NewCodexAppServer + interface methods) are FIXED —
// pkg/ai/provider/init.go registers NewCodexAppServer for ai.BackendCodexCLI.
type CodexAppServer struct {
	model string
}

var errCodexAppServerNotImplemented = fmt.Errorf("codex app-server provider: not implemented")

// NewCodexAppServer builds a codex app-server provider. The supervised process
// is started lazily on the first ExecuteStream.
func NewCodexAppServer(model string) (*CodexAppServer, error) {
	return &CodexAppServer{model: model}, nil
}

func (c *CodexAppServer) GetModel() string       { return c.model }
func (c *CodexAppServer) GetBackend() ai.Backend { return ai.BackendCodexCLI }

func (c *CodexAppServer) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	return nil, errCodexAppServerNotImplemented
}

func (c *CodexAppServer) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	return nil, errCodexAppServerNotImplemented
}
