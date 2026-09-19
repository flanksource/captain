package rootcmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/clicky"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func mapKeys[T any](value map[string]T) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}

func TestDiagramsAnalyzeCommandEmitsExactWireShapeAndCounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagram.tsx")
	markdown := filepath.Join(dir, "notes.md")
	require.NoError(t, os.WriteFile(path, []byte(`<Diagram>{(id) => <Arrow from={id('a')} to={id('b')} />}</Diagram>`), 0o600))
	require.NoError(t, os.WriteFile(markdown, []byte("# Notes"), 0o600))

	root := &cobra.Command{Use: "captain"}
	RegisterDiagramsCommand(root)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"diagrams", "analyze", "--schema-version=1", "--", path, markdown})

	require.NoError(t, root.Execute())
	require.Empty(t, stderr.String())

	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &wire))
	require.ElementsMatch(t, []string{"schema_version", "files_analyzed", "diagrams_analyzed", "diagnostics"}, mapKeys(wire))
	require.JSONEq(t, "1", string(wire["schema_version"]))
	require.JSONEq(t, "2", string(wire["files_analyzed"]))
	require.JSONEq(t, "1", string(wire["diagrams_analyzed"]))

	var diagnostics []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(wire["diagnostics"], &diagnostics))
	require.Len(t, diagnostics, 2)
	require.ElementsMatch(t, []string{"file", "line", "column", "severity", "code", "message"}, mapKeys(diagnostics[0]))
	require.JSONEq(t, `"`+path+`"`, string(diagnostics[0]["file"]))
	require.Equal(t, 1, bytes.Count(stdout.Bytes(), []byte("\n")))
}

func TestDiagramsAnalyzeCommandRequiresSupportedSchemaAndFiles(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "schema flag", args: []string{"diagrams", "analyze", "file.tsx"}, want: "--schema-version is required"},
		{name: "schema value", args: []string{"diagrams", "analyze", "--schema-version=2", "file.tsx"}, want: "unsupported --schema-version 2"},
		{name: "file", args: []string{"diagrams", "analyze", "--schema-version=1"}, want: "requires at least 1 arg"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := &cobra.Command{Use: "captain", SilenceUsage: true, SilenceErrors: true}
			RegisterDiagramsCommand(root)
			root.SetArgs(test.args)
			err := root.Execute()
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestDiagramsCommandIsStandaloneAndLocalOnly(t *testing.T) {
	root := &cobra.Command{Use: "captain"}
	RegisterDiagramsCommand(root)
	group, _, err := root.Find([]string{"diagrams"})
	require.NoError(t, err)
	analyze, _, err := root.Find([]string{"diagrams", "analyze"})
	require.NoError(t, err)

	require.True(t, clicky.IsLocalOnly(group))
	require.True(t, IsStandalone(analyze))
	require.False(t, IsStandalone(root))
}
