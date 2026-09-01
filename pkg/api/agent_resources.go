package api

// This file is the declared answer to "what can the *selected* agent actually
// see?".
//
// The permission catalog used to answer that question once, for everyone: it
// served claude's built-in tool names, claude's `.mcp.json`/`~/.claude.json`
// servers, and every skill directory on the machine — to a codex run, a gemini
// run, and an API run alike. None of those agents can name a tool called `Read`,
// none of them read `~/.claude.json`, and codex's own `[mcp_servers]` block in
// `~/.codex/config.toml` was never read at all. The catalog was a picture of one
// agent presented as a picture of all of them.
//
// The two tables below fix that by declaring, per agent, the vocabulary it
// speaks and the files it reads. Like permission_capabilities.go, this is data a
// reviewer can diff a row of — adding an agent means adding rows, not adding a
// switch to a discovery function. It lives in pkg/api rather than
// pkg/api/registry for the same reason that table does: the vocabulary it is
// keyed by (ResourceKind, ToolPolicy, Backend) lives here.
//
// Nothing here touches the filesystem. These are declarations of *where* to
// look; pkg/cli/permission_catalog.go is the only reader, so a new agent needs a
// new reader only when it brings a genuinely new file format.

// AgentTool is one built-in tool an agent CLI ships with — the names a per-tool
// policy may legitimately mention for ProvenanceAgent.
type AgentTool struct {
	Name        string
	Group       string
	Description string
	// Default is the policy a picker should seed the tool with. Tools that run
	// commands or reach the network default to ask; the rest to auto.
	Default ToolPolicy
}

// AgentResourceScope is the root a source path is resolved against.
type AgentResourceScope string

const (
	// AgentScopeWorkspace roots a path at the run's working directory.
	AgentScopeWorkspace AgentResourceScope = "workspace"
	// AgentScopeHome roots a path at the user's home directory.
	AgentScopeHome AgentResourceScope = "home"
)

// AgentResourceFormat is how one source file or directory yields items.
type AgentResourceFormat string

const (
	// SourceFormatMCPJSON is a JSON object with an "mcpServers" map, the shape
	// claude's .mcp.json/~/.claude.json and gemini's settings.json both use.
	SourceFormatMCPJSON AgentResourceFormat = "mcp-servers-json"
	// SourceFormatMCPTOML is codex's config.toml, whose servers are declared as
	// [mcp_servers.<name>] tables.
	SourceFormatMCPTOML AgentResourceFormat = "mcp-servers-toml"
	// SourceFormatPluginsJSON is claude's installed_plugins.json, a JSON object
	// with a "plugins" map.
	SourceFormatPluginsJSON AgentResourceFormat = "plugins-json"
	// SourceFormatChildDirs treats each child directory as one item.
	SourceFormatChildDirs AgentResourceFormat = "child-dirs"
	// SourceFormatDirectory treats the directory itself as a single item — the
	// shape captain passes to --plugin-dir, where the directory is the unit.
	SourceFormatDirectory AgentResourceFormat = "directory"
)

// AgentResourceSource is one place an agent CLI discovers ambient resources.
type AgentResourceSource struct {
	Kind   ResourceKind
	Scope  AgentResourceScope
	Path   string
	Format AgentResourceFormat
	// Source is the provenance label served to the UI: which config owns the
	// item, so a user can tell a workspace server from a user-level one.
	Source string
	// Group is the heading the items are filed under. It is per-source rather
	// than per-kind because gemini calls its plugins "extensions", and calling
	// them "Plugins" in its own picker would be captain's word, not gemini's.
	Group string
	// ID, when set, is the literal id for a SourceFormatDirectory item — the
	// directory is the unit, and the id is what a permissions block names.
	ID string
}

// AgentToolsFor returns the built-in tool vocabulary for a runtime, empty for
// the API mode: it calls a model over HTTP and ships no agent tools at all, so
// there is nothing for a per-tool policy to name.
func AgentToolsFor(p *ModelProvider, mode RuntimeMode) []AgentTool {
	if p == nil || mode.Kind() != "cli" {
		return nil
	}
	return agentTools[p.AgentName]
}

// AgentResourceSourcesFor returns the discovery sources for a runtime, in
// canonical ResourceKind order. The API mode loads no ambient MCP servers,
// skills or plugins, so it gets none.
func AgentResourceSourcesFor(p *ModelProvider, mode RuntimeMode) []AgentResourceSource {
	if p == nil || mode.Kind() != "cli" {
		return nil
	}
	sources := agentResourceSources[p.AgentName]
	out := make([]AgentResourceSource, 0, len(sources))
	for _, kind := range AllResourceKinds() {
		for _, source := range sources {
			if source.Kind == kind {
				out = append(out, source)
			}
		}
	}
	return out
}

// agentToolNames projects a tool table onto the bare name list the permission
// capability matrix declares, so the two can never disagree about which tools
// an agent has.
func agentToolNames(tools []AgentTool) []string {
	out := make([]string, len(tools))
	for i, tool := range tools {
		out[i] = tool.Name
	}
	return out
}

// --- tool vocabularies ----------------------------------------------------

