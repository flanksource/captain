package promptrun

import (
	"context"
	"path/filepath"

	"github.com/flanksource/commons-db/shell"
	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credproxy"
)

var _ = g.Describe("Inline tool binding", func() {
	spec := func(tools api.InlineTools) Input {
		return Input{Resolved: api.ResolvedSpec{Spec: api.Spec{Tools: tools}}}
	}

	g.It("adds an executable caller tool for each declared tool, after the host's own", func() {
		host := api.ToolDefinition{Name: "host_tool", Handler: func(context.Context, map[string]any) (any, error) { return nil, nil }}
		in := spec(api.InlineTools{"echo_input": {Exec: "cat"}})
		in.Config.Tools = []api.ToolDefinition{host}

		Expect(bindInlineTools(&in)).To(Succeed())

		Expect(in.Config.Tools).To(HaveLen(2))
		Expect(in.Config.Tools[0].Name).To(Equal("host_tool"))
		Expect(in.Config.Tools[1].Name).To(Equal("echo_input"))
		Expect(in.inlineTools.PreRun(&agent.HookContext{Context: context.Background(), Request: &ai.Request{}, Response: &ai.Response{}})).To(Succeed())
		g.DeferCleanup(in.inlineTools.Close)
		out, err := in.Config.Tools[1].Handler(context.Background(), map[string]any{"q": "hi"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal(`{"q":"hi"}`))
	})

	g.It("refuses an inline tool that collides with a host tool", func() {
		in := spec(api.InlineTools{"shared": {Exec: "true"}})
		in.Config.Tools = []api.ToolDefinition{{Name: "shared"}}

		Expect(bindInlineTools(&in)).To(MatchError(ContainSubstring(`inline tool "shared" collides`)))
	})

	g.It("refuses inline tools when a supplied provider would never expose them", func() {
		in := spec(api.InlineTools{"probe": {Exec: "true"}})
		in.Provider = &bufferedOnlyProvider{}

		Expect(bindInlineTools(&in)).To(MatchError(ContainSubstring("supplied Provider")))
	})

	g.It("records how each sidecar tool resolved, for the relocating executor to ship as is", func() {
		grant := credproxy.Grant{Name: "gh", URL: "https://api.example.com", Methods: []string{"GET"}, Paths: []string{"/"}}
		in := spec(api.InlineTools{
			"granted": {Exec: "true", ReadOnly: true, Placement: api.ToolPlacementSidecar, Grants: []credproxy.Grant{grant}},
			"plain":   {Exec: "true", ReadOnly: true, Placement: api.ToolPlacementSidecar},
			"local":   {Exec: "true", ReadOnly: true},
		})
		declared := in.Resolved.Spec.Tools
		in.Resolved.Spec.Model = api.Model{Name: "sonnet", Mode: api.ModeCLI}
		in.Config.SandboxSelection = &api.SandboxConfig{Kind: api.SandboxGitAgent}

		Expect(bindInlineTools(&in)).To(Succeed())
		g.DeferCleanup(in.inlineTools.Close)

		resolved := in.Resolved.Spec.Tools
		Expect(resolved["granted"].Credentials).To(Equal(api.ToolCredentialsProxied), "grants make an unset mode proxied")
		Expect(resolved["plain"].Credentials).To(Equal(api.ToolCredentialsNone))
		Expect(resolved["local"].Credentials).To(BeEmpty(), "a supervisor tool is resolved where it runs")
		Expect(declared["granted"].Credentials).To(BeEmpty(), "the declared tools are not mutated")
	})

	g.Context("bound for a chat, which drives its provider itself", func() {
		chatSpec := func(tool api.InlineTool) api.Spec {
			return api.Spec{Model: api.Model{Name: "sonnet", Mode: api.ModeAgent}, Tools: api.InlineTools{"where": tool}}
		}

		g.It("serves every call from one environment, prepared where setup left the chat, until the chat closes it", func() {
			dir, err := filepath.EvalSymlinks(g.GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			spec := chatSpec(api.InlineTool{Exec: "pwd", ReadOnly: true})
			var cfg ai.Config

			workspace, err := BindInlineTools(&spec, &cfg)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Tools).To(HaveLen(1))
			call := func() (any, error) { return cfg.Tools[0].Handler(context.Background(), map[string]any{}) }
			_, err = call()
			Expect(err).To(MatchError(ContainSubstring("not prepared")))

			workspace.SetRun(dir, nil)
			Expect(workspace.Prepare(context.Background())).To(Succeed())
			first, err := call()
			Expect(err).NotTo(HaveOccurred())
			second, err := call()
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{first, second}).To(Equal([]any{dir, dir}))

			Expect(workspace.Close()).To(Succeed())
			_, err = call()
			Expect(err).To(MatchError(ContainSubstring("not prepared")))
		})

		g.It("refuses a tool that would ask when the chat attaches no broker", func() {
			spec := chatSpec(api.InlineTool{Exec: "true", Destructive: true})
			var cfg ai.Config

			_, err := BindInlineTools(&spec, &cfg)

			Expect(err).To(MatchError(ContainSubstring(`caller tools where resolve to ask on "anthropic agent" but no approval broker is attached`)))
		})

		g.It("binds nothing for a spec without tools", func() {
			spec := api.Spec{Model: api.Model{Name: "sonnet", Mode: api.ModeAgent}}
			var cfg ai.Config

			workspace, err := BindInlineTools(&spec, &cfg)

			Expect(err).NotTo(HaveOccurred())
			Expect(workspace).To(BeNil())
			Expect(cfg.Tools).To(BeEmpty())
		})
	})

	g.It("refreshes the tools' workspace after setup has run", func() {
		in := spec(api.InlineTools{"probe": {Exec: "true"}})
		in.Resolved.Spec.Setup = &shell.Setup{Cwd: "."}
		Expect(bindInlineTools(&in)).To(Succeed())

		hooks, err := Hooks(context.Background(), in, nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(hooks).To(HaveLen(2))
		Expect(hooks[0]).To(BeAssignableToTypeOf(&setup.Plugin{}))
		Expect(hooks[1]).To(BeIdenticalTo(in.inlineTools))
	})
})
