package claude

import (
	"github.com/segmentio/encoding/json"
	"path/filepath"
	"strings"
)

// GetPlansDir returns the directory where Claude Code stores exit-plan-mode
// plan files (~/.claude/plans).
func GetPlansDir() string {
	return filepath.Join(GetClaudeHome(), "plans")
}

// SessionPlan is the exit-plan-mode plan recovered from a Claude session
// transcript.
type SessionPlan struct {
	// Path is the absolute plan file path (~/.claude/plans/<slug>.md).
	Path string
	// Slug is the session/plan slug.
	Slug string
	// Content is the most recent plan markdown captured inline from the
	// transcript (the ExitPlanMode plan field or a write to the plan file).
	// Callers should prefer the on-disk file when it exists, as the agent may
	// have revised it after exiting plan mode.
	Content string
	// Explicit reports whether the session produced a plan signal — an
	// ExitPlanMode call, a write to the plans directory, or a plan-mode
	// attachment. When false the path was derived only from the session slug,
	// which every session carries regardless of whether it ever planned.
	Explicit bool
}

// PlanFromEntries recovers the plan associated with a Claude session from its
// history entries. It returns nil when no plan path can be determined.
//
// The path is taken from the ExitPlanMode tool's planFilePath when present,
// otherwise from a write to ~/.claude/plans, otherwise from a plan-mode
// attachment, otherwise derived from the session slug. When only the slug is
// available, Explicit is false and callers should confirm the plan exists on
// disk before treating the session as having a plan.
func PlanFromEntries(entries []HistoryEntry) *SessionPlan {
	var slug, path, content string
	explicit := false

	for _, entry := range entries {
		if entry.Slug != "" {
			slug = entry.Slug
		}
		if entry.PlanFilePath != "" {
			path = entry.PlanFilePath
			explicit = true
		}
		for _, block := range entry.Message.Content {
			if block.Type != ContentTypeToolUse {
				continue
			}
			input := decodeBlockInput(block.Input)
			switch block.Name {
			case "ExitPlanMode":
				explicit = true
				if p := mapString(input, "planFilePath"); p != "" {
					path = p
				}
				if c := mapString(input, "plan"); c != "" {
					content = c
				}
			case "Write", "Edit":
				fp := mapString(input, "file_path")
				if !isPlanPath(fp) {
					continue
				}
				explicit = true
				path = fp
				// Write carries the full plan; Edit only a fragment, so let the
				// on-disk file (which an Edit implies exists) be the content source.
				if block.Name == "Write" {
					if c := mapString(input, "content"); c != "" {
						content = c
					}
				}
			}
		}
	}

	if path == "" && slug != "" {
		path = filepath.Join(GetPlansDir(), slug+".md")
	}
	if path == "" {
		return nil
	}
	return &SessionPlan{Path: path, Slug: slug, Content: content, Explicit: explicit}
}

// isPlanPath reports whether a file path lives in a Claude Code plans directory.
func isPlanPath(path string) bool {
	return path != "" && strings.Contains(filepath.ToSlash(path), "/.claude/plans/")
}

func decodeBlockInput(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func mapString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
