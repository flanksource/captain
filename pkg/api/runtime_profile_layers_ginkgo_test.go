package api_test

import (
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Runtime profile layers", func() {
	It("preserves authored models and reference order without resolving or merging", func() {
		layers, err := api.RuntimeProfileLayers(api.RuntimeProfileResolveRequest{
			Profile: api.RuntimeProfile{
				ID: "review", Name: "Review", Presets: []string{"Personal", "organization"},
				Spec: api.Spec{Budget: api.Budget{MaxTurns: 5}, Permissions: api.Permissions{Mode: api.PermissionDontAsk}},
			},
			Presets: []api.RuntimePreset{
				{ID: "organization", Name: "Organization", Scope: api.SpecLayerGlobal, Spec: api.RuntimePresetSpec{
					Model: api.Model{Name: "gpt-5", Mode: api.ModeAgent}, Budget: api.Budget{MaxTurns: 20},
				}},
				{ID: "personal", Name: "Personal", Scope: api.SpecLayerUser, Spec: api.RuntimePresetSpec{
					Budget: api.Budget{MaxTurns: 3},
				}},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(layers).To(Equal([]api.SpecLayer{
			{ID: "personal", Name: "Personal", Scope: api.SpecLayerUser, Source: api.SpecLayerSourcePreset,
				Spec: api.Spec{Budget: api.Budget{MaxTurns: 3}}},
			{ID: "organization", Name: "Organization", Scope: api.SpecLayerGlobal, Source: api.SpecLayerSourcePreset,
				Spec: api.Spec{Model: api.Model{Name: "gpt-5", Mode: api.ModeAgent}, Budget: api.Budget{MaxTurns: 20}}},
			{ID: "review:spec", Name: "Review run spec", Scope: api.SpecLayerSurface, Source: api.SpecLayerSourceProfile,
				Spec: api.Spec{Budget: api.Budget{MaxTurns: 5}, Permissions: api.Permissions{Mode: api.PermissionDontAsk}}},
		}))
	})

	It("retains a permission-only profile without guessing a model", func() {
		layers, err := api.RuntimeProfileLayers(api.RuntimeProfileResolveRequest{Profile: api.RuntimeProfile{
			ID: "plan", Name: "Plan", Spec: api.Spec{Permissions: api.Permissions{Mode: api.PermissionPlan}},
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(layers).To(Equal([]api.SpecLayer{{
			ID: "plan:spec", Name: "Plan run spec", Scope: api.SpecLayerSurface, Source: api.SpecLayerSourceProfile,
			Spec: api.Spec{Permissions: api.Permissions{Mode: api.PermissionPlan}},
		}}))
	})
})
