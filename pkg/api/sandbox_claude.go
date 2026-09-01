package api

import (
	"fmt"
	"slices"
)

// TranslateClaudeSandbox maps the unified policy to Claude's SDK/CLI sandbox
// settings object. Both transports consume the same result.
func TranslateClaudeSandbox(runtime Runtime, ref SandboxRef) (map[string]any, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%s sandbox: %w", runtime, err)
	}
	if ref.Mode != SandboxNative {
		return map[string]any{"enabled": false}, nil
	}
	settings := map[string]any{"enabled": true}
	policy := ref.Policy
	if policy == nil {
		return settings, nil
	}
	if policy.Required != nil {
		settings["failIfUnavailable"] = *policy.Required
	}
	filesystem, err := translateClaudeFilesystem(runtime, policy)
	if err != nil {
		return nil, err
	}
	if len(filesystem) > 0 {
		settings["filesystem"] = filesystem
	}
	if network := translateClaudeNetwork(policy.Network); len(network) > 0 {
		settings["network"] = network
	}
	if policy.Commands != nil {
		if len(policy.Commands.ExcludedFromSandbox) > 0 {
			settings["excludedCommands"] = policy.Commands.ExcludedFromSandbox
		}
		if policy.Commands.AllowUnsandboxed != nil {
			settings["allowUnsandboxedCommands"] = *policy.Commands.AllowUnsandboxed
		}
	}
	if policy.Platform != nil {
		if policy.Platform.AllowAppleEvents != nil {
			settings["allowAppleEvents"] = *policy.Platform.AllowAppleEvents
		}
		if policy.Platform.WeakerNestedIsolation != nil {
			settings["enableWeakerNestedSandbox"] = *policy.Platform.WeakerNestedIsolation
		}
		if policy.Platform.WeakerNetworkIsolation != nil {
			settings["enableWeakerNetworkIsolation"] = *policy.Platform.WeakerNetworkIsolation
		}
	}
	return settings, nil
}

func translateClaudeFilesystem(runtime Runtime, policy *NativeSandboxPolicy) (map[string]any, error) {
	settings := map[string]any{}
	if policy.Filesystem != nil {
		filesystem := policy.Filesystem
		if filesystem.IncludeSystemTemp != nil {
			return nil, UnsupportedSandboxField(runtime, "policy.filesystem.includeSystemTemp")
		}
		if len(filesystem.WritableRoots) > 0 {
			settings["allowWrite"] = filesystem.WritableRoots
		}
		if len(filesystem.ReadableRoots) > 0 {
			settings["allowRead"] = filesystem.ReadableRoots
		}
		denyRead := slices.Clone(filesystem.DeniedReadRoots)
		denyWrite := slices.Clone(filesystem.DeniedWriteRoots)
		if filesystem.Access == SandboxFilesystemReadOnly {
			denyWrite = append([]string{"**"}, denyWrite...)
		}
		if len(denyRead) > 0 {
			settings["denyRead"] = denyRead
		}
		if len(denyWrite) > 0 {
			settings["denyWrite"] = denyWrite
		}
	}
	if policy.Credentials != nil {
		if len(policy.Credentials.DeniedEnv) > 0 {
			return nil, UnsupportedSandboxField(runtime, "policy.credentials.deniedEnv")
		}
		if len(policy.Credentials.MaskedEnv) > 0 {
			return nil, UnsupportedSandboxField(runtime, "policy.credentials.maskedEnv")
		}
		if len(policy.Credentials.DeniedFiles) > 0 {
			denied, _ := settings["denyRead"].([]string)
			settings["denyRead"] = append(denied, policy.Credentials.DeniedFiles...)
		}
	}
	return settings, nil
}

func translateClaudeNetwork(policy *SandboxNetworkPolicy) map[string]any {
	if policy == nil {
		return nil
	}
	settings := map[string]any{}
	allowed := slices.Clone(policy.AllowedDomains)
	denied := slices.Clone(policy.DeniedDomains)
	switch policy.Access {
	case SandboxNetworkDisabled:
		denied = append([]string{"*"}, denied...)
	case SandboxNetworkUnrestricted:
		allowed = append([]string{"*"}, allowed...)
	}
	setStringSlice(settings, "allowedDomains", allowed)
	setStringSlice(settings, "deniedDomains", denied)
	setStringSlice(settings, "allowUnixSockets", policy.AllowedUnixSockets)
	setOptional(settings, "allowAllUnixSockets", policy.AllowAllUnixSockets)
	setOptional(settings, "allowLocalBinding", policy.AllowLocalBinding)
	setStringSlice(settings, "allowMachLookup", policy.AllowedMachServices)
	setOptional(settings, "httpProxyPort", policy.HTTPProxyPort)
	setOptional(settings, "socksProxyPort", policy.SOCKSProxyPort)
	return settings
}

func setStringSlice(settings map[string]any, key string, value []string) {
	if len(value) > 0 {
		settings[key] = value
	}
}

func setOptional[T any](settings map[string]any, key string, value *T) {
	if value != nil {
		settings[key] = *value
	}
}

// UnsupportedSandboxField is the canonical failure for a configured neutral
// field the active backend cannot faithfully express.
func UnsupportedSandboxField(runtime Runtime, path string) error {
	return fmt.Errorf("sandbox.%s is not supported by %s", path, runtime)
}
