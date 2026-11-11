package history

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/codexhome"
)

const (
	TranscriptSourceClaude = "claude"
	TranscriptSourceCodex  = "codex"
)

// TranscriptKind says whether a transcript is a session's own or one of its sub-agents'.
type TranscriptKind string

const (
	TranscriptRoot  TranscriptKind = "root"
	TranscriptAgent TranscriptKind = "agent"
)

// TranscriptQuery selects agent transcripts on disk.
type TranscriptQuery struct {
	// Home is the home directory the agents wrote under. Empty means the
	// user's: ~/.claude/projects plus every codex session root (CODEX_HOME and
	// captain's read-only home).
	Home string
	// ModifiedSince keeps only transcripts whose mtime is at or after it. Zero
	// keeps every transcript.
	ModifiedSince time.Time
}

// Transcript is one agent transcript file found on disk.
type Transcript struct {
	Source string
	Kind   TranscriptKind
	Path   string
	Info   fs.FileInfo
}

// FindTranscripts lists the Claude and codex transcripts the query selects.
// The ModifiedSince filter runs on file metadata only: no transcript is
// opened. Codex rollouts sit under the day their session started, so a
// resumed session is found by its mtime, not its folder.
func FindTranscripts(query TranscriptQuery) ([]Transcript, error) {
	projectsDir, codexRoots, err := transcriptRoots(query.Home)
	if err != nil {
		return nil, err
	}
	var found []Transcript
	for _, kind := range []TranscriptKind{TranscriptRoot, TranscriptAgent} {
		find := claude.FindSessionFiles
		if kind == TranscriptAgent {
			find = claude.FindAgentTranscripts
		}
		paths, err := find(projectsDir, "", true)
		if err != nil {
			return nil, fmt.Errorf("find claude %s transcripts under %s: %w", kind, projectsDir, err)
		}
		for _, path := range paths {
			info, err := os.Stat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if !info.ModTime().Before(query.ModifiedSince) {
				found = append(found, Transcript{Source: TranscriptSourceClaude, Kind: kind, Path: path, Info: info})
			}
		}
	}
	codex, err := walkCodexRollouts(codexRoots, query.ModifiedSince)
	if err != nil {
		return nil, err
	}
	return append(found, codex...), nil
}

func transcriptRoots(home string) (projectsDir string, codexRoots []string, err error) {
	if home != "" {
		return filepath.Join(home, ".claude", "projects"), []string{filepath.Join(home, ".codex", "sessions")}, nil
	}
	codexRoots, err = codexhome.SessionRoots()
	return claude.GetProjectsDir(), codexRoots, err
}

// walkCodexRollouts lists the *.jsonl rollouts under each root modified at or
// after since. A rollout that disappears mid-walk is skipped.
func walkCodexRollouts(roots []string, since time.Time) ([]Transcript, error) {
	var found []Transcript
	for _, root := range roots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			info, err := entry.Info()
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if !info.ModTime().Before(since) {
				found = append(found, Transcript{Source: TranscriptSourceCodex, Kind: TranscriptRoot, Path: path, Info: info})
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk codex sessions under %s: %w", root, err)
		}
	}
	return found, nil
}
