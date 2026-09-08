// Package transcript resolves provider transcript candidates into Captain's
// unified session model.
package transcript

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/claude"
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
		return parseClaude(candidate)
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

func parseClaude(candidate Candidate) (*session.Session, error) {
	id := strings.TrimSpace(candidate.ID)
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(candidate.Path), filepath.Ext(candidate.Path))
	}
	sessions, err := session.Build("", true, claude.Filter{
		SessionIDs:    []string{id},
		KeepRaw:       true,
		IncludeAgents: true,
	})
	if err != nil {
		return nil, err
	}
	for _, parsed := range sessions {
		if parsed.ID == id {
			return parsed, nil
		}
	}
	if len(sessions) > 0 {
		return sessions[0], nil
	}
	return nil, fmt.Errorf("session %q not found", id)
}
