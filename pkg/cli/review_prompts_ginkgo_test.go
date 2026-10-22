package cli

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Built-in document review prompts", func() {
	var ctx context.Context
	allFamilies := []string{"req/", "doc/", "hl/", "ll/", "int/", "pub/", "prop/"}

	BeforeEach(func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), "captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		catalog, err := runtimeprofiles.NewCatalog(runtimeprofiles.NewBuiltinSource())
		Expect(err).NotTo(HaveOccurred())
		ctx = ContextWithRuntimeCatalog(context.Background(), catalog)
	})

	DescribeTable("render as read-only lint reviews scoped to their rule families",
		func(name string, vars []string, families []string) {
			// gavel requires the lint preset; `--preset lint` requests it and so replaces any pinned presets.
			for _, model := range []string{"agent:sonnet", "agent:gpt-6.1-sol"} {
				withModel := AIRuntimeOptions{AIProviderOptions: AIProviderOptions{ModelFlags: aiflags.ModelFlags{Model: model}}}
				for _, options := range []AIPromptOptions{
					{AIRuntimeOptions: withModel, RequiredPresets: []string{"lint"}, Var: vars},
					{AIRuntimeOptions: withModel, Presets: []string{"lint"}, Var: vars},
				} {
					rendered, err := RenderPrompt(ctx, PromptInvocation{Reference: name, Options: options})
					Expect(err).NotTo(HaveOccurred(), model)
					Expect(rendered.ValidationError).To(BeEmpty(), model)
					Expect(rendered.Input.Prompt.SchemaJSON).To(ContainSubstring(`"violations"`))
					trace := rendered.Resolution.Trace
					Expect(trace[len(trace)-1].Name).To(Equal("Lint"))
					Expect(rendered.Input.Permissions.Access).To(Equal(api.AccessReadOnly))
					Expect(rendered.Input.Permissions.Tools).To(BeEmpty())
					expectReadOnlyPosture(rendered)
				}
			}
			rendered, err := RenderPrompt(ctx, PromptInvocation{Reference: name, Options: AIPromptOptions{Var: vars}})
			Expect(err).NotTo(HaveOccurred())
			for _, v := range vars {
				_, value, _ := strings.Cut(v, "=")
				Expect(rendered.Input.Prompt.User).To(ContainSubstring(value))
			}
			for _, family := range allFamilies {
				if slices.Contains(families, family) {
					Expect(rendered.Input.Prompt.System).To(ContainSubstring("`"+family), family)
				} else {
					Expect(rendered.Input.Prompt.System).NotTo(ContainSubstring("`"+family), family)
				}
			}
		},
		Entry("internal high-level", "spec-internal-high", []string{"files=docs/prd.md"}, []string{"req/", "doc/", "hl/", "int/"}),
		Entry("internal low-level", "spec-internal-low", []string{"files=docs/design.md", "parent=docs/prd.md"}, []string{"req/", "doc/", "ll/", "int/"}),
		Entry("public high-level", "spec-public-high", []string{"files=docs/overview.md"}, []string{"req/", "doc/", "hl/", "pub/"}),
		Entry("public low-level", "spec-public-low", []string{"files=docs/api.md", "parent=docs/overview.md"}, []string{"req/", "doc/", "ll/", "pub/"}),
		Entry("proposal", "proposal-review", []string{"files=bid/response.md", "rfp=bid/rfp.md"}, []string{"req/", "doc/", "prop/"}),
	)
})

// expectReadOnlyPosture compiles the rendered request the way the provider
// decorator does and checks the runtime's native read-only posture: claude
// denies its write, shell and sub-agent tools; codex runs a read-only sandbox
// that never asks. Web tools stay allowed on both.
func expectReadOnlyPosture(rendered PromptRenderResult) {
	runtime := api.RuntimeOf(rendered.Config.Model.Provider, rendered.Config.Model.Mode)
	compiled, _, err := api.CompileAccess(runtime, rendered.Input)
	Expect(err).NotTo(HaveOccurred())
	Expect(compiled.Permissions.MCP.Disabled).To(BeTrue())
	switch rendered.Provider {
	case "anthropic":
		Expect(compiled.Permissions.Tools.DenyList()).To(ContainElements("Agent", "Bash", "Edit", "Task", "Write"))
		Expect(compiled.Permissions.Tools.DenyList()).NotTo(ContainElements("WebFetch", "WebSearch", "Read"))
	case "openai":
		Expect(compiled.Sandbox.Policy.Filesystem.Access).To(Equal(api.SandboxFilesystemReadOnly))
		Expect(compiled.Permissions.Mode).To(Equal(api.PermissionBypass))
		Expect(compiled.Permissions.Tools).To(BeEmpty())
	default:
		Fail("unexpected provider " + rendered.Provider)
	}
}
