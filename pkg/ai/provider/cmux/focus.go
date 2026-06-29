package cmux

import (
	"context"
	"fmt"
)

// FocusSession switches cmux to the workspace running the agent session for
// workDir, bringing its terminal to the front. It backs the dashboard's "focus
// session" control. agent selects between the per-agent workspaces
// (claude/codex); an empty agent falls back to the bare directory workspace name.
//
// It fails loudly when cmux is not running or no matching workspace exists (the
// session terminal was closed), rather than silently succeeding — the caller
// surfaces the reason to the user.
func FocusSession(ctx context.Context, client *Client, workDir, agent string) error {
	if client == nil {
		return fmt.Errorf("cmux client is required")
	}
	if workDir == "" {
		return fmt.Errorf("workDir is required")
	}
	if err := client.Available(ctx); err != nil {
		return err
	}
	name := AgentWorkspaceName(workDir, agent)
	ref, found, err := client.FindWorkspace(ctx, name, workDir)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no cmux workspace %q for %s; the session terminal may have been closed", name, workDir)
	}
	return client.SelectWorkspace(ctx, ref.String())
}
