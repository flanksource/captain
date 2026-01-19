package bash

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func paths(ops []FileOperation) []string {
	return lo.Map(ops, func(op FileOperation, _ int) string { return op.Path })
}

func TestAnalyze(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		created  []string
		modified []string
		deleted  []string
	}{
		{
			name:    "touch creates file",
			script:  "touch config.txt",
			created: []string{"config.txt"},
		},
		{
			name:    "redirect creates file",
			script:  `echo "hello" > output.txt`,
			created: []string{"output.txt"},
		},
		{
			name:     "append modifies file",
			script:   `echo "more" >> output.txt`,
			modified: []string{"output.txt"},
		},
		{
			name:    "rm deletes file",
			script:  "rm old.log",
			deleted: []string{"old.log"},
		},
		{
			name:    "rm with flags",
			script:  "rm -rf /tmp/dir",
			deleted: []string{"/tmp/dir"},
		},
		{
			name:    "mkdir creates directory",
			script:  "mkdir -p /tmp/newdir",
			created: []string{"/tmp/newdir"},
		},
		{
			name:    "cp creates destination",
			script:  "cp source.txt dest.txt",
			created: []string{"dest.txt"},
		},
		{
			name:    "mv deletes source creates dest",
			script:  "mv old.txt new.txt",
			created: []string{"new.txt"},
			deleted: []string{"old.txt"},
		},
		{
			name:     "chmod modifies file",
			script:   "chmod 644 file.txt",
			modified: []string{"file.txt"},
		},
		{
			name:     "chown modifies file",
			script:   "chown root:root file.txt",
			modified: []string{"file.txt"},
		},
		{
			name:    "tee creates file",
			script:  `echo "data" | tee output.log`,
			created: []string{"output.log"},
		},
		{
			name:     "tee -a modifies file",
			script:   `echo "data" | tee -a output.log`,
			modified: []string{"output.log"},
		},
		{
			name:     "sed -i modifies file",
			script:   `sed -i 's/old/new/g' file.txt`,
			modified: []string{"file.txt"},
		},
		{
			name:    "rmdir deletes directory",
			script:  "rmdir emptydir",
			deleted: []string{"emptydir"},
		},
		{
			name:    "variable in path",
			script:  `touch $HOME/file.txt`,
			created: []string{"$HOME/file.txt"},
		},
		{
			name:    "glob pattern",
			script:  "rm *.log",
			deleted: []string{"*.log"},
		},
		{
			name:     "complex script",
			script:   "touch config.txt\necho \"key=value\" >> config.txt\nchmod 644 config.txt\nrm -f old.log",
			created:  []string{"config.txt"},
			modified: []string{"config.txt", "config.txt"},
			deleted:  []string{"old.log"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Analyze(tt.script)
			require.NoError(t, err)

			assert.ElementsMatch(t, tt.created, paths(result.Created()), "created paths")
			assert.ElementsMatch(t, tt.modified, paths(result.Modified()), "modified paths")
			assert.ElementsMatch(t, tt.deleted, paths(result.Deleted()), "deleted paths")
		})
	}
}

func TestFileOperationFlags(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		hasGlob bool
		hasVar  bool
	}{
		{
			name:    "plain path",
			script:  "touch file.txt",
			hasGlob: false,
			hasVar:  false,
		},
		{
			name:    "glob pattern",
			script:  "rm *.log",
			hasGlob: true,
			hasVar:  false,
		},
		{
			name:    "variable",
			script:  "touch $HOME/file.txt",
			hasGlob: false,
			hasVar:  true,
		},
		{
			name:    "command substitution",
			script:  "touch $(pwd)/file.txt",
			hasGlob: false,
			hasVar:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Analyze(tt.script)
			require.NoError(t, err)
			require.Len(t, result.Operations, 1)

			op := result.Operations[0]
			assert.Equal(t, tt.hasGlob, op.HasGlob, "HasGlob")
			assert.Equal(t, tt.hasVar, op.HasVar, "HasVar")
		})
	}
}

func TestPaths(t *testing.T) {
	script := `
touch file.txt
echo "data" >> file.txt
chmod 644 file.txt
rm old.log
`
	result, err := Analyze(script)
	require.NoError(t, err)

	paths := result.Paths()
	assert.Equal(t, []string{"file.txt", "old.log"}, paths)
}
