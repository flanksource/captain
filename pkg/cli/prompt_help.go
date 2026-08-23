package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/spf13/cobra"
)

type promptHelpRenderOptions struct {
	LLMSession bool
	NoColor    bool
}

// AttachPromptHelp replaces the generated prompt root help while preserving
// the generated help for its list, CRUD, render, and run subcommands.
func AttachPromptHelp(root *cobra.Command) error {
	promptCmd, _, err := root.Find([]string{"prompt"})
	if err != nil {
		return fmt.Errorf("find prompt command: %w", err)
	}
	if promptCmd == nil || promptCmd.Name() != "prompt" {
		return fmt.Errorf("find prompt command: got %v", promptCmd)
	}

	defaultHelp := promptCmd.HelpFunc()
	promptCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd != promptCmd {
			defaultHelp(cmd, args)
			return
		}
		opts := promptHelpRenderOptions{
			LLMSession: CurrentEnvironmentSession() != nil,
			NoColor:    clicky.Flags.NoColor,
		}
		if err := writePromptHelp(cmd.OutOrStdout(), cmd, opts); err != nil {
			cmd.PrintErrf("render prompt help: %v\n", err)
		}
	})
	return nil
}

func renderPromptHelp(cmd *cobra.Command, opts promptHelpRenderOptions) (string, error) {
	format := "pretty"
	if opts.LLMSession {
		format = "markdown"
		opts.NoColor = true
	}
	formatOpts := clicky.FormatOptions{Format: format, NoColor: opts.NoColor}
	formatOpts.ResolveNoColor()
	out, err := clicky.Format(promptHelpGuide(cmd, !opts.LLMSession), formatOpts)
	if err != nil {
		return "", fmt.Errorf("format prompt help as %s: %w", format, err)
	}
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, nil
}

func writePromptHelp(w io.Writer, cmd *cobra.Command, opts promptHelpRenderOptions) error {
	out, err := renderPromptHelp(cmd, opts)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, out)
	return err
}

func promptHelpGuide(cmd *cobra.Command, styled bool) api.Text {
	doc := api.Text{}.
		Add(clicky.Heading(1, promptHelpText("Captain .prompt Files", styled, "font-bold text-blue-600"))).
		NewLine().NewLine().
		Add(clicky.Text("A .prompt file combines YAML frontmatter with a Handlebars body. The frontmatter configures the model and complete Captain run; the body supplies the rendered system and user messages.")).
		NewLine().NewLine().
		Add(promptHelpHeading("Start here", styled)).NewLine().
		Add(promptHelpBullets(styled,
			"Validate and inspect a rendered file without calling a model: captain prompt render <file.prompt>",
			"Execute it: captain prompt run <file.prompt>",
			"Print the generated JSON schemas, catalogs, enum values, and backend-specific cliArgs: captain prompt --schema",
		)).
		NewLine().NewLine().
		Add(promptHelpHeading("File format", styled)).NewLine().
		Add(clicky.Text("Put YAML frontmatter between the opening and closing --- lines. Everything after it is the Handlebars body; a body without frontmatter is also valid.")).
		NewLine().NewLine().
		Add(clicky.CodeBlock("yaml", strings.TrimSpace(promptHelpExample))).
		NewLine().NewLine().
		Add(promptHelpHeading("Templates and precedence", styled)).NewLine().
		Add(promptHelpBullets(styled,
			`Use {{role "system"}} and {{role "user"}} to split the Handlebars body into messages. Without role markers, dotprompt treats the body as its default message.`,
			"Use {{name}}, conditionals, loops, partials, and other Handlebars expressions in the body. The same variables may template YAML frontmatter, including schema constraints.",
			"Supply variables with --var/-V key=value or --vars JSON. input.default supplies defaults and input.schema describes or validates the input contract.",
			"Rendered body messages override prompt.user and prompt.system. config.maxOutputTokens, config.temperature, and config.reasoning override their spec-native equivalents.",
			"output.schema may be Picoschema or raw JSON Schema and becomes prompt.schemaJSON. A caller-provided Go output target takes precedence over the file schema.",
		)).
		NewLine().NewLine().
		Add(promptHelpHeading("Dotprompt frontmatter", styled)).NewLine().
		Add(promptHelpFieldReference(promptDotpromptHelpFields(), styled)).
		NewLine().NewLine().
		Add(promptHelpHeading("Captain run frontmatter", styled)).NewLine().
		Add(clicky.Text("All other frontmatter keys are decoded strictly into Captain's run spec. Unknown keys fail instead of being ignored.")).
		NewLine().NewLine().
		Add(promptHelpFieldReference(promptSpecHelpFields(), styled)).
		NewLine().NewLine().
		Add(promptHelpHeading("Prompt source selection", styled)).NewLine().
		Add(promptHelpBullets(styled,
			"Pass a prompt ID, catalog name, or .prompt path as the positional source.",
			"Use -p/--prompt for inline text. If neither a positional source nor -p is supplied, Captain reads the prompt body from stdin.",
			"Use runtimes[] for prompt-owned parallel defaults, or repeat -M/--multi-models at execution time to compare explicit runtime targets.",
		)).
		NewLine().NewLine().
		Add(promptHelpHeading("Command usage", styled)).NewLine().
		Add(clicky.CodeBlock("text", strings.TrimSpace(cmd.UsageString())))
	return doc
}

func promptHelpHeading(text string, styled bool) api.Heading {
	return clicky.Heading(2, promptHelpText(text, styled, "font-bold text-cyan-600"))
}

func promptHelpBullets(styled bool, items ...string) api.Text {
	out := api.Text{}
	for i, item := range items {
		if i > 0 {
			out = out.NewLine()
		}
		out = out.Add(promptHelpText("- ", styled, "text-muted").Append(item))
	}
	return out
}

func promptHelpFieldReference(fields []promptHelpField, styled bool) api.Textable {
	list := api.List{Bullet: promptHelpText("- ", styled, "text-muted"), MaxInline: 1}
	for _, field := range fields {
		list.Items = append(list.Items,
			promptHelpText(field.Path, styled, "font-mono text-yellow-600").Append(": ").Append(field.Meaning))
	}
	return list
}

func promptHelpText(content string, styled bool, style string) api.Text {
	if !styled {
		return clicky.Text(content)
	}
	return clicky.Text(content, style)
}
