package api

import "strings"

func runtimeSandboxArgumentBindings(p *ModelProvider, mode RuntimeMode) []runtimeArgumentBinding {
	bindings := []runtimeArgumentBinding{mappedArgument("Captain sandbox adapter", "sandbox.mode")}
	switch RuntimeOf(p, mode) {
	case RuntimeOf(Anthropic, ModeCLI):
		bindings = append(bindings, mappedArgument("--settings sandbox.enabled", "sandbox.mode"))
		for _, path := range claudeNativeSandboxFields {
			bindings = append(bindings, mappedArgument("--settings "+claudeSandboxSettingPath(path), path))
		}
	case RuntimeOf(Anthropic, ModeAgent), RuntimeOf(Anthropic, ModeCmux):
		bindings = append(bindings, mappedArgument("sandbox.enabled", "sandbox.mode"))
		for _, path := range claudeNativeSandboxFields {
			bindings = append(bindings, mappedArgument(claudeSandboxSettingPath(path), path))
		}
	case RuntimeOf(OpenAI, ModeCLI):
		bindings = append(bindings,
			mappedArgument("--sandbox", "sandbox.policy.filesystem.access"),
			mappedArgument("sandbox_workspace_write.writable_roots", "sandbox.policy.filesystem.writableRoots"),
			mappedArgument("sandbox_workspace_write.exclude_*_tmp", "sandbox.policy.filesystem.includeSystemTemp"),
			mappedArgument("sandbox_workspace_write.network_access", "sandbox.policy.network.access"),
		)
	case RuntimeOf(OpenAI, ModeAgent), RuntimeOf(OpenAI, ModeCmux):
		bindings = append(bindings,
			mappedArgument("thread/start.sandbox", "sandbox.policy.filesystem.access"),
			mappedArgument("config.sandbox_workspace_write.writable_roots", "sandbox.policy.filesystem.writableRoots"),
			mappedArgument("config.sandbox_workspace_write.exclude_*_tmp", "sandbox.policy.filesystem.includeSystemTemp"),
			mappedArgument("config.sandbox_workspace_write.network_access", "sandbox.policy.network.access"),
		)
	}
	return bindings
}

func claudeSandboxSettingPath(path string) string {
	return "sandbox." + strings.TrimPrefix(path, "sandbox.policy.")
}

func runtimeSandboxPaths(p *ModelProvider, mode RuntimeMode) []string {
	capabilities := RuntimeSandboxCapabilitiesFor(p, mode)
	// The posture is not listed here: it is permissions.mode, contributed by
	// runtimeArgumentPaths, because it applies with or without isolation.
	paths := []string{"sandbox.mode"}
	paths = append(paths, capabilities.NativeFields...)
	if containsSandboxMode(capabilities.Modes, SandboxDocker) || containsSandboxMode(capabilities.Modes, SandboxGitAgent) {
		paths = append(paths, "sandbox.backend")
	}
	if containsSandboxMode(capabilities.Modes, SandboxGitAgent) {
		paths = append(paths, "sandbox.agent", "sandbox.dispatch.paths", "sandbox.dispatch.maxAttempts")
	}
	return paths
}

func applyRuntimeSandboxPresentation(field map[string]any, path string, p *ModelProvider, mode RuntimeMode) {
	capabilities := RuntimeSandboxCapabilitiesFor(p, mode)
	field["x-clicky-section"] = "sandbox"
	field["x-icon"] = sandboxFieldIcon(path)
	switch path {
	case "sandbox.mode":
		field["enum"] = enumValues(capabilities.Modes)
		field["x-enum-display"] = "segmented"
	case "sandbox.policy.filesystem.access":
		field["enum"] = enumValues([]SandboxFilesystemAccess{
			SandboxFilesystemReadOnly, SandboxFilesystemWorkspaceWrite,
		})
		field["x-enum-display"] = "segmented"
	case "sandbox.policy.network.access":
		values := []SandboxNetworkAccess{SandboxNetworkDisabled, SandboxNetworkUnrestricted}
		// Only the local Claude transports expose a restricted network tier; the
		// Anthropic API mode does not. This was a "claude-" string prefix on the
		// composite id, the one test here that would have failed silently rather
		// than at compile time under a rename.
		if p == Anthropic && mode != ModeAPI {
			values = []SandboxNetworkAccess{
				SandboxNetworkDisabled, SandboxNetworkRestricted, SandboxNetworkUnrestricted,
			}
		}
		field["enum"] = enumValues(values)
		field["x-enum-display"] = "segmented"
	}
	if strings.HasPrefix(path, "sandbox.policy.") {
		field["x-clicky-visible-when"] = map[string]any{"path": "sandbox.mode", "equals": "native"}
	}
}

func sandboxFieldIcon(path string) string {
	switch {
	case path == "sandbox.mode":
		return "shield"
	case strings.Contains(path, ".filesystem."):
		return "folder-lock"
	case strings.Contains(path, ".network."):
		return "globe-lock"
	case strings.Contains(path, ".commands."):
		return "terminal"
	case strings.Contains(path, ".credentials."):
		return "key"
	case strings.Contains(path, ".platform."):
		return "desktop"
	case strings.Contains(path, ".dispatch."):
		return "git-branch"
	default:
		return "shield"
	}
}
