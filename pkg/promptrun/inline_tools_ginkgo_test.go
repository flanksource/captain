package promptrun

import (
	"context"

	"github.com/flanksource/commons-db/shell"
	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/api"
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
