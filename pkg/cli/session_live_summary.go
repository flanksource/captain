package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
	rpchttp "github.com/flanksource/clicky/rpc/http"
)

const (
	liveSummaryHeadLines = 16
	liveSummaryTailLines = 128
	liveSummaryTailBytes = 256 << 10
)

type liveSessionFile struct {
	source  string
	path    string
	modUnix int64
}

func discoverLiveSessionCandidates(ctx context.Context, cwd string, searchAll bool, source string, limit int) ([]sessionCandidate, error) {
	stopFind := rpchttp.Track(ctx, "find")
	files, err := discoverLiveSessionFiles(cwd, searchAll, source)
	stopFind()
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modUnix == files[j].modUnix {
			return files[i].path > files[j].path
		}
		return files[i].modUnix > files[j].modUnix
	})
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}

	refs := make([]sessionFileRef, len(files))
	for i, file := range files {
		refs[i] = sessionFileRef{source: file.source, path: file.path}
	}
	return summarizeSessionRefs(ctx, refs), nil
}

func discoverLiveSessionFiles(cwd string, searchAll bool, source string) ([]liveSessionFile, error) {
	var files []liveSessionFile
	if source == "all" || source == "claude" {
		claudeFiles, err := claude.FindSessionFiles(claude.GetProjectsDir(), cwd, searchAll)
		if err != nil {
			return nil, err
		}
		files = appendSessionFiles(files, "claude", claudeFiles)
	}
	if source == "all" || source == "codex" {
		codexFiles, err := history.FindCodexSessionFiles()
		if err != nil {
			return nil, err
		}
		matchRoot := cwd
		if cwd != "" {
			projectInfo := claude.FindProjectInfo(cwd)
			if projectInfo.Root != "" {
				matchRoot = projectInfo.Root
			}
		}
		for _, file := range codexFiles {
			if !searchAll {
				meta, err := history.ReadCodexSessionMeta(file)
				if err != nil || meta == nil || !codexMetaMatchesProject(meta, matchRoot) {
					continue
				}
			}
			files = appendSessionFiles(files, "codex", []string{file})
		}
	}
	return files, nil
}

func appendSessionFiles(out []liveSessionFile, source string, paths []string) []liveSessionFile {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		out = append(out, liveSessionFile{
			source:  source,
			path:    path,
			modUnix: info.ModTime().UnixNano(),
		})
	}
	return out
}

func summarizeClaudeSessionFileFast(file string) (SessionRecord, error) {
	lines, err := sampledJSONLLines(file)
	if err != nil {
		return SessionRecord{}, err
	}
	record := SessionRecord{
		Key:             sessionRecordKey("claude", file),
		ID:              sessionIDFromFile(file),
		Source:          "claude",
		DetailAvailable: true,
	}
	for _, line := range lines {
		var entry claudeSummaryLine
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if ts := parseSessionSummaryTime(entry.Timestamp); !ts.IsZero() {
			extendSessionRange(&record, ts)
		}
		if id := entry.sessionID(); id != "" {
			record.ID = id
		}
		if record.Version == "" && entry.Version != "" {
			record.Version = entry.Version
		}
		if record.GitBranch == "" && entry.GitBranch != "" {
			record.GitBranch = entry.GitBranch
		}
		if record.CWD == "" && entry.CWD != "" {
			record.CWD = entry.CWD
		}
		if entry.Message != nil {
			if record.Model == "" && entry.Message.Model != "" {
				record.Model = entry.Message.Model
			}
			hasText, toolCalls := summarizeClaudeContent(entry.Message.Content)
			if (entry.Message.Role == string(claude.MessageRoleUser) || entry.Message.Role == string(claude.MessageRoleAssistant)) && hasText {
				record.Messages++
			}
			record.ToolCalls += toolCalls
			applyClaudeUsageSummary(&record, entry.Message.Usage, entry.Message.Model)
		}
		if entry.Error != "" && entry.Message != nil && entry.Message.Role == string(claude.MessageRoleAssistant) {
			record.ToolCalls++
		}
		if claudeSyntheticToolLine(entry) {
			record.ToolCalls++
		}
		if entry.CompactMetadata.PostTokens > 0 {
			ensureContext(&record, defaultClaudeContextWindow)
			record.Context.UsedTokens = entry.CompactMetadata.PostTokens
			record.Context.FreePercent = freeContextPercent(record.Context.UsedTokens, record.Context.WindowTokens)
		}
	}
	return record, nil
}