var agentTools = map[string][]AgentTool{
	"claude": {
		{Name: "Bash", Group: "Shell", Description: "Run shell commands.", Default: ToolPolicyAsk},
		{Name: "Edit", Group: "Files", Description: "Apply targeted file edits.", Default: ToolPolicyAuto},
		{Name: "Glob", Group: "Files", Description: "Find files by glob pattern.", Default: ToolPolicyAuto},
		{Name: "Grep", Group: "Files", Description: "Search file contents.", Default: ToolPolicyAuto},
		{Name: "MultiEdit", Group: "Files", Description: "Apply several edits to one file.", Default: ToolPolicyAuto},
		{Name: "Read", Group: "Files", Description: "Read files from the workspace.", Default: ToolPolicyAuto},
		{Name: "TodoWrite", Group: "Planning", Description: "Track task progress.", Default: ToolPolicyAuto},
		{Name: "WebFetch", Group: "Web", Description: "Fetch a web page.", Default: ToolPolicyAsk},
		{Name: "WebSearch", Group: "Web", Description: "Search the web.", Default: ToolPolicyAsk},
		{Name: "Write", Group: "Files", Description: "Write a new file.", Default: ToolPolicyAuto},
	},
	// codex's vocabulary is taken from the codex→claude normalisation table in
	// pkg/ai/history, which is the only accurate list in the tree. Note that
	// there is no separate read tool: codex reads through `shell`.
	"codex": {
		{Name: "apply_patch", Group: "Files", Description: "Apply a patch to workspace files.", Default: ToolPolicyAuto},
		{Name: "close_agent", Group: "Agents", Description: "Close a spawned sub-agent.", Default: ToolPolicyAuto},
		{Name: "request_user_input", Group: "Interaction", Description: "Ask the user a question mid-run.", Default: ToolPolicyAuto},
		{Name: "resume_agent", Group: "Agents", Description: "Resume a previously spawned sub-agent.", Default: ToolPolicyAsk},
		{Name: "send_input", Group: "Agents", Description: "Send input to a running sub-agent.", Default: ToolPolicyAuto},
		{Name: "shell", Group: "Shell", Description: "Run shell commands; also how codex reads files.", Default: ToolPolicyAsk},
		{Name: "spawn_agent", Group: "Agents", Description: "Spawn a sub-agent.", Default: ToolPolicyAsk},
		{Name: "update_plan", Group: "Planning", Description: "Record or revise the run plan.", Default: ToolPolicyAuto},
		{Name: "wait", Group: "Agents", Description: "Wait before continuing.", Default: ToolPolicyAuto},
		{Name: "wait_agent", Group: "Agents", Description: "Wait for a sub-agent to finish.", Default: ToolPolicyAuto},
	},
	"gemini": {
		{Name: "google_web_search", Group: "Web", Description: "Search the web with Google.", Default: ToolPolicyAsk},
		{Name: "read_file", Group: "Files", Description: "Read a file from the workspace.", Default: ToolPolicyAuto},
		{Name: "replace", Group: "Files", Description: "Replace text within a file.", Default: ToolPolicyAuto},
		{Name: "run_shell_command", Group: "Shell", Description: "Run shell commands.", Default: ToolPolicyAsk},
		{Name: "write_file", Group: "Files", Description: "Write a file.", Default: ToolPolicyAuto},
	},
}

// --- discovery sources ----------------------------------------------------

var agentResourceSources = map[string][]AgentResourceSource{
	"claude": {
		{Kind: ResourceKindMCP, Scope: AgentScopeWorkspace, Path: ".mcp.json",
			Format: SourceFormatMCPJSON, Source: "workspace", Group: "MCP"},
		{Kind: ResourceKindMCP, Scope: AgentScopeHome, Path: ".claude.json",
			Format: SourceFormatMCPJSON, Source: "claude", Group: "MCP"},
		{Kind: ResourceKindSkills, Scope: AgentScopeWorkspace, Path: ".skills",
			Format: SourceFormatDirectory, Source: "workspace", Group: "Skills", ID: "$CWD/.skills"},
		{Kind: ResourceKindSkills, Scope: AgentScopeHome, Path: ".claude/skills",
			Format: SourceFormatChildDirs, Source: "claude", Group: "Skills"},
		// ~/.agents/skills is the cross-agent directory, and it is listed for
		// claude alone because the claude CLI's --plugin-dir is the only way captain
		// can actually load it. Offering it to codex or gemini would be the same
		// lie this file exists to end.
		{Kind: ResourceKindSkills, Scope: AgentScopeHome, Path: ".agents/skills",
			Format: SourceFormatChildDirs, Source: "agents", Group: "Skills"},
		{Kind: ResourceKindPlugins, Scope: AgentScopeHome, Path: ".claude/plugins/installed_plugins.json",
			Format: SourceFormatPluginsJSON, Source: "claude", Group: "Plugins"},
	},
	"codex": {
		{Kind: ResourceKindMCP, Scope: AgentScopeHome, Path: ".codex/config.toml",
			Format: SourceFormatMCPTOML, Source: "codex", Group: "MCP"},
		{Kind: ResourceKindSkills, Scope: AgentScopeHome, Path: ".codex/skills",
			Format: SourceFormatChildDirs, Source: "codex", Group: "Skills"},
		{Kind: ResourceKindPlugins, Scope: AgentScopeHome, Path: ".codex/plugins",
			Format: SourceFormatChildDirs, Source: "codex", Group: "Plugins"},
	},
	"gemini": {
		{Kind: ResourceKindMCP, Scope: AgentScopeWorkspace, Path: ".gemini/settings.json",
			Format: SourceFormatMCPJSON, Source: "workspace", Group: "MCP"},
		{Kind: ResourceKindMCP, Scope: AgentScopeHome, Path: ".gemini/settings.json",
			Format: SourceFormatMCPJSON, Source: "gemini", Group: "MCP"},
		{Kind: ResourceKindSkills, Scope: AgentScopeHome, Path: ".gemini/skills",
			Format: SourceFormatChildDirs, Source: "gemini", Group: "Skills"},
		// Gemini's plugin equivalent is an extension, and the group says so.
		{Kind: ResourceKindPlugins, Scope: AgentScopeHome, Path: ".gemini/extensions",
			Format: SourceFormatChildDirs, Source: "gemini", Group: "Extensions"},
	},
}
