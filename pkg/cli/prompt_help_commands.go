package cli

import (
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/spf13/cobra"
)

const PromptLookupHelp = `- Embedded examples and built-in prompts ship with the binary (read-only).
- Configured directories: prompts.dirs in ~/.captain.yaml; relative paths resolve from that config file's directory.
- Caller-provided directories and embedded filesystems are also included when registered by a host application.
- Project directory: ./.captain/prompts, used only when no configured or caller-provided writable source exists.
- User directory: ~/.config/captain/prompts, or $XDG_CONFIG_HOME/captain/prompts when XDG_CONFIG_HOME is set; always included.
- Links directly inside a prompt directory are read-only: linked .prompt files are included, and linked directories contribute only their immediate regular .prompt files (no subdirectories or further links).
- Directories are searched recursively for .prompt files. Local files shadow embedded prompts of the same name; multiple local matches are ambiguous, so use a prompt ID or explicit path.`

func promptCommandHelpGuide(cmd *cobra.Command, styled bool) api.Text {
	doc := api.Text{}.
		Add(clicky.Heading(1, promptHelpText(cmd.CommandPath(), styled, "font-bold text-blue-600"))).
		NewLine().NewLine().
		Add(clicky.Text(cmd.Short))
	if cmd.Long != "" && cmd.Long != cmd.Short {
		doc = doc.NewLine().NewLine().Add(clicky.Text(cmd.Long))
	}
	if help, ok := promptCommandHelp[cmd.Name()]; ok {
		doc = doc.NewLine().NewLine().Add(clicky.Text(help.Description)).
			NewLine().NewLine().Add(promptHelpHeading("Examples", styled)).NewLine().
			Add(clicky.CodeBlock("bash", help.Examples))
	}
	if cmd.Name() == "list" {
		doc = doc.NewLine().NewLine().Add(promptHelpHeading("Prompt lookup locations", styled)).
			NewLine().Add(clicky.Text(PromptLookupHelp))
	}
	return doc.NewLine().NewLine().
		Add(promptHelpHeading("Command usage and flags", styled)).NewLine().
		Add(clicky.CodeBlock("text", strings.TrimSpace(cmd.UsageString()))).
		NewLine().NewLine().
		Add(clicky.Text("See captain prompt --help for file format, variables, runtime fields, and source selection. Use --no-color for plain help; detected agent sessions receive Markdown."))
}

var promptCommandHelp = map[string]struct {
	Description string
	Examples    string
}{
	"list": {
		Description: "Discover embedded, built-in, configured, project and user prompts. Filter by name, description, model or path with --query, and by source with --source. Local files shadow embedded prompts of the same name.",
		Examples:    "captain prompt list\ncaptain prompt list --query review --source local\ncaptain prompt list --json",
	},
	"get": {
		Description: "Inspect a prompt's raw content, variables, schemas and version. Accepts a catalog ID, bare name or .prompt path. Invalid files remain readable with parseError so they can be repaired.",
		Examples:    "captain prompt get proposal-review\ncaptain prompt get .captain/prompts/review.prompt --json",
	},
	"create": {
		Description: "Create a local .prompt file from content. Choose a writable source with target and a relative destination with relPath or name. Embedded prompts are read-only; create a local copy to customize one. Content is validated before writing.",
		Examples:    `captain prompt create name=hello 'content=Hello {{name}}'`,
	},
	"update": {
		Description: "Replace a writable prompt's content after validation. Send the version returned by get as baseVersion to detect concurrent edits. Embedded and linked prompts are read-only; create a local copy instead.",
		Examples:    `captain prompt update .captain/prompts/hello.prompt 'content=Hello {{name}}!' baseVersion=VERSION_FROM_GET`,
	},
	"delete": {
		Description: "Delete a writable local prompt file. Embedded, built-in and linked prompts cannot be deleted. Removing a local override exposes the embedded prompt again.",
		Examples:    "captain prompt delete .captain/prompts/hello.prompt",
	},
	"render": {
		Description: "Preview the rendered messages, complete input spec, schemas and resolution trace without calling a model or running setup. Inspect validationError before execution. --preset replaces frontmatter presets; --no-presets clears them.",
		Examples:    "captain prompt render review.prompt -V target=pkg/cli --json\ncaptain prompt render --model agent:sonnet -p 'Explain {{topic}}' -V topic=permissions\ngit diff | captain prompt render review.prompt -V patch=- --json",
	},
	"run": {
		Description: "Render, validate and execute a prompt. Supply one positional source, inline --prompt/-p, or stdin. Inline text is literal unless variables or {{input}} enable templating. Repeat -M for parallel runtimes. Foreground single-runtime agent runs can broker questions when stdin and stderr are terminals.",
		Examples:    "captain prompt run --model agent:sonnet -p 'Explain this repository'\ncaptain prompt run review.prompt -V target=pkg/cli --access read-only\ngit diff | captain prompt run review.prompt -V patch=-\ncaptain prompt run review.prompt -M agent:sonnet -M agent:gpt-6.1-sol\ncaptain prompt run review.prompt --no-stream --json",
	},
	"observe": {
		Description: "Execute exactly one --runtime and emit a captain.observation/v1 JSON document for profiling and evaluation. This CLI-only action refuses --model, --mode, fallbacks, parallel matrices and workflow-backed prompts. Optional --mcp-config and --capture-kubernetes flags capture external tool traffic.",
		Examples:    "captain prompt observe --runtime agent:sonnet -p 'Summarize this repository'",
	},
}
