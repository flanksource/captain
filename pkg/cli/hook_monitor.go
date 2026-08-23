package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

const (
	// Lifecycle events tolerate a longer delivery attempt because recon can
	// reconstruct them when Captain is unavailable.
	hookNotifyTimeout = time.Second
	// Claude cancels slow status-line commands, so local estimate delivery gets
	// only a small best-effort window. Claude retries on its next refresh.
	statusLineNotifyTimeout = 10 * time.Millisecond
)

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

type HookMonitorStatuslineOptions struct {
	URL string
}

// RunHookMonitorStatusline forwards Claude Code's status-line stdin unchanged
// for composition with an existing status-line command, while best-effort
// reporting the cumulative CLI estimate to Captain.
func RunHookMonitorStatusline(opts HookMonitorStatuslineOptions) error {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain hook monitor statusline: reading stdin: %v\n", err)
		return nil
	}
	if _, err := os.Stdout.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "captain hook monitor statusline: forwarding stdin: %v\n", err)
	}
	ev, err := monitor.ParseClaudeStatusLinePayload(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain hook monitor statusline: %v\n", err)
		return nil
	}
	baseURL := opts.URL
	if baseURL == "" {
		baseURL = api.ServeBaseURL()
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusLineNotifyTimeout)
	defer cancel()
	// Delivery is best-effort; server failures must not alter status-line output.
	_ = monitor.PostHookEvent(ctx, baseURL, ev)
	return nil
}

type HookMonitorInstallOptions struct {
	Timeout     int    `flag:"timeout" help:"Hook timeout in seconds" default:"10"`
	URL         string `flag:"url" help:"Captain serve base URL baked into the hook command (for non-default ports)"`
	CaptureCost bool   `flag:"capture-cost" help:"Capture Claude's client-estimated session cost by configuring a custom status line"`
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
	if opts.CaptureCost {
		projectStatusLine, alreadyInstalled, err := higherPrecedenceClaudeStatusLine()
		if err != nil {
			return nil, err
		}
		if projectStatusLine != "" && !alreadyInstalled {
			return nil, fmt.Errorf("%s defines a higher-precedence Claude statusLine; compose cost capture there or remove it before installing user-wide capture", projectStatusLine)
		}
	}
	urlSuffix := ""
	if opts.URL != "" {
		urlSuffix = " --url " + opts.URL
	}

	target, err := ensureUserClaudeSettings()
	if err != nil {
		return nil, err
	}
	var results []string
	if opts.CaptureCost {
		statusLineCommand := shellQuote(captainPath) + " hook monitor statusline"
		if opts.URL != "" {
			statusLineCommand += " --url " + shellQuote(opts.URL)
		}
		statusLineResult, err := installClaudeStatusLine(target, statusLineCommand)
		if err != nil {
			return nil, err
		}
		results = append(results, statusLineResult)
	}
	claudeCommand := fmt.Sprintf("%s hook monitor notify --provider claude%s", captainPath, urlSuffix)
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
	if opts.CaptureCost {
		results = append(results, "Claude CLI cost estimate capture uses a custom status line; missed delivery retries on Claude's next refresh.")
	}
	return strings.Join(results, "\n"), nil
}

func higherPrecedenceClaudeStatusLine() (string, bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false, fmt.Errorf("resolve current project for Claude statusLine: %w", err)
	}
	root := claude.FindProjectRoot(cwd)
	for _, target := range []string{
		filepath.Join(root, ".claude", "settings.local.json"),
		filepath.Join(root, ".claude", "settings.json"),
	} {
		data, err := os.ReadFile(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("reading %s: %w", target, err)
		}
		var settings map[string]any
		if err := json.Unmarshal(data, &settings); err != nil {
			return "", false, fmt.Errorf("parsing %s: %w", target, err)
		}
		raw, exists := settings["statusLine"]
		if !exists {
			continue
		}
		statusLine, _ := raw.(map[string]any)
		command, _ := statusLine["command"].(string)
		return target, strings.Contains(command, "hook monitor statusline"), nil
	}
	return "", false, nil
}

// installClaudeStatusLine wraps an existing command as the downstream side of
// a pipeline. Captain passes stdin through byte-for-byte, preserving the user's
// rendered status line; without an existing command its output is discarded.
func installClaudeStatusLine(target, captainCommand string) (string, error) {
	data, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", target, err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return "", fmt.Errorf("parsing %s: %w", target, err)
	}
	action := "installed"
	if raw, exists := settings["statusLine"]; exists {
		statusLine, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("%s statusLine is not a command object; cannot compose session cost capture", target)
		}
		command, _ := statusLine["command"].(string)
		if marker := strings.Index(command, "hook monitor statusline"); marker >= 0 {
			var suffix string
			if separator := strings.Index(command, " | ("); separator > marker {
				suffix = command[separator:]
			} else if strings.HasSuffix(command, " >/dev/null") {
				suffix = " >/dev/null"
			} else {
				return "", fmt.Errorf("%s has an unrecognized Captain statusLine wrapper; cannot update session cost capture", target)
			}
			updatedCommand := captainCommand + suffix
			if command == updatedCommand {
				return fmt.Sprintf("StatusLine cost capture: already installed in %s", target), nil
			}
			statusLine["command"] = updatedCommand
			action = "updated"
		} else {
			if kind, _ := statusLine["type"].(string); kind != "command" || strings.TrimSpace(command) == "" {
				return "", fmt.Errorf("%s statusLine must have type=command and a command to compose session cost capture", target)
			}
			statusLine["command"] = captainCommand + " | (" + command + ")"
		}
	} else {
		settings["statusLine"] = map[string]any{
			"type": "command", "command": captainCommand + " >/dev/null",
		}
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode %s statusLine: %w", target, err)
	}
	if err := os.WriteFile(target, append(out, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", target, err)
	}
	return fmt.Sprintf("StatusLine cost capture: %s in %s", action, target), nil
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
