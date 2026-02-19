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

type Bash struct {
	Command string `json:"command"`
	CWD     string `json:"cwd,omitempty"`
}

func (b Bash) ToolName() string { return "Bash" }

func (b Bash) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "💻", Iconify: "codicon:terminal", Style: "muted"}).Append(" Bash", "text-green-600 font-medium")
	if b.CWD != "" {
		text = text.Append(" (", "text-gray-400").Append(b.CWD, "text-gray-500").Append(")", "text-gray-400")
	}
	if b.Command != "" {
		text = text.NewLine().Add(clicky.CodeBlock(b.Command, "bash"))
	}
	return text
}

type Read struct {
	Path   string  `json:"path"`
	Limit  float64 `json:"limit,omitempty"`
	Offset float64 `json:"offset,omitempty"`
}

func (r Read) ToolName() string { return "Read" }

func (r Read) Pretty() api.Text {
	text := clicky.Text("").Add(icons.File).Append(" Read", "text-blue-600 font-medium")
	if r.Path != "" {
		text = text.Append(": ", "text-gray-600").Append(r.Path, "text-gray-800")
		if r.Limit > 0 || r.Offset > 0 {
			text = text.Append(fmt.Sprintf(" [%.0f:%.0f]", r.Offset, r.Limit), "text-gray-500")
		}
	}
	return text
}

type Write struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}

func (w Write) ToolName() string { return "Write" }

func (w Write) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"}).Append(" Write", "text-orange-600 font-medium")
	if w.Path != "" {
		text = text.Append(": ", "text-gray-600").Append(w.Path, "text-gray-800")
	}
	if w.Content != "" {
		preview := w.Content
		if len(preview) > 100 {
			preview = preview[:97] + "..."
		}
		text = text.NewLine().Append("Content: ", "text-gray-500").Append(preview, "text-gray-700")
	}
	return text
}

type Edit struct {
	Path      string `json:"path"`
	OldString string `json:"old_string,omitempty"`
	NewString string `json:"new_string,omitempty"`
}

func (e Edit) ToolName() string { return "Edit" }

func (e Edit) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"}).Append(" Edit", "text-purple-600 font-medium")
	if e.Path != "" {
		text = text.Append(": ", "text-gray-600").Append(e.Path, "text-gray-800")
	}
	if e.OldString != "" && e.NewString != "" {
		old, new := e.OldString, e.NewString
		if len(old) > 50 {
			old = old[:47] + "..."
		}
		if len(new) > 50 {
			new = new[:47] + "..."
		}
		text = text.NewLine().Append("Replace: ", "text-gray-500").Append(old, "text-red-600").
			Append(" → ", "text-gray-400").Append(new, "text-green-600")
	}
	return text
}

type Grep struct {
	OutputMode string `json:"output_mode,omitempty"`
	Glob       string `json:"glob,omitempty"`
	Count      int    `json:"-n,omitempty"`
}

func (g Grep) ToolName() string { return "Grep" }

func (g Grep) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Search).Append(" Grep", "text-yellow-600 font-medium")
	if g.Glob != "" {
		text = text.Append(": ", "text-gray-600").Append(g.Glob, "text-gray-800")
	}
	if g.OutputMode != "" {
		text = text.Append(" (", "text-gray-400").Append(g.OutputMode, "text-gray-500").Append(")", "text-gray-400")
	}
	return text
}

type Task struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt,omitempty"`
	SubAgentType string `json:"subagent_type,omitempty"`
}

func (t Task) ToolName() string { return "Task" }

func (t Task) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Package).Append(" Task", "text-indigo-600 font-medium")
	if t.Description != "" {
		text = text.Append(": ", "text-gray-600").Append(t.Description, "text-gray-800")
	}
	if t.SubAgentType != "" {
		text = text.Append(" (", "text-gray-400").Append(t.SubAgentType, "text-gray-500").Append(")", "text-gray-400")
	}
	return text
}

type MultiEdit struct {
	Edits []Edit `json:"edits"`
}

func (m MultiEdit) ToolName() string { return "MultiEdit" }

func (m MultiEdit) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"}).Append(" MultiEdit", "text-purple-600 font-medium")
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
	text := clicky.Text("").Add(icons.Search).Append(" Glob", "text-cyan-600 font-medium")
	if g.Pattern != "" {
		text = text.Append(": ", "text-gray-600").Append(g.Pattern, "text-gray-800")
	}
	return text
}

type WebFetch struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt,omitempty"`
}

func (w WebFetch) ToolName() string { return "WebFetch" }

func (w WebFetch) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Cloud).Append(" WebFetch", "text-blue-600 font-medium")
	if w.URL != "" {
		text = text.Append(": ", "text-gray-600").Append(w.URL, "text-blue-700 underline")
	}
	if w.Prompt != "" {
		prompt := w.Prompt
		if len(prompt) > 60 {
			prompt = prompt[:57] + "..."
		}
		text = text.NewLine().Append("Prompt: ", "text-gray-500").Append(prompt, "text-gray-700")
	}
	return text
}

type BashOutput struct {
	BashId string `json:"bash_id"`
}

func (b BashOutput) ToolName() string { return "BashOutput" }

func (b BashOutput) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "📋", Iconify: "codicon:output", Style: "muted"}).Append(" BashOutput", "text-green-600 font-medium")
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
	text := clicky.Text("").Add(icons.Icon{Unicode: "🔴", Iconify: "codicon:debug-stop", Style: "muted"}).Append(" KillShell", "text-red-600 font-medium")
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
	text := clicky.Text("").Add(icons.Search).Append(" WebSearch", "text-purple-600 font-medium")
	if w.Query != "" {
		text = text.Append(": ", "text-gray-600").Append(w.Query, "text-gray-800")
	}
	return text
}

type Skill struct {
	Command string `json:"command"`
}

func (s Skill) ToolName() string { return "Skill" }

func (s Skill) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Info).Append(" Skill", "text-teal-600 font-medium")
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
