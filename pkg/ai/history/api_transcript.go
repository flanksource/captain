package history

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// APITranscriptRoot is the directory Captain writes API-mode run transcripts
// into. It is Captain's own, not ~/.claude/projects: a transcript written there
// would surface as a Claude Code session in Claude Code's own resume history.
func APITranscriptRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve API transcript root: %w", err)
	}
	return filepath.Join(home, ".captain", "transcripts", "api"), nil
}

// APITranscriptPath is the transcript file of one API-mode session.
func APITranscriptPath(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || sessionID != filepath.Base(sessionID) || strings.HasPrefix(sessionID, ".") {
		return "", fmt.Errorf("invalid API transcript session id %q", sessionID)
	}
	root, err := APITranscriptRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, sessionID+".jsonl"), nil
}
