package cli

import (
	"path/filepath"
	"strings"
)

// sessionRecordMatchesProject reports whether a session's cwd falls under the
// project root (the record is in scope for the current project).
func sessionRecordMatchesProject(record SessionRecord, projectRoot string) bool {
	if projectRoot == "" {
		return true
	}
	cwd := record.CWD
	if cwd == "" {
		return false
	}
	rel, err := filepath.Rel(projectRoot, cwd)
	if err == nil && (rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")) {
		return true
	}
	return strings.HasPrefix(cwd, projectRoot+string(filepath.Separator))
}
