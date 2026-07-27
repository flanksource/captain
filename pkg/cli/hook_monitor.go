package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/codexconfig"
	"github.com/flanksource/captain/pkg/monitor"
)

type HookMonitorNotifyOptions struct {
	Provider string // claude | codex
	URL      string // serve base URL override; default monitor.ServeBaseURL()
}

// hookNotifyTimeout bounds hook delivery so an agent turn is never held up:
// deliver within it or drop the event (recon reconciles drops).
const hookNotifyTimeout = time.Second

// RunHookMonitorNotify is the hook receiver both providers invoke: Claude Code
// pipes the payload on stdin, codex appends it as the final argv argument. It
// always succeeds with empty stdout — a hook failure must never block or slow
// an agent turn, and Claude injects UserPromptSubmit hook stdout as context.
func RunHookMonitorNotify(opts HookMonitorNotifyOptions, args []string) error {
	ev, err := readHookNotifyEvent(opts.Provider, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain hook monitor notify: %v\n", err)
		return nil
	}
	baseURL := opts.URL
	if baseURL == "" {
		baseURL = api.ServeBaseURL()
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookNotifyTimeout)
	defer cancel()
	if err := monitor.PostHookEvent(ctx, baseURL, ev); err != nil {
		// Degraded mode by design: captain serve is down or slow. The event is
		// dropped; startup/daily recon and the stale reaper converge the DB.
		fmt.Fprintf(os.Stderr, "captain hook monitor notify: %v\n", err)
	}
	return nil
}

type HookMonitorInstallOptions struct {
	Timeout int    `flag:"timeout" help:"Hook timeout in seconds" default:"10"`
	URL     string `flag:"url" help:"Captain serve base URL baked into the hook command (for non-default ports)"`
}

// monitorHookEvents are the Claude Code lifecycle events captain subscribes to
// for session monitoring: start/end bind and tear down the session, the middle
// three signal progress worth ingesting.
var monitorHookEvents = []claude.HookEventType{
	claude.HookEventSessionStart,
	claude.HookEventUserPromptSubmit,
	claude.HookEventStop,
	claude.HookEventSubagentStop,
	claude.HookEventSessionEnd,
}

// RunHookMonitorInstall installs the session-monitoring hooks user-wide:
// Claude Code lifecycle hooks in ~/.claude/settings.json and codex's notify
// program in ~/.codex/config.toml.
func RunHookMonitorInstall(opts HookMonitorInstallOptions) (any, error) {
	captainPath, err := os.Executable()
	if err != nil {
		captainPath = "captain"
	}
	urlSuffix := ""
	if opts.URL != "" {
		urlSuffix = " --url " + opts.URL
	}

	target, err := ensureUserClaudeSettings()
	if err != nil {
		return nil, err
	}
	claudeCommand := fmt.Sprintf("%s hook monitor notify --provider claude%s", captainPath, urlSuffix)
	var results []string
	for _, event := range monitorHookEvents {
		result, err := installHook(target, string(event), "", claudeCommand, "hook monitor notify --provider claude", opts.Timeout)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}

	codexArgv := []string{captainPath, "hook", "monitor", "notify", "--provider", "codex"}
	if opts.URL != "" {
		codexArgv = append(codexArgv, "--url", opts.URL)
	}
	codexResult, err := codexconfig.SetNotify(codexArgv)
	if err != nil {
		return nil, err
	}
	results = append(results, codexResult)

	results = append(results, "Events are delivered to a running `captain serve`; when it is down they are dropped and the daily recon backfills them.")
	return strings.Join(results, "\n"), nil
}

// ensureUserClaudeSettings returns the user-level settings.json path, creating
// an empty file when missing — monitoring hooks are user-level infrastructure
// and must be installable on a fresh machine.
func ensureUserClaudeSettings() (string, error) {
	target := filepath.Join(claude.GetClaudeHome(), "settings.json")
	if _, err := os.Stat(target); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", fmt.Errorf("ensure %s: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
			return "", fmt.Errorf("create %s: %w", target, err)
		}
	}
	return target, nil
}

func readHookNotifyEvent(provider string, args []string) (monitor.HookEvent, error) {
	switch provider {
	case "claude":
		if !claude.IsStdinPiped() {
			return monitor.HookEvent{}, fmt.Errorf("claude hook payload must be piped on stdin")
		}
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return monitor.HookEvent{}, fmt.Errorf("reading stdin: %w", err)
		}
		return monitor.ParseClaudeHookPayload(data)
	case "codex":
		return monitor.ParseCodexNotifyPayload(args)
	default:
		return monitor.HookEvent{}, fmt.Errorf("unknown hook provider %q", provider)
	}
}
