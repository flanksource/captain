package main

import (
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons/help"
	"github.com/spf13/cobra"
)

// installRootHelp appends commons' documentation of the runtime knobs captain
// inherits — log verbosity, HTTP wire logging, HAR capture and output
// formatting — to `captain --help`.
//
// Cobra resolves a command's help function by walking up to the root, so this
// one runs for every command; the block is emitted only when the target is the
// root itself, leaving subcommand help as cobra renders it and leaving the
// sandbox/container SetHelpFunc overrides untouched.
func installRootHelp(root *cobra.Command) {
	cobraHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		cobraHelp(cmd, args)
		if cmd != root {
			return
		}
		text := help.Help()
		out := text.ANSI()
		if clicky.Flags.NoColor {
			out = text.String()
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", out)
	})
}
