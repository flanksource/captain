package adapter

import "path/filepath"

// cliCredentialEnv is the credential environment variables each supported
// agent CLI needs passed through into its confinement. One list, shared by
// every adapter, so a variable added for a new CLI cannot reach one sandbox
// kind and silently miss another.
func cliCredentialEnv(command string) []string {
	switch filepath.Base(command) {
	case "claude":
		return []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN"}
	case "codex":
		return []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"}
	case "gemini":
		return []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}
	}
	return nil
}
