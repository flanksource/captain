package cli

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
)

type CLIOutputResult struct {
	Result     string  `json:"result" pretty:"label=Result"`
	SessionID  string  `json:"session_id" pretty:"label=Session"`
	CostUSD    float64 `json:"cost_usd,omitempty" pretty:"label=Cost (USD)"`
	DurationMS float64 `json:"duration_ms,omitempty" pretty:"label=Duration (ms)"`
	NumTurns   int     `json:"num_turns,omitempty" pretty:"label=Turns"`
	Input      int     `json:"input_tokens,omitempty" pretty:"label=Input Tokens"`
	Output     int     `json:"output_tokens,omitempty" pretty:"label=Output Tokens"`
}

type stdinParseResult struct {
	Format   claude.StreamFormat
	ToolUses []claude.ToolUse
	CLIOut   *claude.ClaudeCLIOutput
}

func parseFromReader(data []byte) (*stdinParseResult, error) {
	format, sample := detectStreamFormat(data)
	if len(sample) == 0 {
		return nil, fmt.Errorf("empty input")
	}

	switch format {
	case claude.FormatClaudeJSONL:
		entries, err := claude.ReadHistory(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parsing claude jsonl: %w", err)
		}
		return &stdinParseResult{
			Format:   format,
			ToolUses: claude.ExtractToolUsesWithTokens(entries),
		}, nil

	case claude.FormatClaudeStreamJSON:
		entries, err := claude.ReadStreamJSON(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parsing claude stream-json: %w", err)
		}
		return &stdinParseResult{
			Format:   format,
			ToolUses: claude.ExtractToolUsesWithTokens(entries),
		}, nil

	case claude.FormatCodexJSONL:
		codexUses, err := history.ExtractCodexToolUsesFromReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parsing codex jsonl: %w", err)
		}
		toolUses := codexToClaudeToolUses(codexUses)
		return &stdinParseResult{Format: format, ToolUses: toolUses}, nil

	case claude.FormatClaudeCLI:
		var out claude.ClaudeCLIOutput
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("parsing claude cli json: %w", err)
		}
		return &stdinParseResult{Format: format, CLIOut: &out}, nil

	default:
		return nil, fmt.Errorf("unrecognized stream format (first line: %s)", truncate(string(sample), 120))
	}
}

// detectStreamFormat scans non-empty lines and returns the first non-Unknown
// format found, along with the sample line that produced it. Newer Claude Code
// session JSONL files lead with metadata records (file-history-snapshot,
// last-prompt, permission-mode, system/local_command) that no format matches —
// scanning past them lets us identify the underlying stream. If the first line
// already maps to FormatClaudeCLI (a single-object format), we accept it
// immediately so we don't keep scanning a single-line payload.
func detectStreamFormat(data []byte) (claude.StreamFormat, []byte) {
	var firstLine []byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if firstLine == nil {
			firstLine = trimmed
		}
		f := claude.DetectFormat(trimmed)
		if f != claude.FormatUnknown {
			return f, trimmed
		}
	}
	return claude.FormatUnknown, firstLine
}

func runHistoryFromReader(data []byte, opts HistoryOptions) (any, error) {
	var sessionIDs []string
	var err error
	opts, sessionIDs, err = normalizeHistoryOptions(opts)
	if err != nil {
		return nil, err
	}

	parsed, err := parseFromReader(data)
	if err != nil {
		return nil, err
	}

	if parsed.CLIOut != nil {
		r := CLIOutputResult{
			Result:     parsed.CLIOut.Result,
			SessionID:  parsed.CLIOut.SessionID,
			CostUSD:    parsed.CLIOut.CostUSD,
			DurationMS: parsed.CLIOut.DurationMS,
			NumTurns:   parsed.CLIOut.NumTurns,
		}
		if parsed.CLIOut.Usage != nil {
			r.Input = parsed.CLIOut.Usage.InputTokens
			r.Output = parsed.CLIOut.Usage.OutputTokens
		}
		return r, nil
	}

	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())

	filter := claude.Filter{
		Tools:      opts.Tools,
		Paths:      resolvePaths(opts.Paths),
		SessionID:  firstSessionID(sessionIDs),
		SessionIDs: sessionIDs,
	}
	if !opts.Since.IsZero() {
		filter.Since = &opts.Since
	}
	if len(opts.Categories) == 0 && opts.TextFilter == "" && !usesDefaultHiddenHistoryTools(opts) {
		filter.Limit = opts.Limit
	}
	toolUses := claude.FilterToolUses(parsed.ToolUses, filter)

	if opts.Summary {
		return runHistorySummary(toolUses, opts, classifier, nil)
	}

	tl := collapseRepeatedTitles(claude.ToolUsesToTools(toolUses))
	return runHistorySingle(tl, opts, classifier, nil)
}

func firstNonEmptyLine(data []byte) []byte {
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			return trimmed
		}
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// codexToClaudeToolUses converts codex history.ToolUse records into the
// claude.ToolUse shape used by the rendering pipeline. Source/model/effort and
// the captured shell output (Response) are preserved so the same Claude tool
// types (BashTool, AssistantTool, …) can render codex rows.
func codexToClaudeToolUses(uses []history.ToolUse) []claude.ToolUse {
	out := make([]claude.ToolUse, len(uses))
	for i, cu := range uses {
		source := cu.Source
		if source == "" {
			source = "codex"
		}
		out[i] = claude.ToolUse{
			Tool:            cu.Tool,
			Input:           cu.Input,
			Timestamp:       cu.Timestamp,
			CWD:             cu.CWD,
			SessionID:       cu.SessionID,
			ToolUseID:       cu.ToolUseID,
			Source:          source,
			Model:           cu.Model,
			ReasoningEffort: cu.ReasoningEffort,
			InputTokens:     cu.InputTokens + cu.CacheReadTokens,
			OutputTokens:    cu.OutputTokens,
			AgentID:         cu.AgentID,
			AgentType:       cu.AgentType,
			AgentDesc:       cu.AgentDesc,
			Response:        cu.Response,
		}
	}
	return out
}
