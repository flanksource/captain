package agentbrowser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"

	"github.com/flanksource/captain/pkg/agentsandbox"
)

// projectAgentBrowserConfig is agent-browser's project-level config file; the
// global one lives at <socket dir>/config.json.
const projectAgentBrowserConfig = "agent-browser.json"

type BrowserInstallOptions struct {
	Global  bool   `flag:"global" help:"Write the user-level ~/.agent-browser/config.json instead of ./agent-browser.json" default:"true"`
	Config  string `flag:"config" help:"Write an explicit agent-browser config file path"`
	Command string `flag:"command" help:"Command agent-browser should run for the plugin (defaults to this captain binary)"`
	Sandbox bool   `flag:"sandbox" help:"Register captain's browser state directory as writable in the Claude and codex sandboxes" default:"true"`
	DryRun  bool   `flag:"dry-run" help:"Print the merged configuration instead of writing it"`
}

type BrowserInstallResult struct {
	Path     string                `json:"path" pretty:"label=Config"`
	Command  string                `json:"command" pretty:"label=Command"`
	StateDir string                `json:"stateDir" pretty:"label=State dir"`
	Changed  bool                  `json:"changed" pretty:"label=Changed"`
	Sandbox  []agentsandbox.Change `json:"sandbox,omitempty" pretty:"label=Sandbox"`
	Config   string                `json:"config,omitempty"`
}

// RunBrowserInstall registers captain as an agent-browser launch.mutate plugin,
// so every browser agent-browser starts locally records which agent session
// opened it.
//
// The entry is merged into agent-browser's own config rather than written over
// it: a user's headed/proxy/extension settings and any other plugins survive.
func RunBrowserInstall(_ context.Context, opts BrowserInstallOptions) (BrowserInstallResult, error) {
	path, err := agentBrowserConfigPath(opts)
	if err != nil {
		return BrowserInstallResult{}, err
	}
	command, err := browserPluginCommand(opts.Command)
	if err != nil {
		return BrowserInstallResult{}, err
	}

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return BrowserInstallResult{}, err
	}
	merged, changed, err := mergeAgentBrowserPlugin(existing, agentBrowserPluginEntry(command))
	if err != nil {
		return BrowserInstallResult{}, err
	}

	stateDir, err := BrowserLinkStateDir()
	if err != nil {
		return BrowserInstallResult{}, err
	}
	result := BrowserInstallResult{Path: path, Command: command, StateDir: stateDir, Changed: changed}
	if opts.DryRun {
		result.Config = string(merged)
		result.Changed = false
		return result, nil
	}

	// The plugin runs inside whatever sandbox the agent's own tools run in, so
	// its state directory has to exist and be on the agents' writable
	// allowlists before the first launch — the plugin itself is already confined
	// and cannot grant either.
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return BrowserInstallResult{}, fmt.Errorf("create browser state directory %s: %w", stateDir, err)
	}
	if opts.Sandbox {
		sandboxChanges, err := agentsandbox.EnsureWritable(stateDir)
		if err != nil {
			return BrowserInstallResult{}, err
		}
		result.Sandbox = sandboxChanges
	}

	if changed {
		if err := writeAgentBrowserConfig(path, merged); err != nil {
			return BrowserInstallResult{}, err
		}
	}
	return result, nil
}

func agentBrowserConfigPath(opts BrowserInstallOptions) (string, error) {
	if opts.Config != "" {
		return filepath.Abs(opts.Config)
	}
	if !opts.Global {
		return filepath.Abs(projectAgentBrowserConfig)
	}
	socketDir, err := agentBrowserSocketDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(socketDir, "config.json"), nil
}

// browserPluginCommand resolves the command agent-browser will spawn. The
// running binary's own path is used so the plugin keeps working when captain is
// not on agent-browser's PATH.
func browserPluginCommand(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve the captain binary to register as a plugin: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return executable, nil
	}
	return resolved, nil
}

func agentBrowserPluginEntry(command string) map[string]any {
	return map[string]any{
		"name":         agentBrowserPluginName,
		"command":      command,
		"args":         []any{"browser", "plugin"},
		"capabilities": []any{agentBrowserLaunchMutate},
	}
}

// mergeAgentBrowserPlugin adds or updates captain's entry in an agent-browser
// configuration document, reporting whether anything changed. A document that
// cannot be parsed is an error: silently replacing a user's configuration would
// lose settings captain has no business discarding.
func mergeAgentBrowserPlugin(document []byte, entry map[string]any) ([]byte, bool, error) {
	config := map[string]any{}
	if len(bytes.TrimSpace(document)) > 0 {
		if err := json.Unmarshal(document, &config); err != nil {
			return nil, false, fmt.Errorf("parse agent-browser configuration: %w", err)
		}
	}
	plugins, err := configuredPlugins(config)
	if err != nil {
		return nil, false, err
	}

	changed := true
	replaced := false
	for i, plugin := range plugins {
		existing, ok := plugin.(map[string]any)
		if !ok || existing["name"] != entry["name"] {
			continue
		}
		changed = !reflect.DeepEqual(existing, entry)
		plugins[i] = entry
		replaced = true
		break
	}
	if !replaced {
		plugins = append(plugins, entry)
	}
	config["plugins"] = plugins

	merged, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(merged, '\n'), changed, nil
}

func configuredPlugins(config map[string]any) ([]any, error) {
	value, ok := config["plugins"]
	if !ok || value == nil {
		return nil, nil
	}
	plugins, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("agent-browser configuration has a %q key that is not a list", "plugins")
	}
	return plugins, nil
}

// writeAgentBrowserConfig replaces the file atomically so a concurrent
// agent-browser read never sees a partial configuration.
func writeAgentBrowserConfig(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".agent-browser-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
