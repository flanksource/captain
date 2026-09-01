package captainconfig

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api/registry"
	"gopkg.in/yaml.v3"
)

func TestSandboxDefaults_Resolve(t *testing.T) {
	defaults := SandboxDefaults{
		Default: "prod-pool",
		Backends: map[string]SandboxBackend{
			"prod-pool":    {Kind: "git-agent", Options: map[string]any{"relay": "sync"}},
			"local-docker": {Kind: "docker"},
			"broken":       {Kind: "warp-drive"},
		},
	}

	t.Run("resolves a configured backend with its options", func(t *testing.T) {
		got, err := defaults.Resolve("prod-pool")
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != registry.SandboxGitAgent || got.Name != "prod-pool" || got.Options["relay"] != "sync" {
			t.Fatalf("selection = %+v", got)
		}
	})

	t.Run("resolves a bare kind with no backend name", func(t *testing.T) {
		got, err := defaults.Resolve("native")
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != registry.SandboxNative || got.Name != "" || got.Options != nil {
			t.Fatalf("selection = %+v", got)
		}
	})

	t.Run("empty selector falls back to the default", func(t *testing.T) {
		got, err := defaults.Resolve("")
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "prod-pool" {
			t.Fatalf("selection = %+v, want the configured default", got)
		}
	})

	t.Run("empty selector and empty default resolve to off", func(t *testing.T) {
		got, err := SandboxDefaults{}.Resolve("")
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != registry.SandboxOff {
			t.Fatalf("kind = %q, want off", got.Kind)
		}
	})

	t.Run("unknown selector fails loud", func(t *testing.T) {
		_, err := defaults.Resolve("nope")
		if err == nil || !strings.Contains(err.Error(), `unknown sandbox "nope"`) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("configured backend with an invalid kind fails loud", func(t *testing.T) {
		_, err := defaults.Resolve("broken")
		if err == nil || !strings.Contains(err.Error(), `invalid kind "warp-drive"`) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSandboxBackend_InlineOptions(t *testing.T) {
	raw := `
default: prod-pool
backends:
  prod-pool:
    kind: git-agent
    relay: sync
    mailboxRoot: ~/.captain/sandbox/repos
  local-docker:
    kind: docker
    presets: [golang, git]
`
	var defaults SandboxDefaults
	if err := yaml.Unmarshal([]byte(raw), &defaults); err != nil {
		t.Fatal(err)
	}
	pool := defaults.Backends["prod-pool"]
	if pool.Kind != "git-agent" || pool.Options["relay"] != "sync" {
		t.Fatalf("prod-pool = %+v: kind-specific keys must land in Options verbatim", pool)
	}
	presets, ok := defaults.Backends["local-docker"].Options["presets"].([]any)
	if !ok || len(presets) != 2 {
		t.Fatalf("local-docker options = %+v", defaults.Backends["local-docker"].Options)
	}
}

func TestSandboxDefaults_OmittedWhenEmpty(t *testing.T) {
	encoded, err := yaml.Marshal(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sandbox") {
		t.Fatalf("empty sandbox block must be omitted from the config file, got:\n%s", encoded)
	}
}

func TestSandboxDefaults_MissingKindFailsLoud(t *testing.T) {
	defaults := SandboxDefaults{Backends: map[string]SandboxBackend{
		"pool": {Options: map[string]any{"image": "img"}}, // kind forgotten
	}}
	_, err := defaults.Resolve("pool")
	if err == nil || !strings.Contains(err.Error(), `declares no kind`) {
		t.Fatalf("err = %v, want missing-kind rejection (empty must NOT mean none)", err)
	}
}
