package cli

import (
	"github.com/flanksource/clicky"
	"github.com/spf13/cobra"
)

func NewBrowserCommand() *cobra.Command {
	command := &cobra.Command{Use: "browser", Short: "Inspect agent-browser sessions and their owning agent session"}
	list := clicky.AddNamedCommandWithContext("list", command, BrowserListOptions{}, RunBrowserList)
	list.Aliases = []string{"ls"}
	list.Short = "List agent-browser sessions with CPU, memory, age, and the agent session that launched them"
	list.Long = "List every agent-browser session on this host. agent-browser records only a session name and its daemon's pid, so CPU and memory are summed across the daemon's whole process tree (the browser itself is an unrecorded child of the daemon), and the owning claude/codex session is resolved from the daemon's inherited environment, captain's launch-plugin record, or the process ancestry. Sessions whose daemon has exited are hidden unless --all is passed; captain never deletes agent-browser's sidecar files, which `agent-browser doctor` cleans up."

	install := clicky.AddNamedCommandWithContext("install", command, BrowserInstallOptions{}, RunBrowserInstall)
	install.Short = "Register captain as an agent-browser launch plugin"
	install.Long = "Merge captain into agent-browser's plugin list as a launch.mutate plugin, so every browser launched locally records which agent session opened it. Other settings and plugins in the configuration are preserved, and re-running changes nothing. Use --dry-run to inspect the merged document first."

	command.AddCommand(&cobra.Command{
		Use: "plugin", Short: "Serve the agent-browser plugin protocol on stdin/stdout (invoked by agent-browser, not by hand)",
		Args: cobra.NoArgs, SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			stateDir, err := BrowserLinkStateDir()
			if err != nil {
				return err
			}
			return RunBrowserPlugin(cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), stateDir)
		},
	})
	return command
}
