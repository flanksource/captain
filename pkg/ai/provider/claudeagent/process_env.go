package claudeagent

import "github.com/flanksource/captain/pkg/ai"

func agentProcessEnv(cfg ai.Config, environ []string) map[string]string {
	env := nestingEnvOverrides(environ)
	if cfg.APIURL != "" {
		env["ANTHROPIC_BASE_URL"] = cfg.APIURL
		env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
		env["DISABLE_NON_ESSENTIAL_MODEL_CALLS"] = "1"
		env["DISABLE_TELEMETRY"] = "1"
		env["DISABLE_ERROR_REPORTING"] = "1"
		env["DISABLE_AUTOUPDATER"] = "1"
		env["DISABLE_BUG_COMMAND"] = "1"
	}
	if cfg.APIKey != "" {
		env["ANTHROPIC_API_KEY"] = cfg.APIKey
		env["ANTHROPIC_AUTH_TOKEN"] = cfg.APIKey
	}
	return env
}
