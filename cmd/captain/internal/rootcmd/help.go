package rootcmd

import (
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons/help"
	"github.com/spf13/cobra"
)

// InstallRootHelp appends commons' documentation of the runtime knobs captain
// inherits to the root command without changing subcommand help.
func InstallRootHelp(root *cobra.Command) {
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
