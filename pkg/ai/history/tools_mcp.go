package history

import (
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

type McpIconifyGetIcon struct {
	Set  string `json:"set"`
	Icon string `json:"icon"`
}

func (m McpIconifyGetIcon) ToolName() string { return "mcp__iconify__get_icon" }

func (m McpIconifyGetIcon) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "🎨", Iconify: "mdi:palette", Style: "muted"}).Append(" Iconify Get Icon", "text-pink-600 font-medium")
	if m.Set != "" && m.Icon != "" {
		text = text.Append(": ", "text-gray-600").Append(m.Set, "text-gray-700").Append("/", "text-gray-400").Append(m.Icon, "text-gray-800")
	}
	return text
}

type McpIconifySearchIcons struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

func (m McpIconifySearchIcons) ToolName() string { return "mcp__iconify__search_icons" }

func (m McpIconifySearchIcons) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Search).Append(" Iconify Search", "text-pink-600 font-medium")
	if m.Query != "" {
		text = text.Append(": ", "text-gray-600").Append(m.Query, "text-gray-800")
	}
	if m.Limit > 0 {
		text = text.Append(fmt.Sprintf(" (limit: %d)", m.Limit), "text-gray-500")
	}
	return text
}

type McpReactIconsGetLibraryIcons struct {
	LibraryPrefix string `json:"libraryPrefix"`
	Limit         int    `json:"limit,omitempty"`
}

func (m McpReactIconsGetLibraryIcons) ToolName() string {
	return "mcp__react-icons__get_library_icons"
}

func (m McpReactIconsGetLibraryIcons) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "⚛️", Iconify: "mdi:react", Style: "muted"}).Append(" React Icons Library", "text-cyan-600 font-medium")
	if m.LibraryPrefix != "" {
		text = text.Append(": ", "text-gray-600").Append(m.LibraryPrefix, "text-gray-800")
	}
	if m.Limit > 0 {
		text = text.Append(fmt.Sprintf(" (limit: %d)", m.Limit), "text-gray-500")
	}
	return text
}

type McpReactIconsSearchIcons struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

func (m McpReactIconsSearchIcons) ToolName() string { return "mcp__react-icons__search_icons" }

func (m McpReactIconsSearchIcons) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Search).Append(" React Icons Search", "text-cyan-600 font-medium")
	if m.Query != "" {
		text = text.Append(": ", "text-gray-600").Append(m.Query, "text-gray-800")
	}
	if m.Limit > 0 {
		text = text.Append(fmt.Sprintf(" (limit: %d)", m.Limit), "text-gray-500")
	}
	return text
}

type McpLucideSearchIcons struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

func (m McpLucideSearchIcons) ToolName() string { return "mcp__lucide__search_icons" }

func (m McpLucideSearchIcons) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Search).Append(" Lucide Search", "text-indigo-600 font-medium")
	if m.Query != "" {
		text = text.Append(": ", "text-gray-600").Append(m.Query, "text-gray-800")
	}
	if m.Limit > 0 {
		text = text.Append(fmt.Sprintf(" (limit: %d)", m.Limit), "text-gray-500")
	}
	return text
}

type McpIcons8GetIconSvg struct {
	IconId string `json:"icon_id"`
}

func (m McpIcons8GetIconSvg) ToolName() string { return "mcp__icons8mcp__get_icon_svg" }

func (m McpIcons8GetIconSvg) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "🎨", Iconify: "mdi:palette", Style: "muted"}).Append(" Icons8 Get SVG", "text-orange-600 font-medium")
	if m.IconId != "" {
		text = text.Append(": ", "text-gray-600").Append(m.IconId, "text-gray-800")
	}
	return text
}

type McpIcons8SearchIcons struct {
	Query    string `json:"query"`
	Amount   int    `json:"amount,omitempty"`
	Platform string `json:"platform,omitempty"`
}

func (m McpIcons8SearchIcons) ToolName() string { return "mcp__icons8mcp__search_icons" }

func (m McpIcons8SearchIcons) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Search).Append(" Icons8 Search", "text-orange-600 font-medium")
	if m.Query != "" {
		text = text.Append(": ", "text-gray-600").Append(m.Query, "text-gray-800")
	}
	if m.Platform != "" {
		text = text.Append(" [", "text-gray-400").Append(m.Platform, "text-gray-600").Append("]", "text-gray-400")
	}
	if m.Amount > 0 {
		text = text.Append(fmt.Sprintf(" (limit: %d)", m.Amount), "text-gray-500")
	}
	return text
}

