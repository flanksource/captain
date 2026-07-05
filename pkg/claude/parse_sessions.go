package claude

import (
	"sort"

	"github.com/flanksource/commons/logger"
)

var parseLog = logger.GetLogger("claude")

// ParsedTranscript is one JSONL transcript (a root session file or a nested
// sub-agent transcript) parsed into its ordered entries and agent-stamped tool
// uses. Sub-agent transcripts carry the agent identity recovered from the
// filename and the agent-<id>.meta.json sidecar.
type ParsedTranscript struct {
	Path      string
	IsAgent   bool
	AgentID   string
	AgentType string
	AgentDesc string
	Entries   []HistoryEntry
	ToolUses  []ToolUse
}

// ParsedSession groups a root session transcript with its sub-agent transcripts
// under one session id. Transcripts[0] is the root; the remainder are sub-agents
// discovered under <session>/subagents/. It is the input the pkg/session
// unified model is built from.
type ParsedSession struct {
	SessionID   string
	Transcripts []ParsedTranscript
}

// ParseSessions discovers the in-scope session transcripts (root + sub-agents
// when filter.IncludeAgents) and returns them grouped by root session id, each
// transcript parsed into entries + token/cost-bearing, agent-stamped tool uses.
//
// Unlike ParseHistory it does NOT flatten across sessions or drop per-transcript
// structure, so callers can reconstruct the agent hierarchy and message stream.
// Unreadable transcripts are logged at Warn and skipped (never silently
// dropped).
func ParseSessions(currentDir string, searchAll bool, filter Filter) ([]ParsedSession, error) {
	projectsDir := GetProjectsDir()

	rootFiles, err := FindSessionFiles(projectsDir, currentDir, searchAll)
	if err != nil {
		return nil, err
	}

	var agentFiles []string
	if filter.IncludeAgents {
		if agentFiles, err = FindAgentTranscripts(projectsDir, currentDir, searchAll); err != nil {
			return nil, err
		}
	}

	bySession := make(map[string]*ParsedSession)
	order := make([]string, 0)
	addTranscript := func(file string, isAgent bool) {
		id := sessionIDFromTranscriptPath(file)
		if !filter.MatchesSessionID(id) {
			return
		}
		entries, err := ReadHistoryFileWithOptions(file, ReadOptions{KeepRaw: filter.KeepRaw})
		if err != nil {
			parseLog.Warnf("skipping unreadable transcript %s: %v", file, err)
			return
		}
		if len(entries) == 0 {
			return
		}
		toolUses := stampToolUses(ExtractToolUsesWithTokens(entries), projectsDir, file)
		t := ParsedTranscript{
			Path:     file,
			IsAgent:  isAgent,
			Entries:  entries,
			ToolUses: toolUses,
		}
		if isAgent {
			t.AgentID = agentIDFromPath(file)
			t.AgentType, t.AgentDesc = readAgentMeta(file)
		}
		ps, ok := bySession[id]
		if !ok {
			ps = &ParsedSession{SessionID: id}
			bySession[id] = ps
			order = append(order, id)
		}
		ps.Transcripts = append(ps.Transcripts, t)
	}

	for _, file := range rootFiles {
		addTranscript(file, false)
	}
	for _, file := range agentFiles {
		addTranscript(file, true)
	}

	out := make([]ParsedSession, 0, len(order))
	for _, id := range order {
		ps := bySession[id]
		// Root transcripts first, then sub-agents, each group stable by path.
		sort.SliceStable(ps.Transcripts, func(i, j int) bool {
			if ps.Transcripts[i].IsAgent != ps.Transcripts[j].IsAgent {
				return !ps.Transcripts[i].IsAgent
			}
			return ps.Transcripts[i].Path < ps.Transcripts[j].Path
		})
		out = append(out, *ps)
	}
	return out, nil
}
