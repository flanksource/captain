package claude

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/segmentio/encoding/json"
)

// ReadOptions controls optional reader behavior.
type ReadOptions struct {
	KeepRaw bool
}

// ReadHistoryFile reads all entries from a JSONL history file.
// It preserves raw JSONL lines for backwards compatibility.
func ReadHistoryFile(path string) ([]HistoryEntry, error) {
	return ReadHistoryFileWithOptions(path, ReadOptions{KeepRaw: true})
}

// ReadHistoryFileWithOptions reads all entries from a JSONL history file with
// caller-selected optional fields.
func ReadHistoryFileWithOptions(path string, opts ReadOptions) ([]HistoryEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ReadHistoryWithOptions(f, opts)
}

// ReadHistory reads all entries from a JSONL reader. It uses the same
// (type, subtype) dispatcher as ReadStreamJSON so that both stream-json
// input and on-disk session files surface the same set of synthetic
// rows (session init, hooks, result/turn summaries, etc.).
func ReadHistory(r io.Reader) ([]HistoryEntry, error) {
	return ReadHistoryWithOptions(r, ReadOptions{KeepRaw: true})
}

// ReadHistoryWithOptions reads Claude history JSONL with caller-selected
// optional fields.
func ReadHistoryWithOptions(r io.Reader, opts ReadOptions) ([]HistoryEntry, error) {
	return readJSONL(r, true, opts)
}

// ReadStreamJSON reads Claude Code stream-json JSONL.
//
// In addition to assistant messages, it surfaces session-lifecycle events
// (system/init, system/hook_started, system/hook_response, result/*) as
// synthetic assistant entries whose single tool_use content block carries the
// event fields. Downstream extraction (ExtractToolUses → tools.NewTool) turns
// these into history rows. Unrecognized (type, subtype) tuples are counted
// via RecordUnhandledStreamType and otherwise ignored — they never error.
func ReadStreamJSON(r io.Reader) ([]HistoryEntry, error) {
	return readJSONL(r, false, ReadOptions{KeepRaw: true})
}

// readJSONL is the shared scanner used by both ReadHistory and ReadStreamJSON.
// When fallbackToHistoryEntry is true, lines whose `type` is empty (no
// `type` field at all — the on-disk session file shape, where every line is
// implicitly a HistoryEntry) are unmarshaled directly into a HistoryEntry.
// Stream-json sets it false: every line carries a `type` and is routed by
// dispatchEvent.
func readJSONL(r io.Reader, fallbackToHistoryEntry bool, opts ReadOptions) ([]HistoryEntry, error) {
	var entries []HistoryEntry
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	lineNo := 0
	// lastTS tracks the most recent successfully-parsed line timestamp so a
	// synthetic ParseError row can inherit it — otherwise ParseError rows have no
	// timestamp, sort last, and are the first discarded by the row limit.
	lastTS := ""
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var sj streamJSONLine
		if err := json.Unmarshal(line, &sj); err != nil {
			entries = append(entries, parseErrorEntry(lineNo, line, err, lastTS))
			continue
		}

		if fallbackToHistoryEntry && sj.Type == "" {
			var entry HistoryEntry
			if err := json.Unmarshal(line, &entry); err != nil {
				entries = append(entries, parseErrorEntry(lineNo, line, err, lastTS))
				continue
			}
			if entry.Timestamp != "" {
				lastTS = entry.Timestamp
			}
			if opts.KeepRaw {
				entry.RawLine = append(json.RawMessage(nil), line...)
			}
			entries = append(entries, entry)
			continue
		}

		if sj.timestamp() != "" {
			lastTS = sj.timestamp()
		}
		for _, entry := range dispatchEvent(sj, line, lineNo) {
			if opts.KeepRaw {
				entry.RawLine = append(json.RawMessage(nil), line...)
			}
			entries = append(entries, entry)
		}
	}

	return entries, scanner.Err()
}

