package process

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	vendoredClaudeCommand = "/Users/acme/Library/Caches/captain/claude-agent/node_modules/sdk/claude --model opus"
	captainServeCommand   = "/private/var/folders/build/captain serve --dev"
	claudeInCaptain       = "/opt/homebrew/bin/claude --cwd /Users/acme/src/captain"
)

var _ = Describe("agent process semantics", func() {
	DescribeTable("classifies the executable without treating arguments as ownership",
		func(command, expected string) {
			Expect(Source(command)).To(Equal(expected))
		},
		Entry("vendored Claude", vendoredClaudeCommand, "claude"),
		Entry("Claude working in Captain", claudeInCaptain, "claude"),
		Entry("Captain itself", captainServeCommand, ""),
		Entry("Claude desktop", "/Applications/Claude.app/Contents/MacOS/Claude", ""),
		Entry("Codex desktop helper", "/Applications/Codex.app/Contents/Frameworks/Codex Framework.framework/Helpers/browser_crashpad_handler", ""),
		Entry("Codex", "/Users/acme/.local/bin/codex-darwin-arm64 exec", "codex"),
		Entry("Codex MCP", "/Users/acme/.local/bin/codex mcp-server", ""),
		Entry("unrelated", "/usr/bin/vim notes.md", ""),
	)

	DescribeTable("extracts an explicit Claude session identity",
		func(command, expected string) {
			Expect(SessionIDFromCommand(command)).To(Equal(expected))
		},
		Entry("session id", "claude --session-id session-a", "session-a"),
		Entry("resume", "claude --resume=session-b", "session-b"),
		Entry("none", "codex app-server", ""),
	)

	It("reads agent identity and CMUX surface from the captured environment", func() {
		environment := map[string]string{
			"CLAUDE_CODE_SESSION_ID": "session-a",
			"CMUX_SURFACE_ID":        "surface-a",
			"CMUX_WORKSPACE_ID":      "workspace-a",
			"CMUX_PORT":              "9150",
			"CMUX_CLAUDE_PID":        "33088",
		}

		source, sessionID := IdentityFromEnvironment(environment)
		Expect(source).To(Equal("claude"))
		Expect(sessionID).To(Equal("session-a"))
		Expect(SurfaceFromEnvironment(environment)).To(Equal(&Surface{
			SurfaceID: "surface-a", WorkspaceID: "workspace-a", Port: 9150, ClaudePID: 33088,
		}))
	})

	It("returns no surface when CMUX markers are absent", func() {
		Expect(SurfaceFromEnvironment(map[string]string{"PATH": "/usr/bin"})).To(BeNil())
	})
})
