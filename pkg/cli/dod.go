package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/dod"
)

type DodSetOptions struct {
	SessionID string   `flag:"session-id" help:"Claude session ID (auto-detected from stdin if not set)" short:"s"`
	Workdir   string   `flag:"workdir" help:"Working directory for commands (defaults to cwd)" short:"w"`
	Timeout   int      `flag:"timeout" help:"Timeout per command in seconds" default:"300" short:"t"`
	Commands  []string `args:"true" help:"Commands to run as Definition of Done checks" required:"true"`
}

func RunDodSet(opts DodSetOptions) (any, error) {
	if len(opts.Commands) == 0 {
		return nil, fmt.Errorf("usage: captain dod set <commands...>")
	}

	sessionID := opts.SessionID
	if sessionID == "" {
		return nil, fmt.Errorf("--session-id is required (Claude provides this via hook stdin)")
	}

	workdir := opts.Workdir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}

	dodFile := &dod.DodFile{
		Commands:  opts.Commands,
		Workdir:   workdir,
		Timeout:   opts.Timeout,
		CreatedAt: time.Now().UTC(),
	}

	if err := dod.Write(sessionID, dodFile); err != nil {
		return nil, fmt.Errorf("writing dod: %w", err)
	}

	return map[string]any{
		"session_id": sessionID,
		"commands":   opts.Commands,
		"workdir":    workdir,
		"timeout":    opts.Timeout,
	}, nil
}

type DodCheckOptions struct{}

// StopHookInput is the JSON Claude Code passes to Stop hooks via stdin.
type StopHookInput struct {
	SessionID        string `json:"session_id"`
	TranscriptPath   string `json:"transcript_path,omitempty"`
	CWD              string `json:"cwd,omitempty"`
	StopHookActive   bool   `json:"stop_hook_active"`
	LastAssistantMsg string `json:"last_assistant_message,omitempty"`
	HookEventName    string `json:"hook_event_name,omitempty"`
}

func RunDodCheck(_ DodCheckOptions) (any, error) {
	if !claude.IsStdinPiped() {
		return nil, fmt.Errorf("dod check must be called as a Claude Code Stop hook (reads JSON from stdin)")
	}

	data, err := os.ReadFile("/dev/stdin")
	if err != nil {
		return nil, fmt.Errorf("reading stdin: %w", err)
	}

	var input StopHookInput
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("parsing hook input: %w", err)
	}

	if input.SessionID == "" {
		return nil, fmt.Errorf("no session_id in hook input")
	}

	if !dod.Exists(input.SessionID) {
		return nil, nil // no DoD set, allow stop
	}

	dodFile, err := dod.Read(input.SessionID)
	if err != nil {
		// corrupt file — allow stop rather than blocking forever
		fmt.Fprintf(os.Stderr, "warning: could not read dod file: %v\n", err)
		return nil, nil
	}

	// If stop_hook_active is true, Claude is already retrying.
	// Run checks again — if they still fail, block again (user can /dod-clear to escape).
	run := dod.RunCommands(dodFile)
	dodFile.LastRun = run
	_ = dod.Write(input.SessionID, dodFile) // persist last run results

	allPassed := true
	for _, r := range run.Results {
		if !r.Passed {
			allPassed = false
			break
		}
	}

	if allPassed {
		_ = dod.Delete(input.SessionID)
		return nil, nil // allow stop
	}

	// Block stop: write failure message to stderr (exit 2 triggers re-prompt)
	fmt.Fprint(os.Stderr, dod.FormatFailureMessage(run))
	os.Exit(2)
	return nil, nil // unreachable
}

type DodClearOptions struct {
	SessionID string `flag:"session-id" help:"Claude session ID" short:"s"`
}

func RunDodClear(opts DodClearOptions) (any, error) {
	if opts.SessionID == "" {
		return nil, fmt.Errorf("--session-id is required")
	}

	if !dod.Exists(opts.SessionID) {
		return "No DoD set for this session", nil
	}

	if err := dod.Delete(opts.SessionID); err != nil {
		return nil, fmt.Errorf("clearing dod: %w", err)
	}
	return "DoD cleared", nil
}

type DodStatusOptions struct {
	SessionID string `flag:"session-id" help:"Claude session ID" short:"s"`
}

