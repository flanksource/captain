package main

import (
	"fmt"
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
	sandboxCmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Sandbox configuration tools",
	}
	sandboxCmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		fmt.Fprint(os.Stderr, cli.SandboxHelp().ANSI())
	})
	rootCmd.AddCommand(sandboxCmd)
	clicky.AddNamedCommand("generate", sandboxCmd, cli.SRTGenerateOptions{}, cli.RunSRTGenerate).Short = "Generate sandbox-runtime config"
	clicky.AddNamedCommand("presets", sandboxCmd, cli.SandboxPresetsOptions{}, cli.RunSandboxPresets).Short = "List available sandbox-runtime presets"

	aiCmd := &cobra.Command{Use: "ai", Short: "AI provider commands"}
	rootCmd.AddCommand(aiCmd)
	clicky.AddNamedCommand("prompt", aiCmd, cli.AIPromptOptions{}, cli.RunAIPrompt)
	clicky.AddNamedCommand("models", aiCmd, cli.AIModelsOptions{}, cli.RunAIModels)
	clicky.AddNamedCommand("test", aiCmd, cli.AITestOptions{}, cli.RunAITest)
	clicky.AddNamedCommand("fixture", aiCmd, cli.AIFixtureOptions{}, cli.RunAIFixture).Short = "Run a YAML fixture across multiple Claude configurations"

	dodCmd := &cobra.Command{Use: "dod", Short: "Definition of Done — gate Claude's stop on passing commands"}
	rootCmd.AddCommand(dodCmd)
	clicky.AddNamedCommand("set", dodCmd, cli.DodSetOptions{}, cli.RunDodSet)
	clicky.AddNamedCommand("check", dodCmd, cli.DodCheckOptions{}, cli.RunDodCheck)
	clicky.AddNamedCommand("clear", dodCmd, cli.DodClearOptions{}, cli.RunDodClear)
	clicky.AddNamedCommand("status", dodCmd, cli.DodStatusOptions{}, cli.RunDodStatus)
	clicky.AddNamedCommand("run", dodCmd, cli.DodRunOptions{}, cli.RunDodRun)

	hookCmd := &cobra.Command{Use: "hook", Short: "Claude Code hook commands"}
	rootCmd.AddCommand(hookCmd)
	bashCheckCmd := &cobra.Command{Use: "bash-check", Short: "Scan bash command for violations (PreToolUse hook)", RunE: func(cmd *cobra.Command, args []string) error {
		_, err := cli.RunBashCheck(cli.BashCheckOptions{})
		return err
	}}
	hookCmd.AddCommand(bashCheckCmd)
	clicky.AddNamedCommand("install", bashCheckCmd, cli.HookInstallOptions{}, cli.RunBashCheckInstall)
	dodHookCmd := &cobra.Command{Use: "dod", Short: "Definition of Done hook"}
	hookCmd.AddCommand(dodHookCmd)
	clicky.AddNamedCommand("install", dodHookCmd, cli.HookInstallOptions{}, cli.RunDodInstall)

	projectsCmd := &cobra.Command{Use: "projects", Short: "Manage Claude Code project sessions"}
	rootCmd.AddCommand(projectsCmd)
	clicky.AddNamedCommand("list", projectsCmd, cli.ProjectsListOptions{}, cli.RunProjectsList)
	clicky.AddNamedCommand("clean", projectsCmd, cli.ProjectsCleanOptions{}, cli.RunProjectsClean)

	containerCmd := &cobra.Command{
		Use:   "container",
		Short: "Container sandbox builder for Claude Code",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.RunContainerTUI()
		},
	}
	containerCmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		fmt.Fprint(os.Stderr, cli.ContainerHelp().ANSI())
	})
	rootCmd.AddCommand(containerCmd)
	clicky.AddNamedCommand("list", containerCmd, cli.ContainerListOptions{}, cli.RunContainerList).Short = "List discovered components"
	clicky.AddNamedCommand("generate", containerCmd, cli.ContainerGenerateOptions{}, cli.RunContainerGenerate).Short = "Generate Dockerfile and sandbox config"
	clicky.AddNamedCommand("build", containerCmd, cli.ContainerBuildOptions{}, cli.RunContainerBuild).Short = "Build container image"
	clicky.AddNamedCommand("run", containerCmd, cli.ContainerRunOptions{}, cli.RunContainerRun).Short = "Run container sandbox"

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
