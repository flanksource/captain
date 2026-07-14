package tools

import (
	"fmt"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// Synthetic tools surfacing Claude Code stream-json non-tool-use events
// (system/init, system/hook_*, result/*) as rows in the history output.

type SystemInitTool struct{ BaseTool }

func (t *SystemInitTool) Name() string     { return "SessionInit" }
func (t *SystemInitTool) Category() string { return "system" }

func (t *SystemInitTool) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "🚀", Iconify: "mdi:rocket-launch", Style: "muted"}).
		Append(" init", "text-purple-500 font-medium")
	if model := t.Str("model"); model != "" {
		text = text.Append(" "+model, "text-muted")
	}
	if cwd := t.Str("cwd"); cwd != "" {
		text = text.Append(" cwd="+ShortenPath(cwd), "text-gray-500")
	}
	if tools, ok := t.Input["tools"].([]any); ok && len(tools) > 0 {
		text = text.Append(fmt.Sprintf(" tools=%d", len(tools)), "text-gray-500")
	}
	return text
}

func (t *SystemInitTool) Detail() api.Textable { return t.BaseTool.Detail() }

type HookStartedTool struct{ BaseTool }

func (t *HookStartedTool) Name() string     { return "HookStart" }
func (t *HookStartedTool) Category() string { return "hook" }

func (t *HookStartedTool) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "🪝", Iconify: "mdi:hook", Style: "muted"}).
		Append(" "+t.Str("hook_name"), "text-amber-600 font-medium")
	if event := t.Str("hook_event"); event != "" {
		text = text.Append(" ("+event+")", "text-gray-500")
	}
	return text
}

func (t *HookStartedTool) Detail() api.Textable { return t.BaseTool.Detail() }

type HookResponseTool struct{ BaseTool }

func (t *HookResponseTool) Name() string     { return "HookResponse" }
func (t *HookResponseTool) Category() string { return "hook" }

func (t *HookResponseTool) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "🪝", Iconify: "mdi:hook", Style: "muted"}).
		Append(" "+t.Str("hook_name"), "text-amber-600 font-medium")
	if outcome := t.Str("outcome"); outcome != "" {
		color := "text-green-600"
		if outcome != "success" {
			color = "text-red-500"
		}
		text = text.Append(" "+outcome, color)
	}
	exitCode := int(t.Float("exit_code"))
	if exitCode != 0 {
		text = text.Append(fmt.Sprintf(" exit=%d", exitCode), "text-red-500")
	}
	return text
}

func (t *HookResponseTool) Detail() api.Textable {
	if d := t.BaseTool.Detail(); d != nil {
		return d
	}
	stdout := strings.TrimSpace(t.Str("stdout"))
	stderr := strings.TrimSpace(t.Str("stderr"))
	if stdout == "" && stderr == "" {
		return nil
	}
	text := clicky.Text("")
	if stdout != "" {
		text = text.Append("stdout: ", "font-bold text-muted").Append(stdout, "")
	}
	if stderr != "" {
		if stdout != "" {
			text = text.NewLine()
		}
		text = text.Append("stderr: ", "font-bold text-red-500").Append(stderr, "")
	}
	return &text
}

type ResultSummaryTool struct{ BaseTool }

func (t *ResultSummaryTool) Name() string     { return "Result" }
func (t *ResultSummaryTool) Category() string { return "result" }

func (t *ResultSummaryTool) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "🏁", Iconify: "mdi:flag-checkered", Style: "muted"}).
		Append(" result", "text-cyan-600 font-medium")
	if turns := int(t.Float("num_turns")); turns > 0 {
		text = text.Append(fmt.Sprintf(" turns=%d", turns), "text-gray-500")
	}
	if cost := t.Float("total_cost_usd"); cost > 0 {
		text = text.Append(fmt.Sprintf(" $%.4f", cost), "text-gray-500")
	}
	if ms := t.Float("duration_ms"); ms > 0 {
		text = text.Append(" "+formatDurationMS(ms), "text-gray-500")
	}
	isErr, _ := t.Input["is_error"].(bool)
	if isErr {
		text = text.Append(" ERROR", "text-red-500 font-bold")
		if status := int(t.Float("api_error_status")); status > 0 {
			text = text.Append(fmt.Sprintf(" %d", status), "text-red-500")
		}
		if reason := t.Str("terminal_reason"); reason != "" && reason != "completed" {
			text = text.Append(" ("+reason+")", "text-red-500")
		}
		if msg := strings.TrimSpace(t.Str("result")); msg != "" {
			preview := msg
			if len(preview) > 100 {
				preview = preview[:97] + "..."
			}
			text = text.NewLine().Append("  "+preview, "text-red-400 italic")
		}
	}
	return text
}

func (t *ResultSummaryTool) Detail() api.Textable {
	if d := t.BaseTool.Detail(); d != nil {
		return d
	}
	result := strings.TrimSpace(t.Str("result"))
	if result == "" {
		return nil
	}
	text := clicky.Text("").Append(result, "")
	return &text
}

type ApiErrorTool struct{ BaseTool }

func (t *ApiErrorTool) Name() string     { return "ApiError" }
func (t *ApiErrorTool) Category() string { return "error" }

