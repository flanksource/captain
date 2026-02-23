package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/commons/collections"
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
	first := firstNonEmptyLine(data)
	if len(first) == 0 {
		return nil, fmt.Errorf("empty input")
	}

	format := claude.DetectFormat(first)

	switch format {
	case claude.FormatClaudeJSONL:
		entries, err := claude.ReadHistory(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parsing claude jsonl: %w", err)
		}
		return &stdinParseResult{
			Format:   format,
			ToolUses: claude.ExtractToolUses(entries),
		}, nil

	case claude.FormatCodexJSONL:
		codexUses, err := history.ExtractCodexToolUsesFromReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parsing codex jsonl: %w", err)
		}
		toolUses := make([]claude.ToolUse, len(codexUses))
		for i, cu := range codexUses {
			toolUses[i] = claude.ToolUse{
				Tool:      cu.Tool,
				Input:     cu.Input,
				Timestamp: cu.Timestamp,
				CWD:       cu.CWD,
				SessionID: cu.SessionID,
				ToolUseID: cu.ToolUseID,
			}
		}
		return &stdinParseResult{Format: format, ToolUses: toolUses}, nil

	case claude.FormatClaudeCLI:
		var out claude.ClaudeCLIOutput
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("parsing claude cli json: %w", err)
		}
		return &stdinParseResult{Format: format, CLIOut: &out}, nil

	default:
		return nil, fmt.Errorf("unrecognized stream format (first line: %s)", truncate(string(first), 120))
	}
}

func runHistoryFromReader(data []byte, opts HistoryOptions) (any, error) {
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

	cwd, _ := os.Getwd()
	scanner := bash.NewScanner(cwd, nil)
	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())

	filter := claude.Filter{
		Tools: opts.Tools,
		Dirs:  opts.Dirs,
	}
	if !opts.Since.IsZero() {
		filter.Since = &opts.Since
	}
	if len(opts.Categories) == 0 {
		filter.Limit = opts.Limit
	}
	toolUses := claude.FilterToolUses(parsed.ToolUses, filter)

	result := HistoryResult{
		Results: make([]ScanResultRowSingle, 0, len(toolUses)),
	}

	for _, tu := range toolUses {
		category := classifier.ClassifyToolWithPath(tu.Tool, tu.FilePath())
		if category == bash.CategoryOther && tu.Tool == "Bash" {
			if rawCmd, ok := tu.Input["command"].(string); ok {
				category = classifier.ClassifyBash(rawCmd)
			}
		}

		if len(opts.Categories) > 0 && !collections.MatchItems(string(category), opts.Categories...) {
			continue
		}

		cmd := tu.FormatCommand()
		scanResult := scanner.Scan(cmd)
		status := "✓"
		if !scanResult.Allowed {
			status = "✗"
			if scanResult.Reason != "" {
				status += " " + scanResult.Reason
			}
			result.Denied++
		} else {
			result.Allowed++
		}
		result.Total++

		row := ScanResultRowSingle{
			Tool:     tu.Tool,
			Command:  tu.PrettyCommand(),
			Path:     tu.ExtractPath(),
			Category: string(category),
			Status:   status,
			Time:     tu.PrettyTimestamp(),
		}
		if opts.Debug {
			row.ToolUse = &tu
		}
		result.Results = append(result.Results, row)

		if opts.Limit > 0 && len(result.Results) >= opts.Limit {
			break
		}
	}

	return result, nil
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
