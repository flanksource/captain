package history

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// renderExtendedTool renders Claude Code agent/task/plan tools and generic MCP
// tool calls that the core ToolUse.Pretty switch does not special-case. It
// appends a concise summary to text and deletes the fields it consumed from
// data so the generic key-value fallback only shows leftover args. It reports
// whether the tool was recognized.
func renderExtendedTool(t ToolUse, text api.Text, data map[string]any, cwd string) (api.Text, bool) {
	if server, name, ok := splitMcpTool(t.Tool); ok {
		return renderMcpTool(t, text, data, server, name), true
	}

	switch t.Tool {
	case "Agent":
		text = appendDesc(text, strField(t.Input, "description"))
		text = appendParen(text, strField(t.Input, "subagent_type"))
		deleteKeys(data, "description", "subagent_type", "prompt")
	case "TaskCreate":
		text = appendDesc(text, firstStrField(t.Input, "subject", "description"))
		deleteKeys(data, "subject", "description", "activeForm", "prompt")
	case "TaskUpdate":
		text = appendID(text, firstStrField(t.Input, "taskId", "task_id", "id"))
		if s := strField(t.Input, "status"); s != "" {
			text = text.Append(" → ", "text-gray-400").Append(s, statusColor(s))
		}
		deleteKeys(data, "taskId", "task_id", "id", "status")
	case "TaskGet", "TaskOutput", "TaskStop":
		text = appendID(text, firstStrField(t.Input, "task_id", "taskId", "id"))
		deleteKeys(data, "task_id", "taskId", "id", "block", "timeout")
	case "TaskList", "EnterPlanMode":
		// the tool name alone is the summary
	case "ToolSearch":
		text = appendValue(text, strField(t.Input, "query"))
		deleteKeys(data, "query", "max_results")
	case "AskUserQuestion":
		text = appendQuestions(text, t.Input)
		deleteKeys(data, "questions")
	case "ExitPlanMode":
		if p := strField(t.Input, "planFilePath"); p != "" {
			text = text.Append(" ", "").Append(shortenPath(p, cwd), "text-cyan-600 font-medium")
		}
		deleteKeys(data, "plan", "planFilePath")
	case "Monitor":
		text = appendDesc(text, strField(t.Input, "description"))
		if cmd := strField(t.Input, "command"); cmd != "" {
			text = text.Add(clicky.CodeBlock("bash", cmd))
		}
		deleteKeys(data, "description", "command")
	case "ScheduleWakeup":
		if d, ok := t.Input["delaySeconds"].(float64); ok && d > 0 {
			text = text.Append(fmt.Sprintf(" (%ds)", int(d)), "text-gray-500")
		}
		text = appendDesc(text, Truncate(strField(t.Input, "reason"), 80))
		deleteKeys(data, "delaySeconds", "reason", "prompt")
	case "PushNotification":
		text = appendValue(text, Truncate(strField(t.Input, "message"), 100))
		deleteKeys(data, "message")
	case "Workflow":
		text = appendValue(text, workflowName(strField(t.Input, "script")))
		deleteKeys(data, "script")
	case "DesignSync":
		text = appendValue(text, strField(t.Input, "method"))
		deleteKeys(data, "method")
	case "NotebookEdit":
		if p := firstStrField(t.Input, "notebook_path", "file_path", "path"); p != "" {
			text = text.Append(" ", "").Append(shortenPath(p, cwd), "text-cyan-600 font-medium")
		}
		deleteKeys(data, "notebook_path", "file_path", "path")
	default:
		return text, false
	}
	return text, true
}

// renderMcpTool renders a generic mcp__server__tool call, highlighting the
// most informative argument (url, sql, query, element) when present.
func renderMcpTool(t ToolUse, text api.Text, data map[string]any, server, name string) api.Text {
	switch {
	case strField(t.Input, "url") != "":
		text = text.Append(": ", "text-gray-600").Append(strField(t.Input, "url"), "text-blue-700 underline")
		deleteKeys(data, "url")
	case isDBServer(server) && firstStrField(t.Input, "sql", "query") != "":
		text = text.Add(clicky.CodeBlock("sql", firstStrField(t.Input, "sql", "query")))
		deleteKeys(data, "sql", "query")
	case strField(t.Input, "query") != "":
		text = appendValue(text, Truncate(strField(t.Input, "query"), 80))
		deleteKeys(data, "query")
	case strField(t.Input, "element") != "":
		text = appendValue(text, strField(t.Input, "element"))
		deleteKeys(data, "element")
	}
	return text
}