func (t *ApiErrorTool) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "❌", Iconify: "mdi:alert-circle", Style: "muted"}).
		Append(" api-error", "text-red-500 font-bold")
	if errStr := errorSummary(t.Input["error"]); errStr != "" {
		text = text.Append(" "+errStr, "text-red-500")
	}
	if status := int(t.Float("api_error_status")); status > 0 {
		text = text.Append(fmt.Sprintf(" %d", status), "text-red-500")
	}
	if reason := t.Str("terminal_reason"); reason != "" {
		text = text.Append(" ("+reason+")", "text-gray-500")
	}
	return text
}

func errorSummary(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case map[string]any:
		for _, key := range []string{"formatted", "message", "status"} {
			if s, ok := v[key].(string); ok && s != "" {
				return s
			}
			if n, ok := v[key].(float64); ok && n != 0 {
				return fmt.Sprintf("%.0f", n)
			}
		}
	}
	return ""
}

func (t *ApiErrorTool) Detail() api.Textable {
	if d := t.BaseTool.Detail(); d != nil {
		return d
	}
	return nil
}

type ParseErrorTool struct{ BaseTool }

func (t *ParseErrorTool) Name() string     { return "ParseError" }
func (t *ParseErrorTool) Category() string { return "error" }

func (t *ParseErrorTool) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "⚠", Iconify: "mdi:alert", Style: "muted"}).
		Append(" parse-error", "text-red-500 font-bold")
	if line := int(t.Float("line")); line > 0 {
		text = text.Append(fmt.Sprintf(" line=%d", line), "text-gray-500")
	}
	if errStr := t.Str("error"); errStr != "" {
		text = text.Append(" "+errStr, "text-red-500")
	}
	return text
}

func (t *ParseErrorTool) Detail() api.Textable {
	raw := strings.TrimSpace(t.Str("raw"))
	if raw == "" {
		return t.BaseTool.Detail()
	}
	text := clicky.Text("").Append("raw: ", "font-bold text-muted").Append(raw, "")
	return &text
}

func formatDurationMS(ms float64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%.0fms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1fs", ms/1000)
	default:
		return fmt.Sprintf("%dm%02ds", int(ms/60_000), int(ms/1000)%60)
	}
}

type StopHookSummaryTool struct{ BaseTool }

func (t *StopHookSummaryTool) Name() string     { return "StopHookSummary" }
func (t *StopHookSummaryTool) Category() string { return "hook" }

func (t *StopHookSummaryTool) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "🪝", Iconify: "mdi:hook", Style: "muted"}).
		Append(" stop-hooks", "text-amber-600 font-medium")
	if n := int(t.Float("hookCount")); n > 0 {
		text = text.Append(fmt.Sprintf(" count=%d", n), "text-gray-500")
	}
	if errs := int(t.Float("hookErrors")); errs > 0 {
		text = text.Append(fmt.Sprintf(" errors=%d", errs), "text-red-500")
	}
	if reason := t.Str("stopReason"); reason != "" {
		text = text.Append(" "+reason, "text-gray-500")
	}
	return text
}

func (t *StopHookSummaryTool) Detail() api.Textable { return t.BaseTool.Detail() }

type TurnDurationTool struct{ BaseTool }

func (t *TurnDurationTool) Name() string     { return "TurnDuration" }
func (t *TurnDurationTool) Category() string { return "system" }

func (t *TurnDurationTool) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "⏱", Iconify: "mdi:timer-outline", Style: "muted"}).
		Append(" turn", "text-muted font-medium")
	if ms := t.Float("durationMs"); ms > 0 {
		text = text.Append(" "+formatDurationMS(ms), "text-gray-500")
	}
	if n := int(t.Float("messageCount")); n > 0 {
		text = text.Append(fmt.Sprintf(" msgs=%d", n), "text-gray-500")
	}
	return text
}

func (t *TurnDurationTool) Detail() api.Textable { return t.BaseTool.Detail() }

type AwaySummaryTool struct{ BaseTool }

func (t *AwaySummaryTool) Name() string     { return "AwaySummary" }
func (t *AwaySummaryTool) Category() string { return "system" }

func (t *AwaySummaryTool) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "💤", Iconify: "mdi:sleep", Style: "muted"}).
		Append(" away-summary", "text-muted font-medium")
	if content := strings.TrimSpace(t.Str("content")); content != "" {
		preview := content
		if len(preview) > 80 {
			preview = preview[:77] + "..."
		}
		text = text.Append(" "+preview, "text-gray-500 italic")
	}
	return text
}

func (t *AwaySummaryTool) Detail() api.Textable {
	if d := t.BaseTool.Detail(); d != nil {
		return d
	}
	if c := strings.TrimSpace(t.Str("content")); c != "" {
		text := clicky.Text("").Append(c, "")
		return &text
	}
	return nil
}

type SessionTitleTool struct{ BaseTool }

func (t *SessionTitleTool) Name() string     { return "SessionTitle" }
func (t *SessionTitleTool) Category() string { return "system" }

func (t *SessionTitleTool) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "🏷", Iconify: "mdi:tag", Style: "muted"}).
		Append(" title", "text-purple-500 font-medium")
	if title := t.Str("aiTitle"); title != "" {
		text = text.Append(" "+title, "text-muted")
	}
	return text
}

func (t *SessionTitleTool) Detail() api.Textable { return t.BaseTool.Detail() }
