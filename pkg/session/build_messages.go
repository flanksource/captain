package session

import (
	"sort"

	"github.com/flanksource/captain/pkg/ai/assistanttags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
)

// hierarchy is the intermediate result of walking a ParsedSession: the ordered
// message stream, the agent tree, and the flat agent index.
type hierarchy struct {
	messages []Message
	root     *Agent
	agents   []*Agent
}

// buildHierarchy converts a ParsedSession into its message stream and agent
// tree. The tree is derived from parentUuid links (a sub-agent's first entry
// points at the spawning tool call in its parent), falling back to the root
// session when the parent cannot be resolved — fixing the flat-attribution gap
// where grandchild agents were mis-attributed to the root.
func buildHierarchy(ps claude.ParsedSession, turnByEntry map[string]string) hierarchy {
	// uuid -> owning agent id ("" == root), so a sub-agent's parentUuid can be
	// resolved to the agent that produced the referenced entry.
	uuidOwner := map[string]string{}
	for _, t := range ps.Transcripts {
		owner := ""
		if t.IsAgent {
			owner = t.AgentID
		}
		for _, e := range t.Entries {
			if e.UUID != "" {
				uuidOwner[e.UUID] = owner
			}
		}
	}

	root := &Agent{ID: ps.SessionID, IsRoot: true}
	agents := []*Agent{root}
	byID := map[string]*Agent{"": root, ps.SessionID: root}

	var messages []Message
	for _, t := range ps.Transcripts {
		var node *Agent
		if t.IsAgent {
			node = &Agent{ID: t.AgentID, Type: t.AgentType, Desc: t.AgentDesc, HistoryFile: t.Path}
			node.ParentID = resolveParent(t, uuidOwner, ps.SessionID)
			byID[t.AgentID] = node
			agents = append(agents, node)
		} else {
			node = root
			if node.HistoryFile == "" {
				node.HistoryFile = t.Path
			}
		}

		costs := api.Costs{}
		for _, e := range t.Entries {
			if e.IsAssistantMessage() && e.Message.Usage != nil {
				costs = append(costs, CostFromUsage(e.Message.Usage, e.Message.Model))
			}
			if m, ok := entryToMessage(e, node.ID, turnByEntry[e.UUID]); ok {
				messages = append(messages, m)
			}
		}
		node.Cost = costs.Sum()
		node.Usage = usageFromCost(node.Cost)
	}

	// Link children now that every node exists.
	for _, a := range agents {
		if a.IsRoot {
			continue
		}
		parent := byID[a.ParentID]
		if parent == nil {
			parent = root
			a.ParentID = root.ID
		}
		parent.Children = append(parent.Children, a)
	}

	return hierarchy{messages: messages, root: root, agents: agents}
}

// resolveParent returns the agent id that spawned transcript t: the owner of the
// entry its first entry's parentUuid points at, or the root session id when
// unresolved or self-referential.
func resolveParent(t claude.ParsedTranscript, uuidOwner map[string]string, rootID string) string {
	for _, e := range t.Entries {
		if e.ParentUUID == "" {
			continue
		}
		if owner, ok := uuidOwner[e.ParentUUID]; ok && owner != t.AgentID {
			if owner == "" {
				return rootID
			}
			return owner
		}
		break
	}
	return rootID
}

// entryToMessage projects a transcript entry into a canonical Message. It
// returns ok=false for entries with no renderable content (e.g. bare
// state-tracking lines).
func entryToMessage(e claude.HistoryEntry, agentID, turnID string) (Message, bool) {
	parts := partsFromEntry(e)
	if len(parts) == 0 {
		return Message{}, false
	}
	m := Message{
		ID:         e.UUID,
		Role:       string(e.Message.Role),
		Parts:      parts,
		Provenance: provenanceFromEntry(e, agentID),
		AgentID:    agentID,
		TurnID:     turnID,
		SourceLine: int64(e.Line),
	}
	if len(e.RawLine) > 0 {
		m.Raw = e.RawLine
	}
	return m, true
}

func partsFromEntry(e claude.HistoryEntry) []Part {
	var parts []Part
	for _, b := range e.Message.Content {
		switch b.Type {
		case claude.ContentTypeText:
			if e.IsAssistantMessage() {
				for _, segment := range assistanttags.Parse(b.Text) {
					switch segment.Kind {
					case assistanttags.SegmentText:
						parts = append(parts, Part{Type: PartText, Text: segment.Text})
					case assistanttags.SegmentPlan:
						parts = append(parts, Part{
							Type:       PartTool,
							ToolName:   "Plan",
							ToolCallID: e.UUID + "-plan",
							State:      ToolStateInputAvailable,
							Input:      marshalInput(map[string]any{"content": segment.Text, "tag": "proposed_plan"}),
						})
					}
				}
			} else if b.Text != "" {
				parts = append(parts, Part{Type: PartText, Text: b.Text})
			}
		case claude.ContentTypeThinking, claude.ContentTypeRedactedThinking:
			if b.Thinking != "" {
				parts = append(parts, Part{Type: PartReasoning, Text: b.Thinking})
			}
		case claude.ContentTypeToolUse:
			parts = append(parts, Part{
				Type:       PartTool,
				ToolName:   b.Name,
				ToolCallID: b.ID,
				State:      ToolStateInputAvailable,
				Input:      b.Input,
			})
		case claude.ContentTypeToolResult:
			state := ToolStateOutputAvailable
			if b.IsError {
				state = ToolStateOutputError
			}
			parts = append(parts, Part{
				Type:       PartTool,
				ToolCallID: b.ToolUseID,
				State:      state,
				Output:     b.Content,
			})
		}
	}
	return parts
}

func provenanceFromEntry(e claude.HistoryEntry, agentID string) *Provenance {
	p := &Provenance{
		CWD:        e.CWD,
		Model:      e.Message.Model,
		GitBranch:  e.GitBranch,
		UUID:       e.UUID,
		ParentUUID: e.ParentUUID,
		SessionID:  e.SessionID,
		AgentID:    agentID,
	}
	if ts, err := e.ParseTimestamp(); err == nil && !ts.IsZero() {
		p.Timestamp = &ts
	}
	return p
}

// sortedUnique returns the sorted, de-duplicated non-empty subset of in.
func sortedUnique(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
