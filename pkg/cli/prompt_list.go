package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/spf13/cobra"
)

// AttachPromptListOutput adds lookup locations to the generated prompt list.
func AttachPromptListOutput(root *cobra.Command) error {
	prompt, _, err := root.Find([]string{"prompt"})
	if err != nil || prompt == nil || prompt.Name() != "prompt" {
		return fmt.Errorf("prompt list output requires a generated prompt command")
	}
	list, _, err := prompt.Find([]string{"list"})
	if err != nil || list == nil || list.Name() != "list" {
		return fmt.Errorf("prompt list output requires a generated list command")
	}
	for _, cmd := range []*cobra.Command{prompt, list} {
		run := cmd.RunE
		if run == nil {
			return fmt.Errorf("prompt list output requires a handler for %s", cmd.CommandPath())
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			footer, err := promptLookupFooter(cmd.Context(), clicky.Flags.FormatOptions)
			if err != nil {
				return err
			}
			if err := run(cmd, args); err != nil {
				return err
			}
			_, err = io.WriteString(cmd.OutOrStdout(), footer)
			return err
		}
	}
	return nil
}

func promptLookupFooter(ctx context.Context, opts clicky.FormatOptions) (string, error) {
	if err := opts.ParseFormatSpec(); err != nil {
		return "", err
	}
	format := "pretty"
	if len(opts.Sinks) > 0 {
		format = ""
		for _, sink := range opts.Sinks {
			if sink.File == "" {
				format = sink.Format
			}
		}
	}
	if format != "pretty" && format != "markdown" {
		return "", nil
	}
	sources, err := buildPromptSources(ctx, promptSourceOptions{})
	if err != nil {
		return "", err
	}
	text := clicky.Text("").Add(clicky.Text("Lookup locations", "font-bold"))
	for _, source := range sources {
		location := source.Root
		if source.Kind == "embedded" {
			location = fmt.Sprintf("embedded: %s/ (%s)", source.WalkRoot, source.Label)
		}
		text = text.NewLine().Append("  - ").Append(location)
	}
	out, err := clicky.Format(text, clicky.FormatOptions{Format: format, NoColor: opts.NoColor})
	if err != nil {
		return "", err
	}
	return "\n" + strings.TrimRight(out, "\n") + "\n", nil
}
