package adapter

import (
	"context"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func TestNoneAdapter(t *testing.T) {
	sandbox, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxNone})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()

	if sandbox.Kind() != api.SandboxNone {
		t.Fatalf("kind = %q", sandbox.Kind())
	}
	session, err := sandbox.Prepare(context.Background(), &api.Spec{})
	if err != nil || session == nil {
		t.Fatalf("session = %v, err = %v", session, err)
	}
	if session.WorkDir != "" || len(session.Env) != 0 {
		t.Fatalf("none must change nothing, got %+v", session)
	}
	// No CommandWrapper: the exec seam must fall through to a bare command,
	// matching the descriptor's empty capability list.
	if _, ok := api.SandboxAs[api.CommandWrapper](sandbox); ok {
		t.Fatal("none must not provide a CommandWrapper")
	}
}
