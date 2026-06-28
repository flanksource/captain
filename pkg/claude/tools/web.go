package tools

import (
	"fmt"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

type WebFetchTool struct{ BaseTool }

func (t *WebFetchTool) Name() string         { return "WebFetch" }
func (t *WebFetchTool) FilePath() string     { return "" }
func (t *WebFetchTool) ExtractPath() string  { return "" }
func (t *WebFetchTool) Category() string     { return "" }
func (t *WebFetchTool) Detail() api.Textable { return nil }

func (t *WebFetchTool) Pretty() api.Text {
	text := t.header(icons.Cloud, "fetch", "text-blue-400 font-medium")
	text = text.Append(": ", "text-gray-500").Append(t.Str("url"), "text-blue-400 underline max-w-[tw-20ch]")
	if prompt := t.Str("prompt"); prompt != "" {
		text = text.Append(" Prompt: ", "text-gray-500").Append(prompt, "text-gray-400")
	}
	return text
}

type WebSearchTool struct{ BaseTool }

func (t *WebSearchTool) Name() string         { return "WebSearch" }
func (t *WebSearchTool) FilePath() string     { return "" }
func (t *WebSearchTool) ExtractPath() string  { return "" }
func (t *WebSearchTool) Category() string     { return "" }
func (t *WebSearchTool) Detail() api.Textable { return nil }

func (t *WebSearchTool) Pretty() api.Text {
	text := t.header(icons.Search, "search", "text-purple-400 font-medium")
	return text.Append(fmt.Sprintf(": %s", t.Str("query")), "text-gray-400 max-w-[tw-20ch]")
}
