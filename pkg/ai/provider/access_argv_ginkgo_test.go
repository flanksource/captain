package provider

import (
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// compiledReadOnly is the request a provider receives once the access
// decorator has compiled permissions.access for its runtime.
func compiledReadOnly(runtime api.Runtime) ai.Request {
	req := ai.Request{Prompt: api.Prompt{User: "review"}, Permissions: api.Permissions{Access: api.AccessReadOnly}}
	compiled, _, err := api.CompileAccess(runtime, req)
	Expect(err).NotTo(HaveOccurred())
	return compiled
}

var _ = Describe("permissions.access read-only on the wire", func() {
	It("runs codex-cli read-only with approval never and no MCP servers", func() {
		args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5"}, compiledReadOnly(api.RuntimeOf(api.OpenAI, api.ModeCLI)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(cleanup)

		Expect(sandboxFlagValue(args, "--sandbox")).To(Equal("read-only"))
		Expect(args).To(ContainElement(`approval_policy="never"`))
		Expect(strings.Join(args, " ")).To(ContainSubstring("mcp_servers"))
	})

	It("starts codex app-server threads and turns read-only with approval never", func() {
		req := compiledReadOnly(api.RuntimeOf(api.OpenAI, api.ModeAgent))
		thread, err := buildThreadStartParams("gpt-5.5", req, nil)
		Expect(err).NotTo(HaveOccurred())
		turn, err := buildTurnStartParams("gpt-5.5", req, "thread-1", nil)
		Expect(err).NotTo(HaveOccurred())

		Expect(thread).To(HaveKeyWithValue("approvalPolicy", "never"))
		Expect(thread).To(HaveKeyWithValue("sandbox", "read-only"))
		Expect(turn).To(HaveKeyWithValue("approvalPolicy", "never"))
		Expect(turn["sandboxPolicy"]).To(HaveKeyWithValue("type", "readOnly"))
	})

	It("denies claude's write, shell and sub-agent tools with strict MCP", func() {
		args, cleanup, err := buildClaudeCLIArgs("claude-sonnet-5", compiledReadOnly(api.RuntimeOf(api.Anthropic, api.ModeCLI)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(cleanup)

		denied := strings.Split(sandboxFlagValue(args, "--disallowedTools"), ",")
		Expect(denied).To(ContainElements("Agent", "Bash", "Edit", "MultiEdit", "NotebookEdit", "Task", "Workflow", "Write"))
		Expect(args).To(ContainElement("--strict-mcp-config"))
	})
})
