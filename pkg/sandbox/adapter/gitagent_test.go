package adapter

import (
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func TestGitAgentRejectsInvalidWaitTimeout(t *testing.T) {
	for _, value := range []any{"fifteen", "0s", 15} {
		_, err := GitAgent(api.SandboxConfig{Options: map[string]any{"waitTimeout": value}})
		if err == nil {
			t.Fatalf("waitTimeout %#v was accepted", value)
		}
	}
}
