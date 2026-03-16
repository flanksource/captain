package main

import (
	"os"

	"github.com/flanksource/captain/pkg/cli"
	"github.com/flanksource/clicky"
	"github.com/spf13/cobra"
)

var version = "dev"

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
	clicky.AddNamedCommand("srt-generate", rootCmd, cli.SRTGenerateOptions{}, cli.RunSRTGenerate)

	aiCmd := &cobra.Command{Use: "ai", Short: "AI provider commands"}
	rootCmd.AddCommand(aiCmd)
	clicky.AddNamedCommand("prompt", aiCmd, cli.AIPromptOptions{}, cli.RunAIPrompt)
	clicky.AddNamedCommand("models", aiCmd, cli.AIModelsOptions{}, cli.RunAIModels)
	clicky.AddNamedCommand("test", aiCmd, cli.AITestOptions{}, cli.RunAITest)

	dodCmd := &cobra.Command{Use: "dod", Short: "Definition of Done — gate Claude's stop on passing commands"}
	rootCmd.AddCommand(dodCmd)
	clicky.AddNamedCommand("set", dodCmd, cli.DodSetOptions{}, cli.RunDodSet)
	clicky.AddNamedCommand("check", dodCmd, cli.DodCheckOptions{}, cli.RunDodCheck)
	clicky.AddNamedCommand("clear", dodCmd, cli.DodClearOptions{}, cli.RunDodClear)
	clicky.AddNamedCommand("status", dodCmd, cli.DodStatusOptions{}, cli.RunDodStatus)
	clicky.AddNamedCommand("run", dodCmd, cli.DodRunOptions{}, cli.RunDodRun)
	clicky.AddNamedCommand("install", dodCmd, cli.DodInstallOptions{}, cli.RunDodInstall)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
