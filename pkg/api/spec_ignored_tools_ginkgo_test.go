package api

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Spec provenance for tools a runtime ignores", func() {
	const toolName = "emit-choices"
	toolSpec := func(model string) Spec {
		return Spec{
			Model:           Model{Name: model},
			Tools:           InlineTools{toolName: {Exec: "echo {}", ReadOnly: true}},
			ToolPolicy:      PermissionPolicy{{ToolMatch: ToolMatch{Name: MatchPatterns{toolName}}, Policy: ToolPolicyAllow}},
			ToolPreferences: ToolPreferences{toolName: ToolPolicyAllow},
			Permissions:     Permissions{Tools: Tools{"Bash": ToolPolicyDeny}},
		}
	}
	resolve := func(model string) ResolvedSpec {
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{PromptSpecLayer("prompt", toolSpec(model))}})
		Expect(err).NotTo(HaveOccurred())
		return resolved
	}

	It("resolves a Jev spec with inline tools and tool policies, marking each set tool path ignored by jev", func() {
		resolved := resolve("api:jev-latest")
		want := &FieldIgnore{
			By:     FieldSource{Kind: FieldSourceCatalog, Name: "jev", Key: "registry.IgnoresTools"},
			Reason: "jev takes no tools; inline tools run only from hooks",
		}
		for _, path := range []string{"/tools/" + toolName, "/tools/" + toolName + "/exec", "/toolPolicy", "/toolPreferences/" + toolName, "/permissions/tools/Bash"} {
			Expect(resolved.Provenance).To(HaveKey(path))
			Expect(resolved.Provenance[path].Ignored).To(Equal(want), path)
			Expect(resolved.Provenance[path].Source.Name).To(Equal("prompt"), path)
		}
		Expect(resolved.Spec.Tools).To(HaveKey(toolName))
	})

	It("records no ignored marker on a runtime that exposes caller tools", func() {
		resolved := resolve("agent:sonnet")
		for path, provenance := range resolved.Provenance {
			Expect(provenance.Ignored).To(BeNil(), path)
		}
	})
})
