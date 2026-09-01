package cli

import (
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The fixture is one machine with all three agents configured, which is the
// only shape that can catch the bug these specs exist for: the catalog used to
// merge every source on the machine and serve the union to whichever agent
// asked, so a codex run was offered claude's tools, claude's MCP servers and
// every skill directory in $HOME — and never codex's own [mcp_servers] block.
func writeCatalogFixture(workspace, home string) {
	write := func(path, data string) {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(data), 0o644)).To(Succeed())
	}
	mkdir := func(path string) { Expect(os.MkdirAll(path, 0o755)).To(Succeed()) }

	// claude
	write(filepath.Join(workspace, ".mcp.json"), `{"mcpServers":{"filesystem":{},"gavel":{}}}`)
	write(filepath.Join(home, ".claude.json"), `{"mcpServers":{"ado":{},"gavel":{}}}`)
	mkdir(filepath.Join(workspace, ".skills"))
	mkdir(filepath.Join(home, ".claude", "skills", "review"))
	mkdir(filepath.Join(home, ".agents", "skills", "iconography"))
	write(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), `{"plugins":{"gavel-pack":{}}}`)

	// codex
	write(filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-5\"\n\n[mcp_servers.openaiDeveloperDocs]\nurl = \"https://developers.openai.com/mcp\"\n")
	mkdir(filepath.Join(home, ".codex", "skills", "wrangler"))
	mkdir(filepath.Join(home, ".codex", "plugins", "captain"))

	// gemini
	write(filepath.Join(home, ".gemini", "settings.json"), `{"mcpServers":{"vertex":{}}}`)
	mkdir(filepath.Join(home, ".gemini", "skills", "cloudflare"))
	mkdir(filepath.Join(home, ".gemini", "extensions", "code-review"))
}

