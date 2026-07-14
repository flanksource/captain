package tools

import (
	"strings"
	"testing"

	"github.com/flanksource/clicky/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMutedStyles(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Muted Pretty Styles Suite")
}

var _ = Describe("semantic muted pretty styles", func() {
	DescribeTable("renders secondary session text with the semantic muted style",
		func(render func() api.Textable, content string) {
			text := render()
			Expect(text).NotTo(BeNil())
			Expect(textStyleForContent(text, content)).To(ContainElement("text-muted"))
			Expect(text.HTML()).NotTo(MatchRegexp(`text-gray-(600|700)`))
			Expect(text.String()).To(ContainSubstring(strings.TrimSpace(content)))
		},
		Entry("assistant body", func() api.Textable {
			return (&AssistantTool{BaseTool: BaseTool{Input: map[string]any{"text": "assistant payload"}}}).Pretty()
		}, " assistant payload"),
		Entry("reasoning body", func() api.Textable {
			return (&ReasoningTool{BaseTool: BaseTool{Input: map[string]any{"text": "reasoning payload"}}}).Pretty()
		}, " reasoning payload"),
		Entry("session model", func() api.Textable {
			return (&SystemInitTool{BaseTool: BaseTool{Input: map[string]any{"model": "model-x"}}}).Pretty()
		}, " model-x"),
		Entry("hook stdout label", func() api.Textable {
			return (&HookResponseTool{BaseTool: BaseTool{Input: map[string]any{"stdout": "hook output"}}}).Detail()
		}, "stdout: "),
		Entry("parse error raw label", func() api.Textable {
			return (&ParseErrorTool{BaseTool: BaseTool{Input: map[string]any{"raw": "raw payload"}}}).Detail()
		}, "raw: "),
		Entry("turn label", func() api.Textable {
			return (&TurnDurationTool{}).Pretty()
		}, " turn"),
		Entry("away summary label", func() api.Textable {
			return (&AwaySummaryTool{}).Pretty()
		}, " away-summary"),
		Entry("session title", func() api.Textable {
			return (&SessionTitleTool{BaseTool: BaseTool{Input: map[string]any{"aiTitle": "session title"}}}).Pretty()
		}, " session title"),
		Entry("token total", func() api.Textable {
			return (&TokenCountTool{BaseTool: BaseTool{Input: map[string]any{"total_tokens": 12}}}).Pretty()
		}, " 12"),
		Entry("Codex command", func() api.Textable {
			return (&CodexExecCommandTool{BaseTool: BaseTool{Input: map[string]any{"command": "command --flag"}}}).Pretty()
		}, " command --flag"),
		Entry("user shell command", func() api.Textable {
			return (&UserShellCommandTool{BaseTool: BaseTool{Input: map[string]any{"command": "local --flag"}}}).Pretty()
		}, " local --flag"),
		Entry("MCP invocation", func() api.Textable {
			return (&MCPToolCallTool{BaseTool: BaseTool{Input: map[string]any{"invocation": map[string]any{"server": "server", "tool": "tool"}}}}).Pretty()
		}, " server.tool"),
		Entry("web search query", func() api.Textable {
			return (&WebSearchEventTool{BaseTool: BaseTool{Input: map[string]any{"query": "search terms"}}}).Pretty()
		}, " search terms"),
		Entry("image path", func() api.Textable {
			return (&ViewImageTool{BaseTool: BaseTool{Input: map[string]any{"path": "image.png"}}}).Pretty()
		}, " image.png"),
		Entry("guardian action", func() api.Textable {
			return (&GuardianAssessmentTool{BaseTool: BaseTool{Input: map[string]any{"action": map[string]any{"tool": "tool-name"}}}}).Pretty()
		}, " tool-name"),
		Entry("collaboration nickname", func() api.Textable {
			return (&CollabEventTool{BaseTool: BaseTool{Input: map[string]any{"nickname": "worker"}}}).Pretty()
		}, " worker"),
		Entry("budget used", func() api.Textable {
			return (&BudgetTool{BaseTool: BaseTool{Input: map[string]any{"used": 1.25}}}).Pretty()
		}, " used=$1.25"),
		Entry("pull request number", func() api.Textable {
			return (&PrLinkTool{BaseTool: BaseTool{Input: map[string]any{"prNumber": 42}}}).Pretty()
		}, " #42"),
		Entry("worktree name", func() api.Textable {
			return (&WorktreeStateTool{BaseTool: BaseTool{Input: map[string]any{"worktreeSession": map[string]any{"worktreeName": "feature-name"}}}}).Pretty()
		}, " feature-name"),
		Entry("relocated directory", func() api.Textable {
			return (&RelocatedTool{BaseTool: BaseTool{Input: map[string]any{"relocatedCwd": "repo"}}}).Pretty()
		}, " repo"),
		Entry("command stdout label", func() api.Textable {
			return (&CodexExecCommandTool{BaseTool: BaseTool{Input: map[string]any{"stdout": "command output"}}}).Detail()
		}, "stdout: "),
	)
})

func textStyleForContent(value api.Textable, content string) []string {
	switch text := value.(type) {
	case api.Text:
		return findTextStyle(text, content)
	case *api.Text:
		return findTextStyle(*text, content)
	default:
		return nil
	}
}

func findTextStyle(text api.Text, content string) []string {
	if text.Content == content {
		return strings.Fields(text.Style)
	}
	for _, child := range text.Children {
		if style := textStyleForContent(child, content); style != nil {
			return style
		}
	}
	return nil
}
