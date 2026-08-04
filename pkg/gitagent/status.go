package gitagent

import (
	"context"
	"fmt"
	"strings"
)

// StatusEntry is one record of `git status --porcelain -z`.
type StatusEntry struct {
	X, Y byte   // staged / worktree status codes
	Path string // repo-relative, verbatim (the -z format does not quote)
}

// Unmerged reports whether the entry is in a conflict state.
func (e StatusEntry) Unmerged() bool {
	return e.X == 'U' || e.Y == 'U' ||
		(e.X == 'D' && e.Y == 'D') || (e.X == 'A' && e.Y == 'A')
}

// statusZ lists every dirty path in dir. --no-renames keeps records
// single-path (a staged rename arrives as its A and D halves, which is
// exactly the final state a tree-level snapshot needs), and -z emits paths
// verbatim where the human format would quote and escape them.
func statusZ(ctx context.Context, dir string, env []string) ([]StatusEntry, error) {
	out, err := runGitRaw(ctx, dir, env, nil,
		"status", "--porcelain", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return nil, err
	}
	return parseStatusZ(out)
}

func parseStatusZ(out string) ([]StatusEntry, error) {
	fields := strings.Split(out, "\x00")
	var entries []StatusEntry
	for i := 0; i < len(fields); i++ {
		record := fields[i]
		if record == "" {
			continue
		}
		if len(record) < 4 || record[2] != ' ' {
			return nil, fmt.Errorf("unparseable git status record %q", record)
		}
		entry := StatusEntry{X: record[0], Y: record[1], Path: record[3:]}
		entries = append(entries, entry)
		// Defensive: rename/copy records carry a second, source-path field.
		// --no-renames should prevent them, but skipping one silently would
		// misattribute the next record.
		if entry.X == 'R' || entry.X == 'C' || entry.Y == 'R' || entry.Y == 'C' {
			if i+1 < len(fields) && fields[i+1] != "" {
				i++
				entries = append(entries, StatusEntry{X: entry.X, Y: entry.Y, Path: fields[i]})
			}
		}
	}
	return entries, nil
}
