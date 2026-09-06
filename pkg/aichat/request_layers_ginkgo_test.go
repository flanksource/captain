package aichat

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("Chat request layers", func() {
	profile := func(spec api.Spec) RuntimeProfile {
		return RuntimeProfile{Composed: api.ComposedSpec{Trace: []api.SpecLayer{
			{Name: "application", Scope: api.SpecLayerGlobal, Spec: spec},
		}}}
	}
	request := ChatRequest{Messages: []UIMessage{{Role: "user", Parts: []UIPart{{Type: "text", Text: "Hello"}}}}}

	g.It("keeps inherited model fields out of the explicit request trace", func() {
		base := profile(api.Spec{Model: api.Model{Name: "gpt-5.6-sol", Mode: api.ModeAPI, Effort: api.EffortHigh}})
		service := NewService(ServiceOptions{Profile: RuntimeProfileProviderFunc(func(context.Context, ...RuntimeProfileOption) (RuntimeProfile, error) {
			return base, nil
		})})
		loaded, err := service.runtimeProfile(context.Background())
		Expect(err).NotTo(HaveOccurred())
		resolved, err := requestSpec(request, loaded, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Trace[1].Spec.Model).To(Equal(api.Model{}))
		Expect(resolved.Spec.Mode).To(Equal(api.ModeAPI))
		Expect(resolved.Spec.Effort).To(Equal(api.EffortHigh))
	})

	g.It("merges a bare catalog model only when composing the final request", func() {
		base := profile(api.Spec{Model: api.Model{Name: "gpt-5.6-luna", Mode: api.ModeAPI, Effort: api.EffortHigh}})
		base.Composed.Spec = base.Composed.Trace[0].Spec
		selected := request
		selected.Model = "openai/gpt-5.6-sol"
		resolved, err := requestSpec(selected, base, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Trace[1].Spec.Model).To(Equal(api.Model{Name: "openai/gpt-5.6-sol"}))
		Expect(resolved.Spec.Mode).To(Equal(api.ModeAPI))
		Expect(resolved.Spec.Effort).To(Equal(api.EffortHigh))
	})

	g.DescribeTable("rejects authored model defects before expanding compact selectors", func(model api.Model) {
		_, err := chatModel(ChatRequest{Runtime: &model})
		Expect(err).To(HaveOccurred())
	},
		g.Entry("invalid mode", api.Model{Name: "agent:sonnet:high", Mode: "invalid"}),
		g.Entry("invalid effort", api.Model{Name: "agent:sonnet:high", Effort: "invalid"}),
	)

	g.It("preserves the caller's compact selector in the raw request layer", func() {
		model := api.Model{Name: "agent:sonnet:high"}
		selected, err := chatModel(ChatRequest{Runtime: &model})
		Expect(err).NotTo(HaveOccurred())
		Expect(selected).To(Equal(model))
	})
})
