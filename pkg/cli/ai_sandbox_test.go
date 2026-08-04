package cli

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
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

// The prompt run/render action path decodes flags from a string map; --mode
// must survive that decoding, or an explicit "--mode api --sandbox container"
// silently resolves CLI mode instead of being rejected as a contradiction.
func TestActionFlags_ModeSandboxConflictRejected(t *testing.T) {
	isolateSavedAI(t)
	opts, err := actionFlagsToOptions(map[string]string{
		"model":   "gemini-3.5-flash",
		"mode":    "api",
		"sandbox": "container",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Mode != "api" {
		t.Fatalf("Mode = %q: --mode must survive actionFlagsToOptions", opts.Mode)
	}

	_, _, err = overlayCLI(baseFileReq(), ai.Config{}, opts)
	if err == nil || !strings.Contains(err.Error(), "requires cli mode") {
		t.Fatalf("err = %v, want the API-mode × container-sandbox contradiction rejected", err)
	}
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
