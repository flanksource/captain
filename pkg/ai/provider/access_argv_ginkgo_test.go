package provider

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
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

	DescribeTable("skips execpolicy rules on codex exec only under read-only",
		func(req ai.Request, want bool) {
			args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5"}, req)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(cleanup)
			Expect(slices.Contains(args, "--ignore-rules")).To(Equal(want))
		},
		Entry("read-only", compiledReadOnly(api.RuntimeOf(api.OpenAI, api.ModeCLI)), true),
		Entry("no access", ai.Request{Prompt: api.Prompt{User: "review"}}, false),
		Entry("full", ai.Request{Prompt: api.Prompt{User: "review"}, Permissions: api.Permissions{Access: api.AccessFull}}, false),
	)

	Describe("codex app-server CODEX_HOME", func() {
		var user, config string
		BeforeEach(func() {
			root := GinkgoT().TempDir()
			user, config = filepath.Join(root, "user-codex"), filepath.Join(root, "config")
			GinkgoT().Setenv("CODEX_HOME", user)
			GinkgoT().Setenv("XDG_CONFIG_HOME", config)
			Expect(os.MkdirAll(filepath.Join(user, "rules"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(user, "auth.json"), []byte("{}"), 0o600)).To(Succeed())
		})

		It("starts a read-only run in the isolated home and any other run in the user's", func() {
			readOnly := compiledReadOnly(api.RuntimeOf(api.OpenAI, api.ModeAgent))
			home, err := codexRunHome(readOnly)
			Expect(err).NotTo(HaveOccurred())
			Expect(home).To(Equal(filepath.Join(config, "captain", "codex-read-only")))
			Expect(newCodexAppServerProcess(ai.Config{}, home).Env).To(HaveKeyWithValue("CODEX_HOME", home))
			Expect(filepath.Join(home, "rules")).NotTo(BeAnExistingFile())

			home, err = codexRunHome(ai.Request{Prompt: api.Prompt{User: "review"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(home).To(BeEmpty())
			Expect(newCodexAppServerProcess(ai.Config{}, home).Env).NotTo(HaveKey("CODEX_HOME"))
		})

		It("refuses a read-only run in a tree whose project rules allow a command", func() {
			repo := GinkgoT().TempDir()
			rules := filepath.Join(repo, ".codex", "rules", "team.rules")
			Expect(os.MkdirAll(filepath.Join(repo, ".git"), 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Dir(rules), 0o755)).To(Succeed())
			Expect(os.WriteFile(rules, []byte(`prefix_rule(pattern=["git","add"], decision="allow")`), 0o600)).To(Succeed())
			req := compiledReadOnly(api.RuntimeOf(api.OpenAI, api.ModeAgent))
			req.SetCwd(repo)

			_, err := codexRunHome(req)
			Expect(err).To(MatchError(ContainSubstring(rules)))
		})

		It("refuses a turn whose access differs from the running app-server's home", func() {
			server := &CodexAppServer{codexHome: "/isolated", rpc: &jsonrpc.Client{}}
			Expect(server.ensureStarted(context.Background(), "")).To(MatchError(ContainSubstring("CODEX_HOME")))
		})
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
