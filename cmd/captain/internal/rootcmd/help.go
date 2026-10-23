package rootcmd

import (
	"fmt"

	"github.com/flanksource/captain/pkg/cli"
	"github.com/flanksource/clicky"
	"github.com/flanksource/commons/help"
	"github.com/spf13/cobra"
)

// InstallRootHelp appends prompt locations and inherited runtime knobs to root help.
func InstallRootHelp(root *cobra.Command) {
	cobraHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		cobraHelp(cmd, args)
		if cmd != root {
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nPrompt lookup locations\n%s\n", cli.PromptLookupHelp)
		text := help.Help()
		out := text.ANSI()
		if clicky.Flags.NoColor {
			out = text.String()
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", out)
	})
}
