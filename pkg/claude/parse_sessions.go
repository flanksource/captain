package claude

import (
	"fmt"
	"path/filepath"
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

// ParseTranscript parses a single transcript file into a ParsedTranscript
// (entries + agent-stamped, token-bearing tool uses) and returns the root
// session id derived from the path. It is the single-file counterpart to
// ParseSessions, used by the persistent summary cache to (re)build one file.
func ParseTranscript(path string) (ParsedTranscript, string, error) {
	t, err := readParsedTranscript(path, isAgentTranscript(path), GetProjectsDir(), ReadOptions{KeepRaw: false})
	if err != nil {
		return ParsedTranscript{}, "", err
	}
	sessionID := sessionIDFromTranscriptPath(path)
	if !t.IsAgent {
		// Root sessions: prefer the in-file sessionId (authoritative) over the
		// filename-derived id.
		for _, e := range t.Entries {
			if e.SessionID != "" {
				sessionID = e.SessionID
				break
			}
		}
	}
	return t, sessionID, nil
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
		t, err := readParsedTranscript(file, isAgent, projectsDir, ReadOptions{KeepRaw: filter.KeepRaw})
		if err != nil {
			parseLog.Warnf("skipping unreadable transcript %s: %v", file, err)
			return
		}
		if len(t.Entries) == 0 {
			return
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
		sortTranscripts(ps.Transcripts)
		out = append(out, *ps)
	}
	return out, nil
}

// ParseSessionFile parses one root transcript and the sub-agent transcripts
// recorded beside it (<dir>/<session>/subagents/agent-*.jsonl). It is the
// path-scoped counterpart to ParseSessions for a caller that already knows the
// file: nothing else under the projects directory is read. A missing or
// unreadable root, or one with no entries, is an error; an unreadable
// sub-agent transcript is logged and skipped, as ParseSessions does.
func ParseSessionFile(path string, opts ReadOptions) (ParsedSession, error) {
	projectsDir := GetProjectsDir()
	root, err := readParsedTranscript(path, false, projectsDir, opts)
	if err != nil {
		return ParsedSession{}, err
	}
	if len(root.Entries) == 0 {
		return ParsedSession{}, fmt.Errorf("claude transcript %s has no entries", path)
	}
	id := sessionIDFromTranscriptPath(path)
	agentFiles, err := filepath.Glob(filepath.Join(filepath.Dir(path), id, "subagents", "agent-*.jsonl"))
	if err != nil {
		return ParsedSession{}, fmt.Errorf("find sub-agent transcripts of %s: %w", path, err)
	}
	ps := ParsedSession{SessionID: id, Transcripts: []ParsedTranscript{root}}
	for _, file := range agentFiles {
		agent, err := readParsedTranscript(file, true, projectsDir, opts)
		if err != nil {
			parseLog.Warnf("skipping unreadable transcript %s: %v", file, err)
			continue
		}
		if len(agent.Entries) > 0 {
			ps.Transcripts = append(ps.Transcripts, agent)
		}
	}
	sortTranscripts(ps.Transcripts)
	return ps, nil
}

// readParsedTranscript reads one transcript file into its entries and
// agent-stamped, token-bearing tool uses.
func readParsedTranscript(file string, isAgent bool, projectsDir string, opts ReadOptions) (ParsedTranscript, error) {
	entries, err := ReadHistoryFileWithOptions(file, opts)
	if err != nil {
		return ParsedTranscript{}, err
	}
	t := ParsedTranscript{
		Path:     file,
		IsAgent:  isAgent,
		Entries:  entries,
		ToolUses: stampToolUses(ExtractToolUsesWithTokens(entries), projectsDir, file),
	}
	if isAgent {
		t.AgentID = agentIDFromPath(file)
		t.AgentType, t.AgentDesc = readAgentMeta(file)
	}
	return t, nil
}

// sortTranscripts orders root transcripts first, then sub-agents, each group
// stable by path.
func sortTranscripts(transcripts []ParsedTranscript) {
	sort.SliceStable(transcripts, func(i, j int) bool {
		if transcripts[i].IsAgent != transcripts[j].IsAgent {
			return !transcripts[i].IsAgent
		}
		return transcripts[i].Path < transcripts[j].Path
	})
}
