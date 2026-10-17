// Package transcript resolves provider transcript candidates into Captain's
// unified session model.
package transcript

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
)

// Candidate identifies one provider transcript without exposing CLI wire types.
type Candidate struct {
	ID     string
	Source string
	Path   string
}

// Parse builds the unified model for one Claude or Codex transcript.
func Parse(candidate Candidate) (*session.Session, error) {
	if strings.TrimSpace(candidate.Path) == "" {
		return nil, fmt.Errorf("transcript path is required")
	}
	switch candidate.Source {
	case "claude":
		parsed, err := session.BuildClaudeFile(candidate.Path)
		if err != nil {
			return nil, fmt.Errorf("claude session %q: %w", candidate.Path, err)
		}
		return parsed, nil
	case api.TranscriptSourceAPI:
		parsed, _, err := session.BuildTranscriptFile(candidate.Path)
		if err != nil {
			return nil, fmt.Errorf("api session %q: %w", candidate.Path, err)
		}
		return parsed, nil
	case "codex":
		parsed, err := session.BuildCodexFile(candidate.Path)
		if err != nil {
			return nil, fmt.Errorf("codex session %q: %w", candidate.Path, err)
		}
		return parsed, nil
	default:
		return nil, fmt.Errorf("unknown session source %q", candidate.Source)
	}
}
