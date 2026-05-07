package main

import (
	"fmt"
	"os"
	"reflect"

	"github.com/flanksource/captain/pkg/cli"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/flags"
	"github.com/flanksource/clicky/mcp"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	rootCmd := &cobra.Command{
		Use:     "captain",
		Short:   "Search and analyze Claude Code tool use history",
		Long:    "Search Claude Code session history by tool, category, file path, or time range. When stdin is piped, transcripts can be streamed in directly (e.g. 'cat session.jsonl | captain'). All other commands are available as subcommands.",
		Version: version,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			clicky.Flags.UseFlags()
		},
	}

	clicky.BindAllFlags(rootCmd.PersistentFlags(), "format")

	// Bind HistoryOptions directly on rootCmd so 'captain' IS 'captain history'.
	// All history flags (--tool, --category, --since, --limit, -f, ...) work
	// at the root. RunHistory auto-detects piped stdin when File is empty.
	bindHistoryAtRoot(rootCmd)

	// Backwards-compat alias: `captain history ...` keeps working by
	// stripping the leading "history" arg and re-dispatching through root.
	historyAlias := &cobra.Command{
		Use:                "history",
		Short:              "Alias for the root command (kept for backwards compatibility)",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(c *cobra.Command, args []string) error {
			rootCmd.SetArgs(args)
			return rootCmd.Execute()
		},
	}
	rootCmd.AddCommand(historyAlias)

	infoCmd := clicky.AddNamedCommand("info", rootCmd, cli.InfoOptions{}, cli.RunInfo)
	infoCmd.Short = "Show current Claude Code session and project info"
	infoCmd.Long = "Display metadata about the current Claude Code session including project root, active session ID, and configuration."

	costCmd := clicky.AddNamedCommand("cost", rootCmd, cli.CostOptions{}, cli.RunCost)
	costCmd.Short = "Show token usage and estimated costs"
	costCmd.Long = "Display token consumption (input, output, cache read/write) and estimated costs across Claude Code sessions."

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

	dodCmd := &cobra.Command{
		Use:   "dod",
		Short: "Definition of Done checks",
		Long:  "Manage Definition of Done gates that must pass before Claude Code stops. Use 'status' to check current gate state and 'check' to run the gate commands.",
	}
	rootCmd.AddCommand(dodCmd)
	clicky.AddNamedCommand("set", dodCmd, cli.DodSetOptions{}, cli.RunDodSet)

	dodCheckCmd := clicky.AddNamedCommand("check", dodCmd, cli.DodCheckOptions{}, cli.RunDodCheck)
	dodCheckCmd.Short = "Run Definition of Done gate checks"
	dodCheckCmd.Long = "Execute the configured DoD commands and report pass/fail status for each gate."

	clicky.AddNamedCommand("clear", dodCmd, cli.DodClearOptions{}, cli.RunDodClear)

	dodStatusCmd := clicky.AddNamedCommand("status", dodCmd, cli.DodStatusOptions{}, cli.RunDodStatus)
	dodStatusCmd.Short = "Show current Definition of Done gate status"
	dodStatusCmd.Long = "Display which DoD gates are configured and their last pass/fail state."

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

	mcpConfig := &mcp.Config{
		Name:    "captain",
		Version: version,
		Tools: mcp.ToolsConfig{
			AutoExpose: true,
			Exclude: []string{
				"^sandbox",
				"^projects",
				"^container",
				"^hook",
				"^ai",
				"^dod set",
				"^dod clear",
				"^dod run",
			},
		},
	}
	mcpCmd := mcp.NewCommandWithConfig(mcpConfig)
	mcpCmd.PersistentFlags().Lookup("verbose").Shorthand = ""
	rootCmd.AddCommand(mcpCmd)

	cmuxCmd := &cobra.Command{Use: "cmux", Short: "Cmux terminal multiplexer commands"}
	rootCmd.AddCommand(cmuxCmd)
	screenshotCmd := clicky.AddNamedCommand("screenshot", cmuxCmd, cli.CmuxScreenshotOptions{}, cli.RunCmuxScreenshot)
	screenshotCmd.Short = "Take a screenshot of the active browser surface"
	screenshotCmd.Long = "Capture a screenshot of the currently focused browser panel in cmux and copy the file path to the clipboard."

	portCmd := &cobra.Command{Use: "port", Short: "Port management commands"}
	rootCmd.AddCommand(portCmd)
	portKillCmd := clicky.AddNamedCommand("kill", portCmd, cli.PortKillOptions{}, cli.RunPortKill)
	portKillCmd.Short = "Kill the process listening on a TCP port"
	portKillCmd.Long = "Find the process bound to the specified TCP port using lsof and kill it with SIGKILL. Reports the process name and PID before killing."

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// bindHistoryAtRoot binds HistoryOptions directly to the root cobra command,
// so `captain` behaves exactly like `captain history` (same flags, same RunE).
// This mirrors clicky.AddNamedCommand but skips creating a child command.
func bindHistoryAtRoot(cmd *cobra.Command) {
	optsType := reflect.TypeFor[cli.HistoryOptions]()
	fieldInfos, err := flags.ParseStructFields(optsType)
	if err != nil {
		panic(fmt.Sprintf("failed to parse HistoryOptions fields: %v", err))
	}

	flagValues := make(map[string]*flags.FlagValue)
	var argsField *flags.FlagValue
	for _, info := range fieldInfos {
		fv := flags.BindFlag(cmd, info)
		if fv == nil {
			continue
		}
		key := info.FlagName
		if key == "" && info.IsArgs {
			key = flags.ARGS
		}
		flagValues[key] = fv
		if info.IsArgs {
			argsField = fv
		}
	}

	if argsField != nil {
		cmd.Args = cobra.MinimumNArgs(0)
	}

	cmd.RunE = func(c *cobra.Command, args []string) error {
		optsValue := reflect.New(optsType).Elem()
		for _, fv := range flagValues {
			argsToPass := []string(nil)
			if fv.IsArgs {
				argsToPass = args
			}
			if err := flags.AssignFieldValue(optsValue, fv, argsToPass, isStdinAvailable()); err != nil {
				return err
			}
		}

		opts := optsValue.Interface().(cli.HistoryOptions)
		result, err := cli.RunHistory(opts)
		if err != nil {
			return err
		}
		if err := clicky.Flags.ParseFormatSpec(); err != nil {
			return err
		}
		clicky.PrintAndWriteSinks(result, clicky.Flags.FormatOptions)
		return nil
	}
}

func isStdinAvailable() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) == 0
}
