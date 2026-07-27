// Package assistanttags parses structured envelopes emitted in assistant text.
package assistanttags

import "strings"

const (
	proposedPlanOpen     = "<proposed_plan>"
	proposedPlanClose    = "</proposed_plan>"
	memoryCitationOpen   = "<oai-mem-citation>"
	memoryCitationClose  = "</oai-mem-citation>"
	citationEntriesOpen  = "<citation_entries>"
	citationEntriesClose = "</citation_entries>"
	rolloutIDsOpen       = "<rollout_ids>"
	rolloutIDsClose      = "</rollout_ids>"
)

// SegmentKind identifies one normalized portion of an assistant message.
type SegmentKind string

const (
	SegmentText           SegmentKind = "text"
	SegmentPlan           SegmentKind = "plan"
	SegmentMemoryCitation SegmentKind = "memory_citation"
)

// MemoryCitation is the structured payload carried by oai-mem-citation.
type MemoryCitation struct {
	CitationEntries []string `json:"citation_entries,omitempty"`
	RolloutIDs      []string `json:"rollout_ids,omitempty"`
}

// Segment is one ordered portion of a structured assistant message.
type Segment struct {
	Kind     SegmentKind
	Text     string
	Citation *MemoryCitation
}

// Parse splits the two assistant protocols observed in Claude/Codex history:
// a leading proposed_plan and a trailing oai-mem-citation. Unknown, embedded,
// or malformed XML remains ordinary text so source/code examples are safe.
func Parse(text string) []Segment {
	remaining := strings.TrimSpace(text)
	if remaining == "" {
		return nil
	}

	segments := make([]Segment, 0, 3)
	if strings.HasPrefix(remaining, proposedPlanOpen) {
		bodyStart := len(proposedPlanOpen)
		if closeAt := strings.Index(remaining[bodyStart:], proposedPlanClose); closeAt >= 0 {
			closeAt += bodyStart
			body := strings.TrimSpace(remaining[bodyStart:closeAt])
			if body != "" {
				segments = append(segments, Segment{Kind: SegmentPlan, Text: body})
			}
			remaining = strings.TrimSpace(remaining[closeAt+len(proposedPlanClose):])
		}
	}

	if remaining == "" {
		return segments
	}

	if start := strings.LastIndex(remaining, memoryCitationOpen); start >= 0 {
		block := strings.TrimSpace(remaining[start:])
		if citation, ok := parseMemoryCitation(block); ok {
			if prefix := strings.TrimSpace(remaining[:start]); prefix != "" {
				segments = append(segments, Segment{Kind: SegmentText, Text: prefix})
			}
			segments = append(segments, Segment{Kind: SegmentMemoryCitation, Citation: citation})
			return segments
		}
	}

	segments = append(segments, Segment{Kind: SegmentText, Text: remaining})
	return segments
}

func parseMemoryCitation(block string) (*MemoryCitation, bool) {
	if !strings.HasPrefix(block, memoryCitationOpen) || !strings.HasSuffix(block, memoryCitationClose) {
		return nil, false
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(block, memoryCitationOpen), memoryCitationClose))
	entries, ok := sectionLines(body, citationEntriesOpen, citationEntriesClose)
	if !ok {
		return nil, false
	}
	rolloutIDs, ok := sectionLines(body, rolloutIDsOpen, rolloutIDsClose)
	if !ok {
		return nil, false
	}
	return &MemoryCitation{CitationEntries: entries, RolloutIDs: rolloutIDs}, true
}

func sectionLines(body, open, close string) ([]string, bool) {
	start := strings.Index(body, open)
	if start < 0 {
		return nil, false
	}
	start += len(open)
	end := strings.Index(body[start:], close)
	if end < 0 {
		return nil, false
	}
	end += start
	var lines []string
	for _, line := range strings.Split(body[start:end], "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, true
}
