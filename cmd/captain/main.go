package main

import (
	"fmt"
	"os"
	"reflect"

	"github.com/flanksource/captain/pkg/cli"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/flags"
	"github.com/flanksource/clicky/mcp"
	"github.com/flanksource/commons/properties"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	rootCmd := &cobra.Command{
		Use:     "captain",
		Short:   "Search and analyze Claude Code tool use history",
		Long:    "Search Claude Code session history by tool, category, file path, or time range. When stdin is piped, transcripts can be streamed in directly (e.g. 'cat session.jsonl | captain'). All other commands are available as subcommands.",
		Version: version,
		// Cobra dumps the full usage block by default whenever any RunE
		// returns an error. For runtime errors (parse failures, network
		// errors, etc.) the help text is just noise and buries the real
		// message. Setting SilenceUsage on the root makes every subcommand
		// respect it (cobra walks up the tree), so a runtime error from any
		// command prints just the error.
		SilenceUsage: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			clicky.Flags.UseFlags()
			cli.EnableHTTPWireLogging()
		},
	}

	clicky.BindAllFlags(rootCmd.PersistentFlags(), "format")
	cli.BindDatabaseURLFlag(rootCmd.PersistentFlags())
	// Bind commons' -P/--properties flag so per-subsystem log levels and HTTP
	// wire logging can be toggled, e.g. -Plog.level.http=trace3.
	properties.BindFlags(rootCmd.PersistentFlags())

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

	changesCmd := clicky.AddNamedCommand("changes", rootCmd, cli.ChangesOptions{}, cli.RunChanges)
	changesCmd.Short = "List files modified by a session"
	changesCmd.Long = "List the files written or edited during a Claude Code or Codex session. Pass --session-id to target a specific session; otherwise the most recent session in the current directory is used."

	planCmd := clicky.AddNamedCommand("plan", rootCmd, cli.PlanOptions{}, cli.RunPlan)
	planCmd.Short = "Show the exit-plan-mode plan for a session"
	planCmd.Long = "Determine the plan file path and content for a Claude Code or Codex session. Pass a session ID (exact or prefix) to target a specific session; otherwise the most recent session with a plan in the current directory is used. Claude plans resolve to a ~/.claude/plans/<slug>.md file; Codex plans are inline update_plan checklists. Use --path to print only the plan file path."

	sessionsCmd := &cobra.Command{Use: "sessions", Short: "Browse Claude and Codex sessions"}
	rootCmd.AddCommand(sessionsCmd)
	clicky.AddNamedCommandWithContext("list", sessionsCmd, cli.SessionListOptions{}, cli.RunSessionList).Short = "List discovered sessions"
	clicky.AddNamedCommandWithContext("live", sessionsCmd, cli.SessionLiveOptions{}, cli.RunSessionLive).Short = "List sessions with live process health"
	clicky.AddNamedCommandWithContext("get", sessionsCmd, cli.SessionGetOptions{}, cli.RunSessionGet).Short = "Show a session transcript"

	psCmd := clicky.AddNamedCommandWithContext("ps", rootCmd, cli.PSOptions{}, cli.RunPS)
	psCmd.Short = "List live agent sessions (claude/codex) with session id, agents, and cmux surface"
	psCmd.Long = "Detect running claude/codex agent processes (via ps + lsof), resolve each one's session id, sub-agent ids, cmux surface (from CMUX_* env vars), and last activity, then augment with cached token/cost/context data. Only currently-active sessions are listed."
	clicky.AddNamedCommandWithContext("ps", sessionsCmd, cli.PSOptions{}, cli.RunPS).Short = psCmd.Short

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

	aiCmd := &cobra.Command{
		Use:   "ai",
		Short: "AI provider commands",
		Long: "AI provider commands.\n\n" +
			"Logging: increase application verbosity with -v/-vv or --log-level=debug. " +
			"To log HTTP requests/responses to the provider APIs (with sensitive headers " +
			"redacted), set -Plog.level.http=trace3 for headers and timing, or trace4 to " +
			"also include request/response bodies.",
	}
	rootCmd.AddCommand(aiCmd)
	clicky.AddNamedCommand("prompt", aiCmd, cli.AIPromptOptions{}, cli.RunAIPrompt)
	clicky.AddNamedCommand("agent", aiCmd, cli.AIAgentOptions{}, cli.RunAIAgent).Short = "Run an iterative agent with verifiers, worktree, and commit"
	clicky.AddNamedCommand("models", aiCmd, cli.AIModelsOptions{}, cli.RunAIModels)
	clicky.AddNamedCommand("test", aiCmd, cli.AITestOptions{}, cli.RunAITest)
	clicky.AddNamedCommand("fixture", aiCmd, cli.AIFixtureOptions{}, cli.RunAIFixture).Short = "Run a YAML fixture across multiple Claude configurations"

	whoamiCmd := clicky.AddNamedCommand("whoami", rootCmd, cli.WhoamiOptions{}, cli.RunWhoami)
	whoamiCmd.Short = "List agent adapters, auth methods, and available models"
	whoamiCmd.Long = "Show every AI agent adapter (API providers and CLI agents), how each is authenticated (API-key env var or CLI login), whether its CLI binary is installed, and the models each provider exposes via a live API call. Pass --models=false to skip the network probes, or --backend to inspect a single adapter."

	configureCmd := clicky.AddNamedCommand("configure", rootCmd, cli.ConfigureOptions{}, cli.RunConfigure)
	configureCmd.Short = "Interactive wizard to set default model, backend, budget, and safety toggles"
	configureCmd.Long = "Run an interactive form to configure ~/.captain.yaml. These defaults are applied to `captain ai prompt`, `captain ai test`, and other AI commands when corresponding flags are not passed."

	rootCmd.AddCommand(cli.NewServeCommand(version))

	attachmentsCmd := &cobra.Command{Use: "attachments", Short: "Manage durable prompt attachments"}
	rootCmd.AddCommand(attachmentsCmd)
	clicky.AddNamedCommand("gc", attachmentsCmd, cli.AttachmentsGCOptions{}, cli.RunAttachmentsGC).Short = "Remove old unreferenced attachments"

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

	monitorHookCmd := &cobra.Command{Use: "monitor", Short: "Session monitoring hooks (hooks-first session tracking)"}
	hookCmd.AddCommand(monitorHookCmd)
	monitorNotifyCmd := &cobra.Command{
		Use:   "notify",
		Short: "Forward one provider hook event to captain serve",
		Long: "Hook receiver for session monitoring: Claude Code lifecycle hooks pipe their JSON payload " +
			"on stdin, codex notify appends its payload as the final argument. The event is POSTed to the " +
			"running captain serve instance ($CAPTAIN_SERVER_URL or http://localhost:9020) with a 1s timeout " +
			"and always exits 0 — when serve is unreachable the event is dropped and the daily recon reconciles it.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			provider, _ := cmd.Flags().GetString("provider")
			url, _ := cmd.Flags().GetString("url")
			return cli.RunHookMonitorNotify(cli.HookMonitorNotifyOptions{Provider: provider, URL: url}, args)
		},
	}
	monitorNotifyCmd.Flags().String("provider", "claude", "Hook payload provider: claude or codex")
	monitorNotifyCmd.Flags().String("url", "", "Captain serve base URL (default $CAPTAIN_SERVER_URL or http://localhost:9020)")
	monitorHookCmd.AddCommand(monitorNotifyCmd)
	monitorInstallCmd := clicky.AddNamedCommand("install", monitorHookCmd, cli.HookMonitorInstallOptions{}, cli.RunHookMonitorInstall)
	monitorInstallCmd.Short = "Install session-monitoring hooks into ~/.claude/settings.json and ~/.codex/config.toml"

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

	cli.RegisterPromptEntity()
	clicky.GenerateCLI(rootCmd)
	if err := cli.AttachPromptSchemaFlag(rootCmd); err != nil {
		fmt.Fprintf(os.Stderr, "failed to attach prompt schema flag: %v\n", err)
		os.Exit(1)
	}

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
	// Clear the -v shorthand only if the mcp command registers a verbose flag;
	// newer clicky versions don't, and an unconditional Lookup(...).Shorthand
	// dereferences nil.
	if vf := mcpCmd.PersistentFlags().Lookup("verbose"); vf != nil {
		vf.Shorthand = ""
	}
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
		if filter := cli.HistoryTextFilterFromGlobal(clicky.Flags.Filter); filter != "" {
			opts.TextFilter = filter
			clicky.Flags.Filter = ""
		}
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
