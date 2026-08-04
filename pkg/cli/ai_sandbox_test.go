package cli

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

func TestResolveSandboxSelection_Precedence(t *testing.T) {
	defaults := captainconfig.SandboxDefaults{
		Default: "srt",
		Backends: map[string]captainconfig.SandboxBackend{
			"pool": {Kind: "srt", Options: map[string]any{"tier": "pool"}},
		},
	}

	t.Run("flag beats frontmatter", func(t *testing.T) {
		got, err := resolveSandboxSelection("none", &api.SandboxRef{Backend: "pool"}, defaults)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != registry.SandboxNone {
			t.Fatalf("kind = %q, want the flag's", got.Kind)
		}
	})

	t.Run("frontmatter beats the global default", func(t *testing.T) {
		got, err := resolveSandboxSelection("", &api.SandboxRef{Backend: "pool"}, defaults)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "pool" {
			t.Fatalf("selection = %+v, want the frontmatter backend", got)
		}
	})

	t.Run("global default applies when nothing else selects", func(t *testing.T) {
		got, err := resolveSandboxSelection("", nil, defaults)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != registry.SandboxSRT {
			t.Fatalf("kind = %q, want the default's", got.Kind)
		}
	})

	t.Run("everything empty resolves to none", func(t *testing.T) {
		got, err := resolveSandboxSelection("", nil, captainconfig.SandboxDefaults{})
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != registry.SandboxNone {
			t.Fatalf("kind = %q, want none", got.Kind)
		}
	})

	t.Run("an unwired kind fails loud instead of running unsandboxed", func(t *testing.T) {
		_, err := resolveSandboxSelection("git-agent", nil, defaults)
		if err == nil || !strings.Contains(err.Error(), "not wired to execution yet") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an unknown selector fails loud", func(t *testing.T) {
		_, err := resolveSandboxSelection("nope", nil, defaults)
		if err == nil || !strings.Contains(err.Error(), `unknown sandbox "nope"`) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSandboxSelector_LegacySpellings(t *testing.T) {
	for flag, want := range map[string]string{
		"true": "srt", "1": "srt", "yes": "srt",
		"false": "none", "0": "none", "no": "none",
		"prod-pool": "prod-pool", "": "",
	} {
		if got := (AIProviderOptions{Sandbox: flag}).SandboxSelector(); got != want {
			t.Errorf("SandboxSelector(%q) = %q, want %q", flag, got, want)
		}
	}
}
