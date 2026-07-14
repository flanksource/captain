package codexconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var captainArgv = []string{"/usr/local/bin/captain", "hook", "monitor", "notify", "--provider", "codex"}

const captainNotifyLine = `notify = ["/usr/local/bin/captain","hook","monitor","notify","--provider","codex"]`

func useTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".codex", "config.toml")
	if content != "" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	SetPathForTesting(path)
	t.Cleanup(func() { SetPathForTesting("") })
	return path
}

func TestSetNotify(t *testing.T) {
	t.Run("creates a missing config file", func(t *testing.T) {
		path := useTempConfig(t, "")
		result, err := SetNotify(captainArgv)
		require.NoError(t, err)
		assert.Contains(t, result, "created")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, captainNotifyLine+"\n", string(data))
	})

	t.Run("inserts above the first table preserving comments", func(t *testing.T) {
		content := "# my codex config\nmodel = \"gpt-5\"\n\n[profiles.fast]\nmodel = \"gpt-5-mini\"\n"
		path := useTempConfig(t, content)
		_, err := SetNotify(captainArgv)
		require.NoError(t, err)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t,
			"# my codex config\nmodel = \"gpt-5\"\n\n"+captainNotifyLine+"\n\n[profiles.fast]\nmodel = \"gpt-5-mini\"\n",
			string(data))
	})

	t.Run("appends to a table-free config", func(t *testing.T) {
		path := useTempConfig(t, "model = \"gpt-5\"\n")
		_, err := SetNotify(captainArgv)
		require.NoError(t, err)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "model = \"gpt-5\"\n"+captainNotifyLine+"\n", string(data))
	})

	t.Run("existing captain notify is idempotent", func(t *testing.T) {
		path := useTempConfig(t, captainNotifyLine+"\n")
		result, err := SetNotify(captainArgv)
		require.NoError(t, err)
		assert.Contains(t, result, "already installed")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, captainNotifyLine+"\n", string(data))
	})

	t.Run("captain notify at another path is updated in place", func(t *testing.T) {
		path := useTempConfig(t, "# keep\nnotify = [\"/old/captain\",\"hook\",\"monitor\",\"notify\",\"--provider\",\"codex\"]\n")
		result, err := SetNotify(captainArgv)
		require.NoError(t, err)
		assert.Contains(t, result, "updated")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "# keep\n"+captainNotifyLine+"\n", string(data))
	})

	t.Run("foreign notify fails loudly", func(t *testing.T) {
		useTempConfig(t, "notify = [\"/usr/bin/terminal-notifier\"]\n")
		_, err := SetNotify(captainArgv)
		require.ErrorContains(t, err, "one notify program")
	})

	t.Run("invalid TOML fails loudly", func(t *testing.T) {
		useTempConfig(t, "model = [unclosed\n")
		_, err := SetNotify(captainArgv)
		require.ErrorContains(t, err, "parse")
	})

	t.Run("multi-line captain notify fails loudly", func(t *testing.T) {
		useTempConfig(t, "notify = [\n  \"/old/captain\", \"hook\", \"monitor\", \"notify\",\n]\n")
		_, err := SetNotify(captainArgv)
		require.ErrorContains(t, err, "manually")
	})

	t.Run("non-captain argv is refused", func(t *testing.T) {
		useTempConfig(t, "")
		_, err := SetNotify([]string{"/bin/echo"})
		require.ErrorContains(t, err, "non-captain")
	})
}
