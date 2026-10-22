package api

import (
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Preset order", func() {
	It("orders presets around prompt and request layers while retaining scope order for ties", func() {
		layers := []SpecLayer{
			{Name: "late", Scope: SpecLayerContext, Order: math.MaxInt, Spec: Spec{Prompt: Prompt{System: "lint"}}},
			RequestSpecLayer("request", Spec{Prompt: Prompt{System: "request"}}),
			PromptSpecLayer("prompt", Spec{Prompt: Prompt{System: "prompt"}}),
			{Name: "early", Scope: SpecLayerUser, Order: math.MinInt},
			{Name: "default", Scope: SpecLayerContext},
			{Name: "second default", Scope: SpecLayerContext},
		}
		resolved, err := ComposeSpecLayers(ResolveSpecOptions{Layers: layers})
		Expect(err).NotTo(HaveOccurred())
		names := make([]string, len(resolved.Trace))
		for i, layer := range resolved.Trace {
			names[i] = layer.Name
		}
		Expect(names).To(Equal([]string{"early", "default", "second default", "prompt", "request", "late"}))
		Expect(resolved.Spec.Prompt.System).To(Equal("lint"))
		Expect(layers[0].Name).To(Equal("late"))
	})

	It("carries preset order into its resolved layers", func() {
		preset := RuntimePreset{ID: "lint", Name: "Lint", Scope: SpecLayerContext, Order: 100}
		layers, err := RuntimePresetLayers(RuntimePresetResolveRequest{Selected: []string{"lint"}, Presets: []RuntimePreset{preset}})
		Expect(err).NotTo(HaveOccurred())
		Expect(layers).To(Equal([]SpecLayer{{ID: "lint", Name: "Lint", Source: SpecLayerSourcePreset, Scope: SpecLayerContext, Order: 100}}))
	})
})
