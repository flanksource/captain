package history

import (
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

type Tool interface {
	ToolName() string
	Pretty() api.Text
}

type Questions struct {
	Questions []Question `json:"questions"`
}

type ExitPlanMode struct {
	Plan string `json:"plan"`
}

type Question struct {
	Description string   `json:"description"`
	Label       string   `json:"label"`
	Header      string   `json:"header,omitempty"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
	Options     []Option `json:"options,omitempty"`
}

type Option struct {
	Description string `json:"description"`
	Label       string `json:"label"`
}

type TodoWrite struct {
	Todos []Todo `json:"todos"`
}

type Todo struct {
	ActiveForm string `json:"activeForm,omitempty"`
	Content    string `json:"content,omitempty"`
	Status     string `json:"status,omitempty"`
}

// --- Typed tool structs ---
// Each delegates to ToolUse.Pretty() for unified rendering.

type Bash struct {
	Command string `json:"command"`
	CWD     string `json:"cwd,omitempty"`
	Timeout float64 `json:"timeout,omitempty"`
}

func (b Bash) ToolName() string { return "Bash" }

func (b Bash) Pretty() api.Text {
	return b.toToolUse().Pretty()
}

func (b Bash) toToolUse() ToolUse {
	input := map[string]any{"command": b.Command}
	if b.Timeout > 0 {
		input["timeout"] = b.Timeout
	}
	return ToolUse{Tool: "Bash", Input: input, CWD: b.CWD}
}

type Read struct {
	Path   string  `json:"path"`
	Limit  float64 `json:"limit,omitempty"`
	Offset float64 `json:"offset,omitempty"`
}

func (r Read) ToolName() string { return "Read" }

func (r Read) Pretty() api.Text {
	return r.toToolUse().Pretty()
}

func (r Read) toToolUse() ToolUse {
	input := map[string]any{"path": r.Path}
	if r.Limit > 0 {
		input["limit"] = r.Limit
	}
	if r.Offset > 0 {
		input["offset"] = r.Offset
	}
	return ToolUse{Tool: "Read", Input: input}
}

type Write struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}

func (w Write) ToolName() string { return "Write" }

func (w Write) Pretty() api.Text {
	return w.toToolUse().Pretty()
}

func (w Write) toToolUse() ToolUse {
	input := map[string]any{"path": w.Path}
	if w.Content != "" {
		input["content"] = w.Content
	}
	return ToolUse{Tool: "Write", Input: input}
}

type Edit struct {
	Path      string `json:"path"`
	OldString string `json:"old_string,omitempty"`
	NewString string `json:"new_string,omitempty"`
}

func (e Edit) ToolName() string { return "Edit" }

func (e Edit) Pretty() api.Text {
	return e.toToolUse().Pretty()
}

func (e Edit) toToolUse() ToolUse {
	input := map[string]any{"path": e.Path}
	if e.OldString != "" {
		input["old_string"] = e.OldString
	}
	if e.NewString != "" {
		input["new_string"] = e.NewString
	}
	return ToolUse{Tool: "Edit", Input: input}
}

type Grep struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	OutputMode string `json:"output_mode,omitempty"`
	Count      int    `json:"-n,omitempty"`
}

func (g Grep) ToolName() string { return "Grep" }

func (g Grep) Pretty() api.Text {
	return g.toToolUse().Pretty()
}

func (g Grep) toToolUse() ToolUse {
	input := map[string]any{"pattern": g.Pattern}
	if g.Path != "" {
		input["path"] = g.Path
	}
	if g.Glob != "" {
		input["glob"] = g.Glob
	}
	if g.OutputMode != "" {
		input["output_mode"] = g.OutputMode
	}
	return ToolUse{Tool: "Grep", Input: input}
}

type Task struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt,omitempty"`
	SubAgentType string `json:"subagent_type,omitempty"`
}

func (t Task) ToolName() string { return "Task" }

func (t Task) Pretty() api.Text {
	return t.toToolUse().Pretty()
}

func (t Task) toToolUse() ToolUse {
	input := map[string]any{}
	if t.Description != "" {
		input["description"] = t.Description
	}
	if t.Prompt != "" {
		input["prompt"] = t.Prompt
	}
	if t.SubAgentType != "" {
		input["subagent_type"] = t.SubAgentType
	}
	return ToolUse{Tool: "Task", Input: input}
}

type MultiEdit struct {
	Edits []Edit `json:"edits"`
}

func (m MultiEdit) ToolName() string { return "MultiEdit" }

func (m MultiEdit) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"}).
		Append(" multi-edit", "text-purple-600 font-medium")
	if len(m.Edits) > 0 {
		text = text.Append(fmt.Sprintf(" (%d edits)", len(m.Edits)), "text-gray-500")
	}
	return text
}

type Glob struct {
	Pattern string `json:"pattern"`
}

func (g Glob) ToolName() string { return "Glob" }

func (g Glob) Pretty() api.Text {
	return g.toToolUse().Pretty()
}

func (g Glob) toToolUse() ToolUse {
	input := map[string]any{"pattern": g.Pattern}
	return ToolUse{Tool: "Glob", Input: input}
}

type WebFetch struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt,omitempty"`
}

func (w WebFetch) ToolName() string { return "WebFetch" }

func (w WebFetch) Pretty() api.Text {
	return w.toToolUse().Pretty()
}

func (w WebFetch) toToolUse() ToolUse {
	input := map[string]any{"url": w.URL}
	if w.Prompt != "" {
		input["prompt"] = w.Prompt
	}
	return ToolUse{Tool: "WebFetch", Input: input}
}

type BashOutput struct {
	BashId string `json:"bash_id"`
}

func (b BashOutput) ToolName() string { return "BashOutput" }

func (b BashOutput) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "📋", Iconify: "codicon:output", Style: "muted"}).
		Append(" bash-output", "text-green-600 font-medium")
	if b.BashId != "" {
		text = text.Append(" [", "text-gray-400").Append(b.BashId, "text-gray-600").Append("]", "text-gray-400")
	}
	return text
}

type KillShell struct {
	ShellId string `json:"shell_id"`
}

func (k KillShell) ToolName() string { return "KillShell" }

func (k KillShell) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "🔴", Iconify: "codicon:debug-stop", Style: "muted"}).
		Append(" kill-shell", "text-red-600 font-medium")
	if k.ShellId != "" {
		text = text.Append(" [", "text-gray-400").Append(k.ShellId, "text-gray-600").Append("]", "text-gray-400")
	}
	return text
}

type WebSearch struct {
	Query string `json:"query"`
}

func (w WebSearch) ToolName() string { return "WebSearch" }

func (w WebSearch) Pretty() api.Text {
	return w.toToolUse().Pretty()
}

func (w WebSearch) toToolUse() ToolUse {
	return ToolUse{Tool: "WebSearch", Input: map[string]any{"query": w.Query}}
}

type Skill struct {
	Command string `json:"command"`
}

func (s Skill) ToolName() string { return "Skill" }

func (s Skill) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Info).
		Append(" skill", "text-teal-600 font-medium")
	if s.Command != "" {
		text = text.Append(": ", "text-gray-600").Append(s.Command, "text-gray-800")
	}
	return text
}

func AllTools() []Tool {
	tools := []Tool{
		Bash{}, Read{}, Write{}, Edit{}, Grep{}, Glob{},
		WebFetch{}, Skill{}, MultiEdit{}, Task{},
		BashOutput{}, KillShell{}, WebSearch{},
	}
	tools = append(tools, McpTools...)
	tools = append(tools, CodexTools...)
	return tools
}
