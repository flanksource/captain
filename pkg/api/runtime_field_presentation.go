package api

import "strings"

func runtimeFieldArrayDisplay(path string) string {
	switch path {
	case "prompt.attachments", "cliArgs.addDir", "cliArgs.image", "sandbox.dispatch.paths",
		"sandbox.policy.filesystem.writableRoots", "sandbox.policy.filesystem.readableRoots",
		"sandbox.policy.filesystem.deniedReadRoots", "sandbox.policy.filesystem.deniedWriteRoots",
		"sandbox.policy.network.allowedDomains", "sandbox.policy.network.deniedDomains",
		"sandbox.policy.network.allowedUnixSockets", "sandbox.policy.credentials.deniedFiles":
		return "list"
	default:
		return ""
	}
}

func runtimeFieldTitle(path string) string {
	switch path {
	case "budget.cost":
		return "Max cost (USD)"
	case "budget.maxTokens":
		return "Max tokens"
	case "budget.maxTurns":
		return "Max turns"
	case "budget.timeout":
		return "Timeout"
	case "permissions.mode":
		return "Permission posture"
	case "sandbox.mode":
		return "Sandbox mode"
	case "sandbox.policy":
		return "Native policy"
	case "sandbox.policy.filesystem":
		return "Filesystem"
	case "sandbox.policy.network":
		return "Network"
	case "sandbox.policy.commands":
		return "Commands"
	case "sandbox.policy.credentials":
		return "Credentials"
	case "sandbox.policy.platform":
		return "Platform"
	case "sandbox.dispatch":
		return "Git Agent dispatch"
	case "sandbox.policy.required":
		return "Require sandbox"
	case "sandbox.policy.filesystem.access":
		return "Filesystem access"
	case "sandbox.policy.filesystem.writableRoots":
		return "Writable roots"
	case "sandbox.policy.filesystem.readableRoots":
		return "Readable roots"
	case "sandbox.policy.filesystem.deniedReadRoots":
		return "Denied read roots"
	case "sandbox.policy.filesystem.deniedWriteRoots":
		return "Denied write roots"
	case "sandbox.policy.filesystem.includeSystemTemp":
		return "Include system temp"
	case "sandbox.policy.network.access":
		return "Network access"
	case "sandbox.policy.network.allowedDomains":
		return "Allowed domains"
	case "sandbox.policy.network.deniedDomains":
		return "Denied domains"
	case "sandbox.policy.network.allowAllUnixSockets":
		return "Allow all Unix sockets"
	case "sandbox.policy.network.allowedUnixSockets":
		return "Allowed Unix sockets"
	case "sandbox.policy.network.allowLocalBinding":
		return "Allow local binding"
	case "sandbox.policy.network.allowedMachServices":
		return "Allowed Mach services"
	case "sandbox.policy.network.httpProxyPort":
		return "HTTP proxy port"
	case "sandbox.policy.network.socksProxyPort":
		return "SOCKS proxy port"
	case "sandbox.policy.commands.excludedFromSandbox":
		return "Commands excluded from sandbox"
	case "sandbox.policy.commands.allowUnsandboxed":
		return "Allow unsandboxed commands"
	case "sandbox.policy.credentials.deniedFiles":
		return "Denied credential files"
	case "sandbox.policy.platform.allowAppleEvents":
		return "Allow Apple events"
	case "sandbox.policy.platform.weakerNestedIsolation":
		return "Weaker nested isolation"
	case "sandbox.policy.platform.weakerNetworkIsolation":
		return "Weaker network isolation"
	case "sandbox.backend":
		return "Sandbox backend"
	case "sandbox.agent":
		return "Pinned agent"
	case "sandbox.dispatch.paths":
		return "Included paths"
	case "sandbox.dispatch.maxAttempts":
		return "Max attempts"
	case "setup.cwd":
		return "Directory"
	case "sessionId":
		return "Session ID"
	}
	parts := strings.Split(path, ".")
	name := parts[len(parts)-1]
	return strings.ToUpper(name[:1]) + name[1:]
}

func runtimeFieldIcon(path string) string {
	switch path {
	case "model":
		return "sparkles"
	case "effort":
		return "gauge"
	case "budget.cost":
		return "currency-dollar"
	case "budget.maxTokens":
		return "coins"
	case "budget.maxTurns":
		return "repeat"
	case "budget.timeout":
		return "timer"
	case "sandbox.mode", "permissions.tools", "toolPreferences", "toolPolicy":
		return "shield"
	case "permissions.mode":
		return "hand"
	case "setup.cwd":
		return "folder"
	case "sessionId":
		return "fingerprint"
	default:
		return ""
	}
}