// toolLabel returns the human-friendly label shown after the tool icon.
// mcp__server__tool becomes "server tool"; CamelCase names become hyphenated
// lowercase ("TaskUpdate" -> "task-update").
func toolLabel(tool string) string {
	if server, name, ok := splitMcpTool(tool); ok {
		if name == "" {
			return server
		}
		return server + " " + name
	}
	return humanizeTool(tool)
}

func humanizeTool(tool string) string {
	var b strings.Builder
	prev := rune(0)
	for i, r := range tool {
		if i > 0 && unicode.IsUpper(r) && !unicode.IsUpper(prev) {
			b.WriteByte('-')
		}
		b.WriteRune(unicode.ToLower(r))
		prev = r
	}
	return b.String()
}

// splitMcpTool parses "mcp__<server>__<tool>" into its server and tool parts.
func splitMcpTool(tool string) (server, name string, ok bool) {
	const prefix = "mcp__"
	if !strings.HasPrefix(tool, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(tool, prefix)
	parts := strings.SplitN(rest, "__", 2)
	server = parts[0]
	if server == "" {
		return "", "", false
	}
	if len(parts) == 2 {
		name = parts[1]
	}
	return server, name, true
}

func mcpServerIcon(server string) icons.Icon {
	switch server {
	case "postgres", "mssql", "mysql", "sqlite":
		return icons.Database
	case "playwright", "chrome-devtools", "puppeteer":
		return icons.Network
	case "gemini", "gemini-cli", "codex":
		return icons.AI
	case "iconify", "icons8mcp", "lucide", "react-icons":
		return icons.Icon{Unicode: "🎨", Iconify: "mdi:palette", Style: "muted"}
	default:
		return icons.Plugin
	}
}

func isDBServer(server string) bool {
	switch server {
	case "postgres", "mssql", "mysql", "sqlite":
		return true
	default:
		return false
	}
}

var workflowNameRe = regexp.MustCompile(`name:\s*['"]([^'"]+)['"]`)

func workflowName(script string) string {
	if m := workflowNameRe.FindStringSubmatch(script); len(m) == 2 {
		return m[1]
	}
	return ""
}

func statusColor(status string) string {
	switch status {
	case "in_progress", "running":
		return "text-blue-600"
	case "completed", "done", "success":
		return "text-green-600"
	case "pending", "queued":
		return "text-gray-500"
	case "failed", "error", "cancelled", "canceled":
		return "text-red-600"
	default:
		return "text-gray-700"
	}
}

func appendDesc(text api.Text, desc string) api.Text {
	if desc == "" {
		return text
	}
	return text.Append(": ", "text-gray-400").Append(desc, "text-gray-700")
}

func appendValue(text api.Text, value string) api.Text {
	if value == "" {
		return text
	}
	return text.Append(": ", "text-gray-600").Append(value, "text-gray-800")
}

func appendParen(text api.Text, value string) api.Text {
	if value == "" {
		return text
	}
	return text.Append(" (", "text-gray-400").Append(value, "text-gray-500").Append(")", "text-gray-400")
}

func appendID(text api.Text, id string) api.Text {
	if id == "" {
		return text
	}
	return text.Append(" [", "text-gray-400").Append(id, "text-gray-600").Append("]", "text-gray-400")
}

func appendQuestions(text api.Text, input map[string]any) api.Text {
	qs, _ := input["questions"].([]any)
	text = text.Append(fmt.Sprintf(" (%d questions)", len(qs)), "text-gray-500")
	if len(qs) == 0 {
		return text
	}
	first, ok := qs[0].(map[string]any)
	if !ok {
		return text
	}
	if h := strField(first, "header"); h != "" {
		return text.Append(": ", "text-gray-400").Append(h, "text-gray-700")
	}
	if q := strField(first, "question"); q != "" {
		return text.Append(": ", "text-gray-400").Append(Truncate(q, 80), "text-gray-700")
	}
	return text
}

func strField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func firstStrField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := strField(m, k); v != "" {
			return v
		}
	}
	return ""
}

func deleteKeys(m map[string]any, keys ...string) {
	for _, k := range keys {
		delete(m, k)
	}
}