type McpGeminiGenerateContent struct {
	UserPrompt string                   `json:"user_prompt"`
	Model      string                   `json:"model,omitempty"`
	Files      []map[string]any `json:"files,omitempty"`
}

func (m McpGeminiGenerateContent) ToolName() string { return "mcp__gemini__generate_content" }

func (m McpGeminiGenerateContent) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "✨", Iconify: "mdi:sparkles", Style: "muted"}).Append(" Gemini", "text-purple-600 font-medium")
	if m.Model != "" {
		text = text.Append(" [", "text-gray-400").Append(m.Model, "text-gray-600").Append("]", "text-gray-400")
	}
	if m.UserPrompt != "" {
		prompt := m.UserPrompt
		if len(prompt) > 60 {
			prompt = prompt[:57] + "..."
		}
		text = text.NewLine().Append(prompt, "text-gray-700")
	}
	if len(m.Files) > 0 {
		text = text.Append(fmt.Sprintf(" (%d files)", len(m.Files)), "text-gray-500")
	}
	return text
}

type McpPlaywrightBrowserClick struct {
	Element string `json:"element"`
	Ref     string `json:"ref"`
}

func (m McpPlaywrightBrowserClick) ToolName() string { return "mcp__playwright__browser_click" }

func (m McpPlaywrightBrowserClick) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "🖱️", Iconify: "mdi:cursor-default-click", Style: "muted"}).Append(" Browser Click", "text-blue-600 font-medium")
	if m.Element != "" {
		text = text.Append(": ", "text-gray-600").Append(m.Element, "text-gray-800")
	}
	return text
}

type McpPlaywrightBrowserClose struct{}

func (m McpPlaywrightBrowserClose) ToolName() string { return "mcp__playwright__browser_close" }

func (m McpPlaywrightBrowserClose) Pretty() api.Text {
	return clicky.Text("").Add(icons.Icon{Unicode: "❌", Iconify: "mdi:close", Style: "muted"}).Append(" Browser Close", "text-red-600 font-medium")
}

type McpPlaywrightBrowserConsoleMessages struct{}

func (m McpPlaywrightBrowserConsoleMessages) ToolName() string {
	return "mcp__playwright__browser_console_messages"
}

func (m McpPlaywrightBrowserConsoleMessages) Pretty() api.Text {
	return clicky.Text("").Add(icons.Icon{Unicode: "📋", Iconify: "codicon:output", Style: "muted"}).Append(" Browser Console", "text-gray-600 font-medium")
}

type McpPlaywrightBrowserEvaluate struct {
	Function string `json:"function"`
}

func (m McpPlaywrightBrowserEvaluate) ToolName() string {
	return "mcp__playwright__browser_evaluate"
}

func (m McpPlaywrightBrowserEvaluate) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "⚙️", Iconify: "codicon:code", Style: "muted"}).Append(" Browser Evaluate", "text-purple-600 font-medium")
	if m.Function != "" {
		preview := m.Function
		if len(preview) > 50 {
			preview = preview[:47] + "..."
		}
		text = text.NewLine().Append(preview, "text-gray-700")
	}
	return text
}

type McpPlaywrightBrowserNavigate struct {
	URL string `json:"url"`
}

func (m McpPlaywrightBrowserNavigate) ToolName() string {
	return "mcp__playwright__browser_navigate"
}

func (m McpPlaywrightBrowserNavigate) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "🌐", Iconify: "mdi:web", Style: "muted"}).Append(" Browser Navigate", "text-blue-600 font-medium")
	if m.URL != "" {
		text = text.Append(": ", "text-gray-600").Append(m.URL, "text-blue-700 underline")
	}
	return text
}

type McpPlaywrightBrowserNetworkRequests struct{}

func (m McpPlaywrightBrowserNetworkRequests) ToolName() string {
	return "mcp__playwright__browser_network_requests"
}

func (m McpPlaywrightBrowserNetworkRequests) Pretty() api.Text {
	return clicky.Text("").Add(icons.Icon{Unicode: "🌐", Iconify: "mdi:network", Style: "muted"}).Append(" Browser Network", "text-green-600 font-medium")
}

type McpPlaywrightBrowserPressKey struct {
	Key string `json:"key"`
}

func (m McpPlaywrightBrowserPressKey) ToolName() string {
	return "mcp__playwright__browser_press_key"
}