func RunDodStatus(opts DodStatusOptions) (any, error) {
	if opts.SessionID == "" {
		return nil, fmt.Errorf("--session-id is required")
	}

	if !dod.Exists(opts.SessionID) {
		return "No DoD set for this session", nil
	}

	dodFile, err := dod.Read(opts.SessionID)
	if err != nil {
		return nil, err
	}

	return dodFile, nil
}

type DodRunOptions struct {
	SessionID string `flag:"session-id" help:"Claude session ID" short:"s"`
}

func RunDodRun(opts DodRunOptions) (any, error) {
	if opts.SessionID == "" {
		return nil, fmt.Errorf("--session-id is required")
	}

	if !dod.Exists(opts.SessionID) {
		return nil, fmt.Errorf("no DoD set for session %s", opts.SessionID)
	}

	dodFile, err := dod.Read(opts.SessionID)
	if err != nil {
		return nil, err
	}

	run := dod.RunCommands(dodFile)
	dodFile.LastRun = run
	_ = dod.Write(opts.SessionID, dodFile)

	return run, nil
}

type DodInstallOptions struct {
	Timeout int  `flag:"timeout" help:"Hook timeout in seconds" default:"120"`
	Global  bool `flag:"global" help:"Install to global ~/.claude/settings.json instead of project-local" short:"g"`
}

func RunDodInstall(opts DodInstallOptions) (any, error) {
	captainPath, err := os.Executable()
	if err != nil {
		captainPath = "captain"
	}

	var results []string

	// 1. Install Stop hook
	hookResult, err := installStopHook(captainPath, opts)
	if err != nil {
		return nil, err
	}
	results = append(results, hookResult)

	// 2. Install skill files
	skillResult, err := installSkills()
	if err != nil {
		return nil, err
	}
	results = append(results, skillResult)

	return strings.Join(results, "\n"), nil
}

func installStopHook(captainPath string, opts DodInstallOptions) (string, error) {
	target := ".claude/settings.json"
	if opts.Global {
		target = claude.GetClaudeHome() + "/settings.json"
	}
	if _, err := os.Stat(target); os.IsNotExist(err) {
		if !opts.Global {
			target = claude.GetClaudeHome() + "/settings.json"
		} else {
			return "", fmt.Errorf("settings file not found: %s", target)
		}
	}

	data, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", target, err)
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return "", fmt.Errorf("parsing %s: %w", target, err)
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
		settings["hooks"] = hooks
	}

	hookCommand := fmt.Sprintf("%s dod check", captainPath)

	// Check if already installed
	stopHooks, _ := hooks["Stop"].([]any)
	for _, h := range stopHooks {
		matcher, ok := h.(map[string]any)
		if !ok {
			continue
		}
		innerHooks, _ := matcher["hooks"].([]any)
		for _, ih := range innerHooks {
			hook, _ := ih.(map[string]any)
			if cmd, _ := hook["command"].(string); strings.Contains(cmd, "dod check") {
				return fmt.Sprintf("Stop hook: already installed in %s", target), nil
			}
		}
	}

	stopHooks = append(stopHooks, map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": hookCommand,
				"timeout": opts.Timeout,
			},
		},
	})
	hooks["Stop"] = stopHooks

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(target, append(out, '\n'), 0644); err != nil {
		return "", fmt.Errorf("writing %s: %w", target, err)
	}

	return fmt.Sprintf("Stop hook: installed in %s (%s)", target, hookCommand), nil
}

func installSkills() (string, error) {
	skillsDir := filepath.Join(claude.GetClaudeHome(), "skills", "captain-dod")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		return "", fmt.Errorf("creating skills dir: %w", err)
	}

	skills := map[string]string{
		"dod.md":        dod.SkillDod,
		"dod-clear.md":  dod.SkillDodClear,
		"dod-status.md": dod.SkillDodStatus,
		"dod-run.md":    dod.SkillDodRun,
	}

	installed := 0
	for name, content := range skills {
		path := filepath.Join(skillsDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return "", fmt.Errorf("writing skill %s: %w", name, err)
		}
		installed++
	}

	return fmt.Sprintf("Skills: installed %d skill files to %s", installed, skillsDir), nil
}
