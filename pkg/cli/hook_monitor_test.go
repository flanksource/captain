package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/codexconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunHookMonitorInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexPath := filepath.Join(home, ".codex", "config.toml")
	codexconfig.SetPathForTesting(codexPath)
	t.Cleanup(func() { codexconfig.SetPathForTesting("") })

	readHookEvents := func() map[string][]any {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
		require.NoError(t, err)
		var settings struct {
			Hooks map[string][]any `json:"hooks"`
		}
		require.NoError(t, json.Unmarshal(data, &settings))
		return settings.Hooks
	}

	_, err := RunHookMonitorInstall(HookMonitorInstallOptions{Timeout: 10})
	require.NoError(t, err)

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	hooks := readHookEvents()
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SubagentStop", "SessionEnd"} {
		assert.Len(t, hooks[event], 1, "event %s must be installed", event)
	}
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))
	assert.NotContains(t, settings, "statusLine", "cost capture must be opt-in")

	codexData, err := os.ReadFile(codexPath)
	require.NoError(t, err)
	assert.Contains(t, string(codexData), `"hook","monitor","notify","--provider","codex"`)

	t.Run("second install is idempotent", func(t *testing.T) {
		_, err := RunHookMonitorInstall(HookMonitorInstallOptions{Timeout: 10})
		require.NoError(t, err)
		hooks := readHookEvents()
		for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SubagentStop", "SessionEnd"} {
			assert.Len(t, hooks[event], 1, "event %s must not be duplicated", event)
		}
	})

	t.Run("capture cost composes an existing status line", func(t *testing.T) {
		data, err := os.ReadFile(settingsPath)
		require.NoError(t, err)
		var settings map[string]any
		require.NoError(t, json.Unmarshal(data, &settings))
		settings["statusLine"] = map[string]any{"type": "command", "command": "jq -r .cwd"}
		data, err = json.MarshalIndent(settings, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(settingsPath, append(data, '\n'), 0o644))

		_, err = RunHookMonitorInstall(HookMonitorInstallOptions{Timeout: 10, CaptureCost: true})
		require.NoError(t, err)
		data, err = os.ReadFile(settingsPath)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &settings))
		statusLine := settings["statusLine"].(map[string]any)
		command := statusLine["command"].(string)
		assert.Contains(t, command, "hook monitor statusline")
		assert.Contains(t, command, "| (jq -r .cwd)")
	})

	for _, filename := range []string{"settings.json", "settings.local.json"} {
		t.Run("capture cost rejects higher precedence "+filename, func(t *testing.T) {
			project := t.TempDir()
			t.Chdir(project)
			require.NoError(t, os.WriteFile(filepath.Join(project, "go.mod"), []byte("module example.com/project\n"), 0o644))
			settingsDir := filepath.Join(project, ".claude")
			require.NoError(t, os.MkdirAll(settingsDir, 0o755))
			target := filepath.Join(settingsDir, filename)
			require.NoError(t, os.WriteFile(target, []byte(`{"statusLine":{"type":"command","command":"jq -r .cwd"}}`), 0o644))

			_, err := RunHookMonitorInstall(HookMonitorInstallOptions{Timeout: 10, CaptureCost: true})
			require.ErrorContains(t, err, target)
			require.ErrorContains(t, err, "higher-precedence Claude statusLine")
		})
	}
}