// streamJSONLine captures the discriminator fields shared by Claude Code
// stream-json lines and on-disk session-file lines. Both naming conventions
// (snake_case for stream-json, camelCase for session files) are recognized.
type streamJSONLine struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`

	// Stream-json fields
	SessionIDSnake string `json:"session_id,omitempty"`
	TimestampSnake string `json:"timestamp,omitempty"`

	// Session-file fields
	SessionIDCamel string          `json:"sessionId,omitempty"`
	UUID           string          `json:"uuid,omitempty"`
	Version        string          `json:"version,omitempty"`
	CWD            string          `json:"cwd,omitempty"`
	GitBranch      string          `json:"gitBranch,omitempty"`
	Slug           string          `json:"slug,omitempty"`
	Error          json.RawMessage `json:"error,omitempty"`
	Attachment     json.RawMessage `json:"attachment,omitempty"`
}

func (sj streamJSONLine) sessionID() string {
	if sj.SessionIDCamel != "" {
		return sj.SessionIDCamel
	}
	return sj.SessionIDSnake
}

func (sj streamJSONLine) timestamp() string {
	return sj.TimestampSnake
}

// knownSessionStorageTypes are line types that appear in on-disk session
// files for state tracking but carry no useful row-level information.
// Listed explicitly so they don't pollute the unhandled-types diagnostic.
var knownSessionStorageTypes = map[string]bool{
	"file-history-snapshot": true,
	"permission-mode":       true,
	"agent-name":            true,
	// Operational/streaming state with no unique row-level content — the real
	// content surfaces via the actual user/assistant messages. Listed so they
	// don't pollute the unhandled-types diagnostic.
	"mode":           true, // active mode marker (e.g. {"mode":"normal"})
	"bridge-session": true, // cloud bridge-session linkage
	"progress":       true, // intermediate streaming progress, superseded by the final message
}

// planAttachment is the plan-mode attachment Claude Code writes when entering
// ("plan_mode") and exiting ("plan_mode_exit") plan mode. It names the plan file
// even for sessions whose transcript carries no ExitPlanMode tool call.
type planAttachment struct {
	Type         string `json:"type"`
	PlanFilePath string `json:"planFilePath"`
}

// attachmentEntry surfaces plan-mode attachments as a synthetic, message-less
// HistoryEntry carrying the plan file path. Non-plan attachments are dropped.
func attachmentEntry(sj streamJSONLine) []HistoryEntry {
	if len(sj.Attachment) == 0 {
		return nil
	}
	var a planAttachment
	if err := json.Unmarshal(sj.Attachment, &a); err != nil {
		return nil
	}
	if !strings.HasPrefix(a.Type, "plan_mode") || a.PlanFilePath == "" {
		return nil
	}
	return single(HistoryEntry{
		SessionID:    sj.sessionID(),
		UUID:         sj.UUID,
		Timestamp:    sj.timestamp(),
		CWD:          sj.CWD,
		GitBranch:    sj.GitBranch,
		Slug:         sj.Slug,
		PlanFilePath: a.PlanFilePath,
	})
}

func metadataEventEntry(sj streamJSONLine, eventType, scope string, data map[string]any) []HistoryEntry {
	if eventType == "" {
		return nil
	}
	return single(HistoryEntry{
		SessionID: sj.sessionID(),
		UUID:      sj.UUID,
		Timestamp: sj.timestamp(),
		CWD:       sj.CWD,
		GitBranch: sj.GitBranch,
		Slug:      sj.Slug,
		Event: &TranscriptEvent{
			Type:  eventType,
			Scope: scope,
			Data:  data,
		},
	})
}

func rawObject(raw []byte) map[string]any {
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	delete(out, "message")
	return out
}

func attachmentEventEntry(sj streamJSONLine, raw []byte) []HistoryEntry {
	var attachment map[string]any
	if err := json.Unmarshal(sj.Attachment, &attachment); err != nil {
		return nil
	}
	typ, _ := attachment["type"].(string)
	switch typ {
	case "deferred_tools_delta", "agent_listing_delta", "skill_listing":
		return metadataEventEntry(sj, typ, "session", attachment)
	case "budget_usd":
		return metadataEventEntry(sj, typ, "turn", attachment)
	default:
		return attachmentEntry(sj)
	}
}

// dispatchEvent routes a typed line to zero, one, or more HistoryEntry rows.
// A single line can emit multiple entries — e.g. an assistant message with a
// top-level "error" field yields both the regular assistant entry and an
// extra ApiError synthetic row so the failure is visible in history output.
// Unknown types are recorded as unhandled.
func dispatchEvent(sj streamJSONLine, raw []byte, lineNo int) []HistoryEntry {
	switch sj.Type {
	case "assistant", "user":
		if len(sj.Message) == 0 {
			return nil
		}
		var msg Message
		if err := json.Unmarshal(sj.Message, &msg); err != nil {
			return []HistoryEntry{parseErrorEntry(lineNo, raw, err, sj.timestamp())}
		}
		out := []HistoryEntry{{
			SessionID: sj.sessionID(),
			UUID:      sj.UUID,
			Timestamp: sj.timestamp(),
			Version:   sj.Version,
			CWD:       sj.CWD,
			GitBranch: sj.GitBranch,
			Slug:      sj.Slug,
			Message:   msg,
		}}
		if errEntry, ok := apiErrorFromAssistantLine(sj, raw); ok {
			out = append(out, errEntry)
		}
		return out

	case "queue-operation":
		return metadataEventEntry(sj, "queue-operation", "turn", rawObject(raw))

	case "system":
		switch sj.Subtype {
		case "init":
			return single(syntheticEntry(sj, "SessionInit", raw, []string{
				"cwd", "model", "tools", "plugins", "slash_commands",
				"mcp_servers", "permissionMode", "apiKeySource",
				"claude_code_version",
			}))
		case "hook_started":
			return single(syntheticEntry(sj, "HookStart", raw, []string{
				"hook_name", "hook_event", "hook_id",
			}))
		case "hook_response":
			return single(syntheticEntry(sj, "HookResponse", raw, []string{
				"hook_name", "hook_event", "hook_id", "outcome",
				"exit_code", "stdout", "stderr", "output",
			}))
		case "stop_hook_summary":
			return single(syntheticEntry(sj, "StopHookSummary", raw, []string{
				"hookCount", "hookErrors", "hookInfos", "stopReason",
				"hasOutput", "preventedContinuation",
			}))
		case "turn_duration":
			return single(syntheticEntry(sj, "TurnDuration", raw, []string{
				"durationMs", "messageCount",
			}))
		case "away_summary":
			return single(syntheticEntry(sj, "AwaySummary", raw, []string{"content"}))
		case "compact_boundary":
			return single(syntheticEntry(sj, "CompactBoundary", raw, []string{
				"content", "compactMetadata", "level",
			}))
		case "local_command":
			return single(syntheticEntry(sj, "LocalCommand", raw, []string{"content", "level", "cwd"}))
		case "scheduled_task_fire":
			return single(syntheticEntry(sj, "ScheduledTaskFire", raw, []string{"content"}))
		case "informational":
			return single(syntheticEntry(sj, "Informational", raw, []string{"content", "level"}))
		case "api_error":
			return single(syntheticEntry(sj, "ApiError", raw, []string{
				"error", "level", "retryInMs", "retryAttempt", "maxRetries",
				"api_error_status",
			}))
		}

	case "result":
		return single(syntheticEntry(sj, "Result", raw, []string{
			"result", "is_error", "num_turns", "total_cost_usd",
			"duration_ms", "duration_api_ms", "stop_reason",
			"terminal_reason", "api_error_status", "modelUsage",
			"usage", "permission_denials", "error",
		}))

	case "ai-title":
		return single(syntheticEntry(sj, "SessionTitle", raw, []string{"aiTitle"}))

	case "pr-link":
		// Workflow state: links the session to a pull request.
		return single(syntheticEntry(sj, "PrLink", raw, []string{
			"prNumber", "prUrl", "prRepository",
		}))

	case "worktree-state":
		return single(syntheticEntry(sj, "WorktreeState", raw, []string{"worktreeSession"}))

	case "relocated":
		return single(syntheticEntry(sj, "Relocated", raw, []string{"relocatedCwd"}))

	case "started":
		return single(syntheticEntry(sj, "Started", raw, []string{"cwd"}))

	case "attachment":
		return attachmentEventEntry(sj, raw)

	case "last-prompt":
		return metadataEventEntry(sj, "last-prompt", "session", rawObject(raw))
	}

	if knownSessionStorageTypes[sj.Type] {
		// Recognized but intentionally not surfaced.
		return nil
	}

	key := sj.Type
	if sj.Subtype != "" {
		key = sj.Type + "/" + sj.Subtype
	}
	RecordUnhandledStreamType(key)
	return nil
}

func single(e HistoryEntry) []HistoryEntry { return []HistoryEntry{e} }

// apiErrorFromAssistantLine yields an ApiError row when the assistant line
// carries a top-level "error" field (e.g. invalid_request, 4xx/5xx from the
// model API). The error would otherwise be invisible in history output.
func apiErrorFromAssistantLine(sj streamJSONLine, raw []byte) (HistoryEntry, bool) {
	if len(sj.Error) == 0 || string(sj.Error) == "null" {
		return HistoryEntry{}, false
	}
	return syntheticEntry(sj, "ApiError", raw, []string{
		"error", "api_error_status", "stop_reason", "terminal_reason",
	}), true
}

// parseErrorEntry builds a synthetic ParseError row for a line that failed
// to unmarshal. The line content is truncated to keep the row scannable. ts is
// the surrounding lines' timestamp (RFC3339), inherited so the row sorts among
// its neighbors rather than last.
func parseErrorEntry(lineNo int, raw []byte, err error, ts string) HistoryEntry {
	preview := string(raw)
	if len(preview) > 200 {
		preview = preview[:197] + "..."
	}
	input := map[string]any{
		"line":  lineNo,
		"error": err.Error(),
		"raw":   preview,
	}
	inputJSON, _ := json.Marshal(input)
	return HistoryEntry{
		Timestamp: ts,
		Message: Message{
			Role: MessageRoleAssistant,
			Content: []ContentBlock{{
				Type:  ContentTypeToolUse,
				ID:    fmt.Sprintf("parse-error-%d", lineNo),
				Name:  "ParseError",
				Input: inputJSON,
			}},
		},
	}
}

// syntheticEntry builds a HistoryEntry whose Message contains a single
// tool_use ContentBlock for a non-tool-use event. The named keys are
// extracted from the raw JSON line into the tool input map.
func syntheticEntry(sj streamJSONLine, toolName string, raw []byte, keys []string) HistoryEntry {
	var full map[string]any
	_ = json.Unmarshal(raw, &full)

	input := make(map[string]any, len(keys))
	for _, k := range keys {
		if v, ok := full[k]; ok {
			input[k] = v
		}
	}
	inputJSON, _ := json.Marshal(input)

	id := sj.UUID
	if id == "" {
		id = sj.Type + "/" + sj.Subtype
	}

	return HistoryEntry{
		SessionID: sj.sessionID(),
		UUID:      sj.UUID,
		Timestamp: sj.timestamp(),
		Message: Message{
			Role: MessageRoleAssistant,
			Content: []ContentBlock{{
				Type:  ContentTypeToolUse,
				ID:    id,
				Name:  toolName,
				Input: inputJSON,
			}},
		},
	}
}

// HistoryIterator provides streaming access to JSONL history
type HistoryIterator struct {
	scanner *bufio.Scanner
	current HistoryEntry
	err     error
}

// NewHistoryIterator creates an iterator for streaming large files
func NewHistoryIterator(r io.Reader) *HistoryIterator {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	return &HistoryIterator{scanner: scanner}
}

// Next advances to the next entry, returns false when done or on error
func (it *HistoryIterator) Next() bool {
	for it.scanner.Scan() {
		line := it.scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		it.current = HistoryEntry{}
		if err := json.Unmarshal(line, &it.current); err != nil {
			it.err = err
			return false
		}
		it.current.RawLine = append(json.RawMessage(nil), line...)
		return true
	}

	it.err = it.scanner.Err()
	return false
}

// Entry returns the current entry
func (it *HistoryIterator) Entry() HistoryEntry {
	return it.current
}

// Err returns any error encountered during iteration
func (it *HistoryIterator) Err() error {
	return it.err
}

// StreamJSONIterator is the streaming counterpart to ReadStreamJSON: it reads
// Claude Code stream-json one line at a time and surfaces system/result/etc
// lines as synthetic tool_use entries via the same (type, subtype) dispatcher
// used by ReadStreamJSON. Use it when a caller needs live progress instead of
// buffering the whole stream.
type StreamJSONIterator struct {
	scanner *bufio.Scanner
	pending []HistoryEntry
	current HistoryEntry
	lineNo  int
	err     error
}

// NewStreamJSONIterator creates an iterator over Claude Code stream-json.
func NewStreamJSONIterator(r io.Reader) *StreamJSONIterator {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	return &StreamJSONIterator{scanner: scanner}
}

// Next advances to the next entry. A single stream-json line can dispatch to
// zero, one, or two HistoryEntry rows (e.g. an assistant line with a top-level
// `error` field yields the assistant message AND a synthetic ApiError row);
// the iterator buffers the extras so each call returns exactly one entry.
func (it *StreamJSONIterator) Next() bool {
	if len(it.pending) > 0 {
		it.current = it.pending[0]
		it.pending = it.pending[1:]
		return true
	}
	for it.scanner.Scan() {
		it.lineNo++
		line := it.scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var sj streamJSONLine
		if err := json.Unmarshal(line, &sj); err != nil {
			it.current = parseErrorEntry(it.lineNo, line, err, "")
			it.current.RawLine = append(json.RawMessage(nil), line...)
			return true
		}

		entries := dispatchEvent(sj, line, it.lineNo)
		if len(entries) == 0 {
			continue
		}
		for i := range entries {
			entries[i].RawLine = append(json.RawMessage(nil), line...)
		}
		it.current = entries[0]
		if len(entries) > 1 {
			it.pending = append(it.pending, entries[1:]...)
		}
		return true
	}

	it.err = it.scanner.Err()
	return false
}

// Entry returns the current entry.
func (it *StreamJSONIterator) Entry() HistoryEntry { return it.current }

// Err returns any error encountered during iteration.
func (it *StreamJSONIterator) Err() error { return it.err }
