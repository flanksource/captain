package bash

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPathClassifier_ClassifyPath(t *testing.T) {
	tests := []struct {
		name     string
		cwd      string
		path     string
		wantSafe bool
	}{
		{
			name:     "relative path in CWD is safe",
			cwd:      "/Users/test/project",
			path:     "./file.txt",
			wantSafe: true,
		},
		{
			name:     "subdir relative path in CWD is safe",
			cwd:      "/Users/test/project",
			path:     "subdir/file.txt",
			wantSafe: true,
		},
		{
			name:     "/tmp path is safe",
			cwd:      "/Users/test/project",
			path:     "/tmp/output.txt",
			wantSafe: true,
		},
		{
			name:     "/dev/null is safe",
			cwd:      "/Users/test/project",
			path:     "/dev/null",
			wantSafe: true,
		},
		{
			name:     "parent traversal is unsafe",
			cwd:      "/Users/test/project",
			path:     "../file.txt",
			wantSafe: false,
		},
		{
			name:     "/etc path is unsafe (system)",
			cwd:      "/Users/test/project",
			path:     "/etc/hosts",
			wantSafe: false,
		},
		{
			name:     "/usr path is unsafe (system)",
			cwd:      "/Users/test/project",
			path:     "/usr/bin/something",
			wantSafe: false,
		},
		{
			name:     "/var path is unsafe (system)",
			cwd:      "/Users/test/project",
			path:     "/var/log/app.log",
			wantSafe: false,
		},
		{
			name:     "absolute path within CWD is safe",
			cwd:      "/Users/test/project",
			path:     "/Users/test/project/file.txt",
			wantSafe: true,
		},
		{
			name:     "absolute path outside CWD is unsafe",
			cwd:      "/Users/test/project",
			path:     "/Users/other/file.txt",
			wantSafe: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pc := NewPathClassifier(tt.cwd, nil)
			got := pc.ClassifyPath(tt.path)
			assert.Equal(t, tt.wantSafe, got.IsSafe)
		})
	}
}

func TestPathClassifier_WithConfig(t *testing.T) {
	config := &Config{
		SafePaths: []string{"/opt/myapp/*"},
	}

	pc := NewPathClassifier("/Users/test/project", config)
	result := pc.ClassifyPath("/opt/myapp/data.txt")
	assert.True(t, result.IsSafe)
}

func TestIsDynamicPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{
			name: "literal path is not dynamic",
			path: "/tmp/file.txt",
			want: false,
		},
		{
			name: "$HOME is not considered dynamic (handled separately)",
			path: "$HOME/file.txt",
			want: false,
		},
		{
			name: "$VAR is dynamic",
			path: "$VAR/file.txt",
			want: true,
		},
		{
			name: "command substitution is dynamic",
			path: "$(dirname foo)/file.txt",
			want: true,
		},
		{
			name: "backtick substitution is dynamic",
			path: "`pwd`/file.txt",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsDynamicPath(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}