func (m McpPlaywrightBrowserPressKey) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "⌨️", Iconify: "mdi:keyboard", Style: "muted"}).Append(" Browser Press Key", "text-yellow-600 font-medium")
	if m.Key != "" {
		text = text.Append(": ", "text-gray-600").Append(m.Key, "text-gray-800")
	}
	return text
}

type McpPlaywrightBrowserSnapshot struct{}

func (m McpPlaywrightBrowserSnapshot) ToolName() string {
	return "mcp__playwright__browser_snapshot"
}

func (m McpPlaywrightBrowserSnapshot) Pretty() api.Text {
	return clicky.Text("").Add(icons.Icon{Unicode: "📸", Iconify: "mdi:camera", Style: "muted"}).Append(" Browser Snapshot", "text-purple-600 font-medium")
}

type McpPlaywrightBrowserTakeScreenshot struct {
	Filename string `json:"filename,omitempty"`
	FullPage bool   `json:"fullPage,omitempty"`
}

func (m McpPlaywrightBrowserTakeScreenshot) ToolName() string {
	return "mcp__playwright__browser_take_screenshot"
}

func (m McpPlaywrightBrowserTakeScreenshot) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "📷", Iconify: "mdi:camera", Style: "muted"}).Append(" Browser Screenshot", "text-indigo-600 font-medium")
	if m.Filename != "" {
		text = text.Append(": ", "text-gray-600").Append(m.Filename, "text-gray-800")
	}
	if m.FullPage {
		text = text.Append(" [full page]", "text-gray-500")
	}
	return text
}

type McpPlaywrightBrowserTripleClick struct {
	Element string `json:"element"`
	Ref     string `json:"ref"`
}

func (m McpPlaywrightBrowserTripleClick) ToolName() string {
	return "mcp__playwright__browser_triple_click"
}

func (m McpPlaywrightBrowserTripleClick) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "🖱️", Iconify: "mdi:cursor-default-click", Style: "muted"}).Append(" Browser Triple Click", "text-blue-600 font-medium")
	if m.Element != "" {
		text = text.Append(": ", "text-gray-600").Append(m.Element, "text-gray-800")
	}
	return text
}

type McpPlaywrightBrowserType struct {
	Element string `json:"element"`
	Ref     string `json:"ref"`
	Text    string `json:"text"`
}

func (m McpPlaywrightBrowserType) ToolName() string { return "mcp__playwright__browser_type" }

func (m McpPlaywrightBrowserType) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "⌨️", Iconify: "mdi:keyboard", Style: "muted"}).Append(" Browser Type", "text-green-600 font-medium")
	if m.Element != "" {
		text = text.Append(": ", "text-gray-600").Append(m.Element, "text-gray-800")
	}
	if m.Text != "" {
		text = text.Append(" → ", "text-gray-400").Append(fmt.Sprintf("\"%s\"", m.Text), "text-gray-700")
	}
	return text
}

type McpPlaywrightBrowserWaitFor struct {
	Time int `json:"time,omitempty"`
}

func (m McpPlaywrightBrowserWaitFor) ToolName() string {
	return "mcp__playwright__browser_wait_for"
}

func (m McpPlaywrightBrowserWaitFor) Pretty() api.Text {
	text := clicky.Text("").Add(icons.Icon{Unicode: "⏳", Iconify: "mdi:timer-sand", Style: "muted"}).Append(" Browser Wait", "text-orange-600 font-medium")
	if m.Time > 0 {
		text = text.Append(fmt.Sprintf(": %ds", m.Time), "text-gray-600")
	}
	return text
}

var McpTools = []Tool{
	McpIconifyGetIcon{},
	McpIconifySearchIcons{},
	McpReactIconsGetLibraryIcons{},
	McpReactIconsSearchIcons{},
	McpLucideSearchIcons{},
	McpIcons8GetIconSvg{},
	McpIcons8SearchIcons{},
	McpGeminiGenerateContent{},
	McpPlaywrightBrowserClick{},
	McpPlaywrightBrowserClose{},
	McpPlaywrightBrowserConsoleMessages{},
	McpPlaywrightBrowserEvaluate{},
	McpPlaywrightBrowserNavigate{},
	McpPlaywrightBrowserNetworkRequests{},
	McpPlaywrightBrowserPressKey{},
	McpPlaywrightBrowserSnapshot{},
	McpPlaywrightBrowserTakeScreenshot{},
	McpPlaywrightBrowserTripleClick{},
	McpPlaywrightBrowserType{},
	McpPlaywrightBrowserWaitFor{},
}
