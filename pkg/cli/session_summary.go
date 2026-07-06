package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
)

const (
	defaultClaudeContextWindow = 1_000_000
	defaultCodexContextWindow  = 200_000
)

type claudeSummaryLine struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
	SessionIDSnake string `json:"session_id,omitempty"`
	Timestamp      string `json:"timestamp,omitempty"`
	Version        string `json:"version,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	GitBranch      string `json:"gitBranch,omitempty"`
	Slug           string `json:"slug,omitempty"`
	Error          string `json:"error,omitempty"`
	Message        *struct {
		Role       string          `json:"role,omitempty"`
		Model      string          `json:"model,omitempty"`
		Content    json.RawMessage `json:"content,omitempty"`
		Usage      *claude.Usage   `json:"usage,omitempty"`
		StopReason string          `json:"stop_reason,omitempty"`
	} `json:"message,omitempty"`
	CompactMetadata struct {
		PostTokens int `json:"postTokens,omitempty"`
	} `json:"compactMetadata,omitempty"`
}

func (l claudeSummaryLine) sessionID() string {
	if l.SessionID != "" {
		return l.SessionID
	}
	return l.SessionIDSnake
}

func summarizeClaudeContent(raw json.RawMessage) (hasText bool, toolCalls int) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, 0
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
			return true, 0
		}
		return false, 0
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		Thinking string `json:"thinking,omitempty"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return false, 0
	}
	for _, block := range blocks {
		switch block.Type {
		case string(claude.ContentTypeText):
			if strings.TrimSpace(block.Text) != "" {
				hasText = true
			}
		case string(claude.ContentTypeThinking), string(claude.ContentTypeRedactedThinking):
			if strings.TrimSpace(block.Thinking) != "" {
				hasText = true
			}
		case string(claude.ContentTypeToolUse):
			toolCalls++
		}
	}
	return hasText, toolCalls
}

func claudeSyntheticToolLine(entry claudeSummaryLine) bool {
	switch entry.Type {
	case "result", "ai-title":
		return true
	case "system":
		switch entry.Subtype {
		case "init", "hook_started", "hook_response", "stop_hook_summary", "turn_duration", "away_summary":
			return true
		}
	}
	return false
}

func applyClaudeUsageSummary(record *SessionRecord, usage *claude.Usage, model string) {
	if usage == nil {
		return
	}
	ensureTokens(record)
	record.Tokens.InputTokens += usage.InputTokens
	record.Tokens.OutputTokens += usage.OutputTokens
	record.Tokens.CacheReadTokens += usage.CacheReadInputTokens
	record.Tokens.CacheCreationTokens += usage.CacheCreationInputTokens
	record.Tokens.TotalTokens = sessionTokensTotal(record.Tokens)
	ensureContext(record, defaultClaudeContextWindow)
	record.Context.UsedTokens = usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	record.Context.FreePercent = freeContextPercent(record.Context.UsedTokens, record.Context.WindowTokens)
	if strings.Contains(strings.ToLower(model), "claude") || claude.ClassifyModel(model) != claude.ModelFamilyUnknown {
		record.CostUSD += claude.CalculateCost(usage, model)
	}
}

func applyCodexTokenSummary(record *SessionRecord, info *history.CodexTokenInfo) {
	if info == nil {
		return
	}
	ensureTokens(record)
	total := info.TotalTokenUsage
	record.Tokens.InputTokens = total.InputTokens
	record.Tokens.OutputTokens = total.OutputTokens
	record.Tokens.CacheReadTokens = total.CachedInputTokens
	record.Tokens.TotalTokens = total.TotalTokens
	if record.Tokens.TotalTokens == 0 {
		record.Tokens.TotalTokens = sessionTokensTotal(record.Tokens)
	}
	window := info.ModelContextWindow
	if window <= 0 {
		window = defaultCodexContextWindow
	}
	ensureContext(record, window)
	record.Context.WindowTokens = window
	used := info.LastTokenUsage.InputTokens + info.LastTokenUsage.CachedInputTokens
	if used == 0 {
		used = total.InputTokens + total.CachedInputTokens
	}
	record.Context.UsedTokens = used
	record.Context.FreePercent = freeContextPercent(record.Context.UsedTokens, record.Context.WindowTokens)
}

func ensureTokens(record *SessionRecord) {
	if record.Tokens == nil {
		record.Tokens = &SessionTokensWire{}
	}
}

func ensureContext(record *SessionRecord, window int) {
	if window <= 0 {
		window = defaultClaudeContextWindow
	}
	if record.Context == nil {
		record.Context = &SessionContextWire{WindowTokens: window}
	}
	if record.Context.WindowTokens == 0 {
		record.Context.WindowTokens = window
	}
}

func sessionTokensTotal(tokens *SessionTokensWire) int {
	if tokens == nil {
		return 0
	}
	return tokens.InputTokens + tokens.OutputTokens + tokens.CacheReadTokens + tokens.CacheCreationTokens
}

func freeContextPercent(used, window int) int {
	if window <= 0 {
		return 0
	}
	if used < 0 {
		used = 0
	}
	free := 100 - int(float64(used)/float64(window)*100)
	if free < 0 {
		return 0
	}
	if free > 100 {
		return 100
	}
	return free
}

func codexContentHasText(content []history.CodexContent, textType string) bool {
	for _, block := range content {
		if block.Type == textType && strings.TrimSpace(block.Text) != "" {
			return true
		}
	}
	return false
}

func sessionRecordMatchesProject(record SessionRecord, projectRoot string) bool {
	if projectRoot == "" {
		return true
	}
	cwd := record.CWD
	if cwd == "" {
		return false
	}
	rel, err := filepath.Rel(projectRoot, cwd)
	if err == nil && (rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")) {
		return true
	}
	return strings.HasPrefix(cwd, projectRoot+string(filepath.Separator))
}

func parseSessionSummaryTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	return time.Time{}
}
