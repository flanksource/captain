package api

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Effective layered runtime validation", func() {
	It("resolves after a request repairs a profile runtime and preserves authored trace", func() {
		profile := PromptSpecLayer("profile", Spec{Model: Model{Name: "agent:sol"}, Permissions: Permissions{Tools: Tools{"Bash": ToolPolicyDeny}}})
		request := RequestSpecLayer("request", Spec{Model: Model{Name: "sonnet", Mode: ModeCLI}})
		Expect(ValidateSpecLayers(profile)).To(Succeed())
		_, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{profile}})
		Expect(err).To(HaveOccurred())
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{profile, request}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Name).To(Equal("claude-sonnet-5"))
		Expect(resolved.Spec.Provider).To(Equal(Anthropic))
		Expect(resolved.Spec.Mode).To(Equal(ModeCLI))
		Expect(resolved.Trace).To(Equal([]SpecLayer{profile, request}))
		Expect(resolved.Warnings).To(BeEmpty())
	})

	It("keeps an explicit API mode after subsequent provider model resolution", func() {
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{PromptSpecLayer("profile", Spec{Model: Model{Name: "api:sonnet"}})}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Mode).To(Equal(ModeAPI))
		again, err := ResolveModel(resolved.Spec.Model)
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(Equal(resolved.Spec.Model))
	})

	It("returns unsupported primary and fallback resource policies as warnings", func() {
		layer := PromptSpecLayer("profile", Spec{
			Model:       Model{Name: "agent:sonnet", Fallbacks: []Model{{Name: "cli:sol"}}},
			Permissions: Permissions{Plugins: ResourcePolicies{"example": ResourceEnabled}},
		})
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{layer}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Warnings).To(ConsistOf(
			"resource policy plugins=enabled is not available for anthropic agent",
			`fallback[0] "gpt-5.6-sol": resource policy plugins=enabled is not available for openai cli`,
		))
		Expect(resolved.Trace).To(Equal([]SpecLayer{layer}))
	})

	DescribeTable("refuses unsupported isolation on every candidate", func(model Model) {
		_, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{PromptSpecLayer("profile", Spec{Model: model, Sandbox: &SandboxRef{Mode: SandboxNative}})}})
		Expect(err).To(MatchError(ContainSubstring(`sandbox mode "native" is not available`)))
	},
		Entry("primary", Model{Name: "api:sonnet"}),
		Entry("fallback", Model{Name: "agent:sonnet", Fallbacks: []Model{{Name: "api:sol"}}}),
	)

	It("retains hard agent-tool policy refusal for fallback runtimes", func() {
		_, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{PromptSpecLayer("profile", Spec{
			Model:       Model{Name: "cli:sonnet", Fallbacks: []Model{{Name: "cli:sol"}}},
			Permissions: Permissions{Tools: Tools{"Bash": ToolPolicyDeny}},
		})}})
		Expect(err).To(MatchError(ContainSubstring("tool policy")))
	})

	It("uses native translators for unsupported policy fields on fallbacks", func() {
		_, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{PromptSpecLayer("profile", Spec{
			Model: Model{Name: "agent:sonnet", Fallbacks: []Model{{Name: "agent:sol"}}},
			Sandbox: &SandboxRef{Mode: SandboxNative, Policy: &NativeSandboxPolicy{
				Network: &SandboxNetworkPolicy{AllowedDomains: []string{"example.com"}},
			}},
		})}})
		Expect(err).To(MatchError(ContainSubstring("allowedDomains")))
	})

	It("preserves supported native sandbox policy on primary and fallback", func() {
		layer := PromptSpecLayer("profile", Spec{
			Model: Model{Name: "agent:sonnet", Fallbacks: []Model{{Name: "agent:sol"}}},
			Sandbox: &SandboxRef{Mode: SandboxNative, Policy: &NativeSandboxPolicy{
				Filesystem: &SandboxFilesystemPolicy{Access: SandboxFilesystemWorkspaceWrite},
			}},
		})
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{layer}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Sandbox).To(Equal(layer.Spec.Sandbox))
		Expect(resolved.Trace).To(Equal([]SpecLayer{layer}))
	})

	It("keeps incomplete model-free verification valid without inventing runtime warnings", func() {
		layer := PromptSpecLayer("fixture", Spec{Model: Model{Mode: ModeAgent},
			Workflow:    &Workflow{Verify: &Verify{Commands: []string{"true"}}},
			Permissions: Permissions{Plugins: ResourcePolicies{"example": ResourceEnabled}},
		})
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{layer}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Model).To(Equal(layer.Spec.Model))
		Expect(resolved.Warnings).To(BeEmpty())
		Expect(resolved.Trace).To(Equal([]SpecLayer{layer}))
	})

	It("matches an authored alias constraint against the canonical primary and fallback", func() {
		layer := SpecLayer{Name: "catalog", Scope: SpecLayerGlobal,
			Constraints: RuntimeConstraints{Models: []string{"sol", "sonnet"}},
			Spec:        Spec{Model: Model{Name: "sol", Fallbacks: []Model{{Name: "sonnet"}}}},
		}
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{layer}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Name).To(Equal("gpt-5.6-sol"))
		Expect(resolved.Spec.Fallbacks[0].Name).To(Equal("claude-sonnet-5"))
		Expect(resolved.Constraints).To(Equal(layer.Constraints))
		Expect(ValidateRuntimeConstraints(resolved, resolved.Spec.Model, 0)).To(Succeed())
	})
})
