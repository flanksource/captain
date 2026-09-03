package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
)

type HookInstallOptions struct {
	Timeout int  `flag:"timeout" help:"Hook timeout in seconds" default:"120"`
	User    bool `flag:"user" help:"Install to ~/.claude/settings.json (user-global) instead of .claude/settings.json (project)" short:"u"`
}

type BashCheckOptions struct{}

func RunBashCheck(_ BashCheckOptions) (any, error) {
	if !claude.IsStdinPiped() {
		return nil, fmt.Errorf("bash-check must be called as a Claude Code PreToolUse hook (reads JSON from stdin)")
	}

	data, err := os.ReadFile("/dev/stdin")
	if err != nil {
		return nil, fmt.Errorf("reading stdin: %w", err)
	}

	var input claude.HookInput
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("parsing hook input: %w", err)
	}

	var toolInput bash.BashToolInput
	if err := json.Unmarshal(input.ToolInput, &toolInput); err != nil || toolInput.Command == "" {
		writeHookOutput(claude.HookOutput{Continue: true})
		return nil, nil
	}

	cwd, _ := os.Getwd()
	result := bash.NewScanner(cwd, nil).Scan(toolInput.Command)

	if !result.Allowed {
		writeHookOutput(claude.HookOutput{
			Continue: true,
			HookSpecificOutput: &claude.HookSpecificOutput{
				PermissionDecision:       claude.PermissionDeny,
				PermissionDecisionReason: result.Violations[0].Message,
			},
		})
		return nil, nil
	}

	writeHookOutput(claude.HookOutput{Continue: true})
	return nil, nil
}

func writeHookOutput(output claude.HookOutput) {
	out, _ := json.Marshal(output)
	fmt.Println(string(out))
}

func RunBashCheckInstall(opts HookInstallOptions) (any, error) {
	captainPath, err := os.Executable()
	if err != nil {
		captainPath = "captain"
	}

	target, err := resolveSettingsTarget(opts.User)
	if err != nil {
		return nil, err
	}

	hookCommand := fmt.Sprintf("%s hook bash-check", captainPath)
	result, err := installHook(target, "PreToolUse", "Bash", hookCommand, "hook bash-check", opts.Timeout)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func resolveSettingsTarget(userGlobal bool) (string, error) {
	if userGlobal {
		target := claude.GetClaudeHome() + "/settings.json"
		if _, err := os.Stat(target); os.IsNotExist(err) {
			return "", fmt.Errorf("settings file not found: %s", target)
		}
		return target, nil
	}

	target := ".claude/settings.json"
	if _, err := os.Stat(target); os.IsNotExist(err) {
		// fall back to user-global
		target = claude.GetClaudeHome() + "/settings.json"
	}
	return target, nil
}

// installHook idempotently ensures a hook command entry exists for the given event type.
// matcher is used for PreToolUse tool matching (e.g. "Bash"); pass "" for Stop hooks.
// existingCheck identifies the command entry Captain owns and may update.
func installHook(target, eventType, matcher, hookCommand, existingCheck string, timeout int) (string, error) {
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

	existingHooks, _ := hooks[eventType].([]any)
	action := "installed"
	updated := false
findExisting:
	for _, h := range existingHooks {
		m, ok := h.(map[string]any)
		if !ok {
			continue
		}
		for _, ih := range asSlice(m["hooks"]) {
			hook, _ := ih.(map[string]any)
			if cmd, _ := hook["command"].(string); strings.Contains(cmd, existingCheck) {
				existingTimeout, _ := hook["timeout"].(float64)
				if cmd == hookCommand && existingTimeout == float64(timeout) {
					return fmt.Sprintf("%s hook: already installed in %s", eventType, target), nil
				}
				hook["command"] = hookCommand
				hook["timeout"] = timeout
				action = "updated"
				updated = true
				break findExisting
			}
		}
	}

	if !updated {
		entry := map[string]any{
			"type":    "command",
			"command": hookCommand,
			"timeout": timeout,
		}
		matcherEntry := map[string]any{"hooks": []any{entry}}
		if matcher != "" {
			matcherEntry["matcher"] = matcher
		}

		hooks[eventType] = append(existingHooks, matcherEntry)
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(target, append(out, '\n'), 0644); err != nil {
		return "", fmt.Errorf("writing %s: %w", target, err)
	}
	return fmt.Sprintf("%s hook: %s in %s (%s)", eventType, action, target, hookCommand), nil
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}
