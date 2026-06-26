package cli

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/flanksource/captain/pkg/claude"
)

type ChangesOptions struct {
	SessionID string    `flag:"session-id" help:"Session ID (exact or prefix) to list modified files for; defaults to the most recent session in the current directory" short:"s"`
	Since     time.Time `flag:"since" help:"Only consider sessions after this time" default:"now-30d"`
	All       bool      `flag:"all" help:"Search all projects, not just the current directory" short:"a"`
	Claude    bool      `flag:"claude" help:"Only consider Claude sessions"`
	Codex     bool      `flag:"codex" help:"Only consider Codex sessions"`
	Agents    bool      `flag:"agents" help:"Include files edited by nested sub-agents (Task/Agent); --agents=false for the main thread only" default:"true"`
	Plans     bool      `flag:"plans" help:"Include changes to plan files (~/.claude/plans); --plans=true to show them"`
	Ignored   bool      `flag:"ignored" help:"Include gitignored / out-of-repo files; --ignored=true to show them"`
}

// ChangedFile describes a single file modified during a session.
type ChangedFile struct {
	Path  string `json:"path" pretty:"label=File,table"`
	Edits int    `json:"edits" pretty:"label=Edits,table"`
	Tools string `json:"tools" pretty:"label=Tools,table"`
	Agent string `json:"agent,omitempty" pretty:"label=Agent,table"`
	Last  string `json:"last,omitempty" pretty:"label=Last Modified,table"`
}

// ChangesResult lists the files modified by a single session.
type ChangesResult struct {
	SessionID string        `json:"sessionId" pretty:"label=Session"`
	Source    string        `json:"source,omitempty" pretty:"label=Source"`
	FileCount int           `json:"fileCount" pretty:"label=Files Modified"`
	Files     []ChangedFile `json:"files"`
}

func RunChanges(opts ChangesOptions) (any, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	showClaude := opts.Claude || (!opts.Claude && !opts.Codex)
	showCodex := opts.Codex || (!opts.Claude && !opts.Codex)

	filter := claude.Filter{
		Since:         &opts.Since,
		SessionID:     opts.SessionID,
		IncludeAgents: opts.Agents,
	}

	uses, err := gatherToolUses(cwd, opts.All, showClaude, showCodex, filter)
	if err != nil {
		return nil, err
	}
	if len(uses) == 0 {
		if opts.SessionID != "" {
			return nil, fmt.Errorf("no session matching %q found in %s", opts.SessionID, scopeLabel(cwd, opts.All))
		}
		return nil, fmt.Errorf("no sessions found in %s", scopeLabel(cwd, opts.All))
	}

	sessionID := mostRecentSession(uses)
	sessionUses := filterBySession(uses, sessionID)

	result := buildChangesResult(sessionID, sessionUses)
	result.filter(newPathFilter(opts.Plans, opts.Ignored))
	return result, nil
}

func scopeLabel(cwd string, all bool) string {
	if all {
		return "any project"
	}
	return cwd
}

// mostRecentSession returns the session ID whose newest tool use is the latest.
func mostRecentSession(uses []claude.ToolUse) string {
	latest := make(map[string]time.Time)
	for _, tu := range uses {
		if tu.SessionID == "" || tu.Timestamp == nil {
			continue
		}
		if tu.Timestamp.After(latest[tu.SessionID]) {
			latest[tu.SessionID] = *tu.Timestamp
		}
	}

	var best string
	var bestTime time.Time
	for id, t := range latest {
		if best == "" || t.After(bestTime) {
			best, bestTime = id, t
		}
	}
	if best == "" && len(uses) > 0 {
		best = uses[0].SessionID
	}
	return best
}

func filterBySession(uses []claude.ToolUse, sessionID string) []claude.ToolUse {
	var out []claude.ToolUse
	for _, tu := range uses {
		if tu.SessionID == sessionID {
			out = append(out, tu)
		}
	}
	return out
}

func buildChangesResult(sessionID string, uses []claude.ToolUse) ChangesResult {
	type agg struct {
		edits  int
		tools  map[string]struct{}
		agents map[string]struct{}
		last   *time.Time
	}
	files := make(map[string]*agg)
	source := ""

	for _, tu := range uses {
		if source == "" {
			source = tu.Source
		}
		projectRoot := tu.ProjectRoot
		if projectRoot == "" {
			projectRoot = claude.FindProjectRoot(tu.CWD)
		}
		analysis := AnalyzeToolUseLegacy(tu, projectRoot)
		for _, p := range analysis.WritePaths {
			a := files[p]
			if a == nil {
				a = &agg{tools: make(map[string]struct{}), agents: make(map[string]struct{})}
				files[p] = a
			}
			a.edits++
			a.tools[tu.DisplayTool()] = struct{}{}
			if label := agentLabel(tu); label != "" {
				a.agents[label] = struct{}{}
			}
			if tu.Timestamp != nil && (a.last == nil || tu.Timestamp.After(*a.last)) {
				a.last = tu.Timestamp
			}
		}
	}

	result := ChangesResult{
		SessionID: sessionID,
		Source:    source,
		FileCount: len(files),
		Files:     make([]ChangedFile, 0, len(files)),
	}
	for path, a := range files {
		row := ChangedFile{Path: path, Edits: a.edits, Tools: joinSorted(a.tools), Agent: joinSorted(a.agents)}
		if a.last != nil {
			row.Last = a.last.Format("2006-01-02 15:04")
		}
		result.Files = append(result.Files, row)
	}
	sort.Slice(result.Files, func(i, j int) bool {
		if result.Files[i].Edits != result.Files[j].Edits {
			return result.Files[i].Edits > result.Files[j].Edits
		}
		return result.Files[i].Path < result.Files[j].Path
	})
	return result
}

// agentLabel is the sub-agent attribution shown in the Agent column: the task
// description if known, else the agent type. Empty for main-thread edits.
func agentLabel(tu claude.ToolUse) string {
	if tu.AgentDesc != "" {
		return tu.AgentDesc
	}
	return tu.AgentType
}

// filter drops files hidden by the --plans/--ignored flags and recomputes the count.
func (r *ChangesResult) filter(pf *pathFilter) {
	kept := r.Files[:0]
	for _, f := range r.Files {
		if pf.keep(f.Path) {
			kept = append(kept, f)
		}
	}
	r.Files = kept
	r.FileCount = len(kept)
}

func joinSorted(set map[string]struct{}) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += k
	}
	return out
}
