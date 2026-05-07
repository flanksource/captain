package provider

// DemoteOnAllowlist returns the effective permission mode for a request that
// carries an --allowedTools whitelist. Claude CLI's --allowedTools is an
// auto-approve list, not a restriction, so under bypassPermissions the model
// can still reach for any tool. Demoting to default turns --allowedTools into
// an actual allowlist in non-interactive -p mode (anything else gets denied).
func DemoteOnAllowlist(mode string, hasAllowlist bool) string {
	if hasAllowlist && (mode == "" || mode == "bypassPermissions") {
		return "default"
	}
	return mode
}

// SafeEditAllowlist is the curated allowlist applied by --edit when the caller
// did not provide their own AllowedTools override. It limits the model to
// pure code-editing tools; no Bash, no WebFetch, no MCP.
var SafeEditAllowlist = []string{"Read", "Edit", "Write", "Glob", "Grep"}
