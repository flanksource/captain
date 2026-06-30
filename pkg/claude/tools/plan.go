package tools

import (
	"path/filepath"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

var planIcon = icons.Icon{Unicode: "📋", Iconify: "mdi:clipboard-text", Style: "muted"}
var planColor = "text-green-400 font-medium"

type PlanTool struct{ BaseTool }

func (t *PlanTool) Name() string     { return "Plan" }
func (t *PlanTool) Category() string { return "" }

func (t *PlanTool) Pretty() api.Text {
	text := t.header(planIcon, "plan", planColor)
	if t.Denied {
		text = text.Append(" ✗", "text-red-500")
	}
	text = text.Append(" "+planName(t.FilePath()), "text-cyan-400")
	content := t.Str("content")
	if content == "" {
		content = t.Str("new_string")
	}
	if title := extractMarkdownTitle(content); title != "" {
		text = text.Append(" — ", "text-gray-400").Append(title, "max-w-[tw-30ch]")
	}
	return text
}

func (t *PlanTool) Detail() api.Textable {
	content := t.Str("content")
	if content == "" {
		content = t.Str("new_string")
	}
	if content == "" {
		return nil
	}
	c := api.NewCode(content, "markdown")
	return c
}

func (t *PlanTool) ExtractPath() string { return t.Rel(t.FilePath()) }

// planName is the human-facing plan label: the plan file's basename without its
// .md extension.
func planName(path string) string {
	if path == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(path), ".md")
}

func extractMarkdownTitle(content string) string {
	for _, line := range strings.SplitN(content, "\n", 20) {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

// ExitPlanTool

type ExitPlanTool struct{ BaseTool }

func (t *ExitPlanTool) Name() string     { return "Plan" }
func (t *ExitPlanTool) Category() string { return "" }

// FilePath returns the plan file the agent exited plan mode against, so plan
// writes and the exit row attribute to the same file.
func (t *ExitPlanTool) FilePath() string { return t.Str("planFilePath") }

func (t *ExitPlanTool) ExtractPath() string {
	if p := t.FilePath(); p != "" {
		return t.Rel(p)
	}
	return ""
}

func (t *ExitPlanTool) Pretty() api.Text {
	text := t.header(planIcon, "plan", planColor)
	if name := planName(t.FilePath()); name != "" {
		text = text.Append(" "+name, "text-cyan-400")
	}
	if t.Denied {
		text = text.Append(" ✗", "text-red-500")
	} else {
		text = text.Append(" ✓ approved", "text-green-500")
	}
	return text
}

func (t *ExitPlanTool) Detail() api.Textable {
	prompts, ok := t.Input["allowedPrompts"].([]any)
	if !ok || len(prompts) == 0 {
		return nil
	}
	text := clicky.Text("").Append("Allowed:", "")
	for _, p := range prompts {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		tool, _ := m["tool"].(string)
		prompt, _ := m["prompt"].(string)
		text = text.NewLine().Append("  • "+tool+": "+prompt, "")
	}
	return &text
}
