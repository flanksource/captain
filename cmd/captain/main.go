package main

import (
	"os"

	"github.com/flanksource/captain/pkg/cli"
	"github.com/flanksource/clicky"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "captain",
		Short:   "Claude Code analysis tools",
		Version: version,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			clicky.Flags.UseFlags()
		},
	}

	clicky.BindAllFlags(rootCmd.PersistentFlags(), "format")
	clicky.AddNamedCommand("history", rootCmd, cli.HistoryOptions{}, cli.RunHistory)
	clicky.AddNamedCommand("info", rootCmd, cli.InfoOptions{}, cli.RunInfo)
	clicky.AddNamedCommand("cost", rootCmd, cli.CostOptions{}, cli.RunCost)

	aiCmd := &cobra.Command{Use: "ai", Short: "AI provider commands"}
	rootCmd.AddCommand(aiCmd)
	clicky.AddNamedCommand("prompt", aiCmd, cli.AIPromptOptions{}, cli.RunAIPrompt)
	clicky.AddNamedCommand("models", aiCmd, cli.AIModelsOptions{}, cli.RunAIModels)
	clicky.AddNamedCommand("test", aiCmd, cli.AITestOptions{}, cli.RunAITest)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
