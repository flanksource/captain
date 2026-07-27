package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
)

func eventInt(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func eventFloat(value any) float64 {
	switch v := value.(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	default:
		return 0
	}
}

func eventString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func eventText(icon api.Textable, label, color string) api.Text {
	return clicky.Text("").Add(icon).Append(" "+label, color)
}

func eventPreview(s string, max int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func firstNonEmptyEvent(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func mapLen(value any) int {
	switch v := value.(type) {
	case map[string]any:
		return len(v)
	case map[string]map[string]any:
		return len(v)
	default:
		return 0
	}
}

func listLen(value any) int {
	switch v := value.(type) {
	case []any:
		return len(v)
	case []string:
		return len(v)
	default:
		return 0
	}
}

func appendListCount(text *api.Text, label string, value any) {
	if n := listLen(value); n > 0 {
		*text = text.Append(fmt.Sprintf(" %s=%d", label, n), "text-gray-500")
	}
}

func statusColor(status string) string {
	switch strings.ToLower(status) {
	case "completed", "success", "ok", "approved", "pass", "passed":
		return "text-green-600"
	case "failed", "error", "denied", "blocked", "rejected", "interrupted":
		return "text-red-500"
	default:
		return "text-gray-500"
	}
}

func codexCommandString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	case []string:
		return strings.Join(v, " ")
	default:
		return ""
	}
}

func commandOutputDetail(base BaseTool) api.Textable {
	if d := base.Detail(); d != nil {
		return d
	}
	stdout := strings.TrimSpace(base.Str("stdout"))
	stderr := strings.TrimSpace(base.Str("stderr"))
	if stdout == "" && stderr == "" {
		stdout = strings.TrimSpace(base.Str("aggregated_output"))
	}
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

func invocationName(value any) (string, string) {
	m, ok := value.(map[string]any)
	if !ok {
		return "", ""
	}
	return eventString(m["server"]), eventString(m["tool"])
}

func durationString(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if secs := eventFloat(m["secs"]); secs > 0 {
		return formatDurationMS(secs * 1000)
	}
	if nanos := eventFloat(m["nanos"]); nanos > 0 {
		return formatDurationMS(nanos / 1_000_000)
	}
	return ""
}

func resultStatus(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for k := range m {
		if strings.EqualFold(k, "ok") {
			return "ok"
		}
		if strings.EqualFold(k, "err") || strings.EqualFold(k, "error") {
			return "error"
		}
	}
	return ""
}

func actionQuery(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if q := eventString(m["query"]); q != "" {
		return q
	}
	if qs, ok := m["queries"].([]any); ok && len(qs) > 0 {
		if q, ok := qs[0].(string); ok {
			return q
		}
	}
	return ""
}

func actionSummary(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, key := range []string{"tool", "command", "cwd"} {
		if value := eventString(m[key]); value != "" {
			if key == "cwd" {
				value = filepath.Base(value)
			}
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " ")
}

func findingsCount(value any) int {
	m, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	return listLen(m["findings"])
}

func nestedString(value any, key string) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return eventString(m[key])
}
