package api

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Structural spec layer validation", func() {
	invalidTemperature := 3.0
	DescribeTable("rejects malformed authored fields even if a request replaces them", func(invalid Spec, fragment string) {
		layer := PromptSpecLayer("project.prompt", invalid)
		err := ValidateSpecLayers(layer)
		Expect(err).To(HaveOccurred())
		var structural *LayerValidationError
		Expect(errors.As(err, &structural)).To(BeTrue())
		Expect(structural.Layer).To(Equal("project.prompt"))
		Expect(err.Error()).To(ContainSubstring(fragment))
		_, err = ResolveSpecLayers(layer, RequestSpecLayer("request", Spec{Model: Model{Name: "agent:sonnet"}, Budget: Budget{Timeout: "1m"}}))
		Expect(errors.As(err, &structural)).To(BeTrue())
	},
		Entry("budget", Spec{Budget: Budget{Timeout: "tomorrow"}}, "timeout"),
		Entry("mode hidden by compact name", Spec{Model: Model{Name: "agent:sonnet", Mode: "invalid"}}, "mode"),
		Entry("effort hidden by compact name", Spec{Model: Model{Name: "agent:sonnet:high", Effort: "invalid"}}, "effort"),
		Entry("temperature", Spec{Model: Model{Temperature: &invalidTemperature}}, "temperature"),
		Entry("fallback options", Spec{Model: Model{Fallbacks: []Model{{Name: "sol", Effort: "invalid"}}}}, "effort"),
		Entry("fallback compact mode", Spec{Model: Model{Fallbacks: []Model{{Name: "invalid:sol"}}}}, "mode"),
		Entry("permissions", Spec{Permissions: Permissions{Mode: "invalid"}}, "permissions"),
		Entry("strictness", Spec{Prompt: Prompt{SchemaStrictness: "invalid"}}, "schemaStrictness"),
		Entry("attachment", Spec{Prompt: Prompt{Attachments: []AttachmentRef{{URL: "file:///private/data"}}}}, "attachment"),
		Entry("workflow", Spec{Workflow: &Workflow{Verify: &Verify{MaxIterations: -1}}}, "maxIterations"),
		Entry("sandbox", Spec{Sandbox: &SandboxRef{Mode: "invalid"}}, "sandbox"),
	)

	It("attributes malformed layer metadata and constraints", func() {
		for _, layer := range []SpecLayer{
			{Name: "scope", Scope: "invalid"},
			{Name: "limits", Scope: SpecLayerGlobal, Constraints: RuntimeConstraints{Limits: RunLimits{MaxInputTokens: -1}}},
		} {
			err := ValidateSpecLayers(layer)
			var structural *LayerValidationError
			Expect(errors.As(err, &structural)).To(BeTrue())
			Expect(structural.Layer).To(Equal(layer.Name))
		}
	})

	It("accepts incomplete models and named models awaiting a request runtime", func() {
		layers := []SpecLayer{
			{Name: "defaults", Scope: SpecLayerGlobal, Spec: Spec{Model: Model{Mode: ModeAgent, Effort: EffortHigh}, Permissions: Permissions{Mode: PermissionPlan}}},
			PromptSpecLayer("unknown.prompt", Spec{Model: Model{Name: "unregistered-model"}}),
		}
		Expect(ValidateSpecLayers(layers...)).To(Succeed())
		composed, err := ComposeSpecLayers(layers...)
		Expect(err).NotTo(HaveOccurred())
		Expect(composed.Spec.Model).To(Equal(Model{Name: "unregistered-model", Mode: ModeAgent, Effort: EffortHigh}))
		Expect(composed.Trace).To(Equal(layers))
	})
})
