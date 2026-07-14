package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
)

// fakeCaptainOnPath makes captainBinary() resolve deterministically in tests:
// the test binary is not captain-named, so PATH lookup finds this fake.
func fakeCaptainOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "captain")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv(api.MonitorHooksEnv, "")
	return path
}

func monitorHookEventNames() []string {
	names := make([]string, 0, len(monitorHookEvents))
	for _, event := range monitorHookEvents {
		names = append(names, string(event))
	}
	return names
}

func TestClaudeCLIMonitorHooksInjection(t *testing.T) {
	binary := fakeCaptainOnPath(t)
	req := ai.Request{Prompt: api.Prompt{User: "hi"}}

	args, cleanup, err := buildClaudeCLIArgs("claude-sonnet-5", req)
	if err != nil {
		t.Fatalf("buildClaudeCLIArgs: %v", err)
	}
	settingsPath := flagValue(t, args, "--settings")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read injected settings: %v", err)
	}
	var config claude.HooksConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse injected settings: %v", err)
	}
	for _, event := range monitorHookEventNames() {
		matchers := config.Hooks[claude.HookEventType(event)]
		if len(matchers) != 1 || len(matchers[0].Hooks) != 1 {
			t.Fatalf("event %s: expected exactly one hook, got %+v", event, matchers)
		}
		command := matchers[0].Hooks[0].Command
		if command != binary+" hook monitor notify --provider claude" {
			t.Fatalf("event %s: unexpected command %q", event, command)
		}
	}

	cleanup()
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup must remove the temp settings file")
	}

	t.Run("bare request suppresses injection", func(t *testing.T) {
		bare := req
		bare.Memory.Bare = true
		args, cleanup, err := buildClaudeCLIArgs("claude-sonnet-5", bare)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		assertNoFlag(t, args, "--settings")
	})

	t.Run("CAPTAIN_MONITOR_HOOKS=off suppresses injection", func(t *testing.T) {
		t.Setenv(api.MonitorHooksEnv, "off")
		args, cleanup, err := buildClaudeCLIArgs("claude-sonnet-5", req)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		assertNoFlag(t, args, "--settings")
	})
}

func TestCodexCLIMonitorHooksInjection(t *testing.T) {
	binary := fakeCaptainOnPath(t)
	req := ai.Request{Prompt: api.Prompt{User: "hi"}}

	args, cleanup, err := buildCodexCLIArgs("gpt-5", req)
	if err != nil {
		t.Fatalf("buildCodexCLIArgs: %v", err)
	}
	defer cleanup()

	notify := codexOverrideValue(t, args, "notify=")
	var argv []string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(notify, "notify=")), &argv); err != nil {
		t.Fatalf("notify override is not a JSON string array: %v", err)
	}
	want := []string{binary, "hook", "monitor", "notify", "--provider", "codex"}
	if len(argv) != len(want) {
		t.Fatalf("notify argv = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("notify argv = %v, want %v", argv, want)
		}
	}

	t.Run("CAPTAIN_MONITOR_HOOKS=off suppresses injection", func(t *testing.T) {
		t.Setenv(api.MonitorHooksEnv, "off")
		args, cleanup, err := buildCodexCLIArgs("gpt-5", req)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		for _, arg := range args {
			if strings.HasPrefix(arg, "notify=") {
				t.Fatalf("notify override must be suppressed, got args %v", args)
			}
		}
	})
}

// codexOverrideValue returns the -c override value with the given prefix.
func codexOverrideValue(t *testing.T, args []string, prefix string) string {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-c" && strings.HasPrefix(args[i+1], prefix) {
			return args[i+1]
		}
	}
	t.Fatalf("no -c %s… override in args %v", prefix, args)
	return ""
}

func assertNoFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, arg := range args {
		if arg == flag {
			t.Fatalf("args must not contain %s: %v", flag, args)
		}
	}
}