func catalogIDs(items []api.PermissionCatalogItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

var _ = Describe("permission catalog", func() {
	var workspace, home string

	BeforeEach(func() {
		workspace = GinkgoT().TempDir()
		home = GinkgoT().TempDir()
		GinkgoT().Setenv("HOME", home)
		writeCatalogFixture(workspace, home)
	})

	Describe("tools", func() {
		It("serves each agent its own built-in vocabulary", func() {
			Expect(catalogIDs(buildPermissionCatalog(workspace, api.Anthropic, api.ModeCLI).Tools)).
				To(ContainElements("Read", "Bash", "WebFetch"))
			Expect(catalogIDs(buildPermissionCatalog(workspace, api.OpenAI, api.ModeCLI).Tools)).
				To(ContainElements("shell", "apply_patch", "update_plan"))
			Expect(catalogIDs(buildPermissionCatalog(workspace, api.Google, api.ModeCLI).Tools)).
				To(ContainElements("run_shell_command", "read_file", "write_file"))
		})

		It("never offers one agent another's tool names", func() {
			codex := catalogIDs(buildPermissionCatalog(workspace, api.OpenAI, api.ModeCLI).Tools)
			Expect(codex).ToNot(ContainElement("Read"))
			Expect(codex).ToNot(ContainElement("Bash"))

			gemini := catalogIDs(buildPermissionCatalog(workspace, api.Google, api.ModeCLI).Tools)
			Expect(gemini).ToNot(ContainElement("Read"))
			Expect(gemini).ToNot(ContainElement("shell"))
		})

		It("keeps group and default policy so a picker can render them", func() {
			tools := buildPermissionCatalog(workspace, api.OpenAI, api.ModeCLI).Tools
			var shell api.PermissionCatalogItem
			for _, tool := range tools {
				if tool.ID == "shell" {
					shell = tool
				}
			}
			Expect(shell.Group).To(Equal("Shell"))
			Expect(shell.DefaultMode).To(Equal(string(api.ToolPolicyAsk)))
			Expect(shell.Description).ToNot(BeEmpty())
		})
	})

	Describe("MCP servers", func() {
		It("reads claude's .mcp.json and ~/.claude.json", func() {
			Expect(catalogIDs(buildPermissionCatalog(workspace, api.Anthropic, api.ModeCLI).MCP)).
				To(ConsistOf("ado", "filesystem", "gavel"))
		})

		It("reads codex's [mcp_servers] tables, which were never read before", func() {
			Expect(catalogIDs(buildPermissionCatalog(workspace, api.OpenAI, api.ModeCLI).MCP)).
				To(ConsistOf("openaiDeveloperDocs"))
		})

		It("reads gemini's settings.json", func() {
			Expect(catalogIDs(buildPermissionCatalog(workspace, api.Google, api.ModeCLI).MCP)).
				To(ConsistOf("vertex"))
		})
	})

	Describe("skills and plugins", func() {
		It("scopes skills to the selected agent's own directories", func() {
			Expect(catalogIDs(buildPermissionCatalog(workspace, api.Anthropic, api.ModeCLI).Skills)).To(ConsistOf(
				"$CWD/.skills",
				filepath.Join(home, ".claude", "skills", "review"),
				filepath.Join(home, ".agents", "skills", "iconography"),
			))
			Expect(catalogIDs(buildPermissionCatalog(workspace, api.OpenAI, api.ModeCLI).Skills)).
				To(ConsistOf(filepath.Join(home, ".codex", "skills", "wrangler")))
			Expect(catalogIDs(buildPermissionCatalog(workspace, api.Google, api.ModeCLI).Skills)).
				To(ConsistOf(filepath.Join(home, ".gemini", "skills", "cloudflare")))
		})

		It("scopes plugins to the selected agent, calling gemini's extensions what gemini calls them", func() {
			Expect(catalogIDs(buildPermissionCatalog(workspace, api.Anthropic, api.ModeCLI).Plugins)).
				To(ConsistOf("gavel-pack"))
			Expect(catalogIDs(buildPermissionCatalog(workspace, api.OpenAI, api.ModeCLI).Plugins)).
				To(ConsistOf(filepath.Join(home, ".codex", "plugins", "captain")))

			gemini := buildPermissionCatalog(workspace, api.Google, api.ModeCLI).Plugins
			Expect(catalogIDs(gemini)).To(ConsistOf(filepath.Join(home, ".gemini", "extensions", "code-review")))
			Expect(gemini[0].Group).To(Equal("Extensions"))
		})
	})

	Describe("API backends", func() {
		It("serves an empty catalog, because no agent CLI runs", func() {
			catalog := buildPermissionCatalog(workspace, api.Anthropic, api.ModeAPI)
			Expect(catalog.Tools).To(BeEmpty())
			Expect(catalog.MCP).To(BeEmpty())
			Expect(catalog.Skills).To(BeEmpty())
			Expect(catalog.Plugins).To(BeEmpty())
		})
	})

	Describe("resolveCatalogDir", func() {
		var base, nested string

		BeforeEach(func() {
			base = GinkgoT().TempDir()
			nested = filepath.Join(base, "sub", "child")
			Expect(os.MkdirAll(nested, 0o755)).To(Succeed())
		})

		It("resolves an empty dir to the workspace root", func() {
			Expect(resolveCatalogDir(base, "")).To(Equal(filepath.Clean(base)))
		})

		It("allows relative paths that stay inside the workspace", func() {
			Expect(resolveCatalogDir(base, filepath.Join("sub", "child"))).To(Equal(nested))
		})

		DescribeTable("rejects traversal attempts",
			func(dir string) {
				_, err := resolveCatalogDir(base, dir)
				Expect(err).To(HaveOccurred())
			},
			Entry("parent escape", "../../etc"),
			Entry("interior parent segments", filepath.Join("sub", "..", "..", "etc")),
			// Rejected even though it resolves back inside the workspace: the
			// ".." check runs on the raw input, before any path is built.
			Entry("parent segment that resolves inside", "sub/../sub/child"),
			Entry("absolute path outside", "/etc"),
		)
	})
})