func summarizeCodexSessionFileFast(file string) (SessionRecord, error) {
	lines, err := sampledJSONLLines(file)
	if err != nil {
		return SessionRecord{}, err
	}
	record := SessionRecord{
		Key:             sessionRecordKey("codex", file),
		ID:              sessionIDFromFile(file),
		Source:          "codex",
		DetailAvailable: true,
	}
	if meta, err := history.ReadCodexSessionInfo(file); err == nil && meta != nil {
		record.ID = liveFirstNonEmpty(record.ID, meta.ID)
		record.CWD = meta.CWD
		record.Provider = meta.ModelProvider
		record.Version = meta.CLIVersion
		record.GitBranch = meta.GitBranch
		record.Model = meta.Model
		record.ReasoningEffort = meta.ReasoningEffort
		record.StartedAt = meta.StartedAt
	}
	for _, line := range lines {
		event, err := history.ParseCodexLine(string(line))
		if err != nil {
			continue
		}
		applyCodexSummaryEvent(&record, event)
	}
	if record.ID == "" {
		record.ID = sessionIDFromFile(file)
	}
	return record, nil
}

func applyCodexSummaryEvent(record *SessionRecord, event history.CodexEvent) {
	if ts := event.Time(); ts != nil {
		extendSessionRange(record, *ts)
	}
	switch event.Type {
	case "session_meta":
		if event.Payload.ID != "" {
			record.ID = event.Payload.ID
		}
		if record.CWD == "" {
			record.CWD = event.Payload.CWD
		}
		if record.Provider == "" {
			record.Provider = event.Payload.ModelProvider
		}
		if record.Version == "" {
			record.Version = event.Payload.CLIVersion
		}
		if event.Payload.Git != nil && record.GitBranch == "" {
			record.GitBranch = event.Payload.Git.Branch
		}
	case "thread.started":
		if event.ThreadID != "" {
			record.ID = event.ThreadID
		}
	case "turn_context":
		if record.Model == "" {
			record.Model = event.Payload.Model
		}
		if record.ReasoningEffort == "" {
			record.ReasoningEffort = event.Payload.Effort
		}
	case "response_item":
		switch event.Payload.Type {
		case "function_call":
			record.ToolCalls++
		case "reasoning":
			if len(event.Payload.Summary) > 0 {
				record.Messages++
			}
		case "message":
			if event.Payload.Role == "assistant" && codexContentHasText(event.Payload.Content, "output_text") {
				record.Messages++
			}
		}
	case "event_msg":
		switch event.Payload.Type {
		case "agent_reasoning":
			if event.Payload.Text != "" {
				record.Messages++
			}
		case "agent_message":
			if event.Payload.Message != "" {
				record.Messages++
			}
		case "token_count":
			applyCodexTokenSummary(record, event.Payload.Info)
		}
	case "item.completed":
		if event.Item != nil && (event.Item.Text != "" || codexContentHasText(event.Item.Content, "output_text")) {
			record.Messages++
		}
	case "turn.failed", "error":
		record.ToolCalls++
	}
}

// sampledJSONLLines returns the head and tail JSONL lines of a session file as
// raw byte slices, deduplicated and trimmed. Working in bytes avoids the
// per-line string allocation and the []byte(string) round-trip on the parse
// path. Returned slices are owned by the caller (head lines are copied; tail
// lines slice into a private read buffer).
func sampledJSONLLines(file string) ([][]byte, error) {
	head, err := readHeadLines(file, liveSummaryHeadLines)
	if err != nil {
		return nil, err
	}
	tail, err := readTailLines(file, liveSummaryTailLines, liveSummaryTailBytes)
	if err != nil {
		return nil, err
	}
	lines := make([][]byte, 0, len(head)+len(tail))
	seen := make(map[string]struct{}, len(head)+len(tail))
	for _, line := range append(head, tail...) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if _, ok := seen[string(line)]; ok {
			continue
		}
		seen[string(line)] = struct{}{}
		lines = append(lines, line)
	}
	return lines, nil
}

func readHeadLines(file string, maxLines int) ([][]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	lines := make([][]byte, 0, maxLines)
	for scanner.Scan() {
		// Scanner reuses its buffer across Scan calls, so copy each retained line.
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
		if len(lines) >= maxLines {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func readTailLines(file string, maxLines int, maxBytes int64) ([][]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size == 0 {
		return nil, nil
	}
	readSize := maxBytes
	if size < readSize {
		readSize = size
	}
	buf := make([]byte, readSize)
	offset := size - readSize
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return nil, err
	}
	trimmed := bytes.TrimRight(buf, "\n")
	if len(trimmed) == 0 {
		return nil, nil
	}
	lines := bytes.Split(trimmed, []byte{'\n'})
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, nil
}

func liveFirstNonEmpty(fallback, value string) string {
	if value != "" {
		return value
	}
	return fallback
}
