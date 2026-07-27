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

	hooks := readHookEvents()
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SubagentStop", "SessionEnd"} {
		assert.Len(t, hooks[event], 1, "event %s must be installed", event)
	}

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
}
