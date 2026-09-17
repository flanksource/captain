package runtimeprofiles

import (
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Catalog layers", func() {
	It("materialises an ordered flat preset selection without a profile", func(ctx SpecContext) {
		source := newMemSource("db", SourceDB, true)
		organization := source.presets.put("organization", globalPreset("Organization"))
		review := source.presets.put("review", PresetInput{
			Name: "Review", Scope: api.SpecLayerSurface,
			Spec: api.RuntimePresetSpec(api.Spec{Budget: api.Budget{MaxTurns: 3}}),
		})
		catalog, err := NewCatalog(source)
		Expect(err).NotTo(HaveOccurred())

		resolution, err := catalog.PresetLayers(ctx, []string{"organization", review.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolution.Presets).To(Equal([]Preset{organization, review}))
		Expect(resolution.Layers).To(Equal([]api.SpecLayer{
			{ID: organization.ID, Name: "Organization", Scope: api.SpecLayerGlobal, Source: api.SpecLayerSourcePreset,
				Spec: api.Spec{Model: api.Model{Name: "gpt-5", Mode: api.ModeAgent}}},
			{ID: review.ID, Name: "Review", Scope: api.SpecLayerSurface, Source: api.SpecLayerSourcePreset,
				Spec: api.Spec{Budget: api.Budget{MaxTurns: 3}}},
		}))
	})

	It("loads nested references but refuses to execute them", func(ctx SpecContext) {
		source := newMemSource("db", SourceDB, true)
		organization := source.presets.put("organization", globalPreset("Organization"))
		team := source.presets.put("team", PresetInput{
			Name: "Team", Scope: api.SpecLayerGlobal, Presets: []string{organization.ID},
		})
		catalog, err := NewCatalog(source)
		Expect(err).NotTo(HaveOccurred())

		_, err = catalog.PresetLayers(ctx, []string{team.ID})
		Expect(err).To(MatchError(ContainSubstring(api.ErrRuntimePresetNestingUnsupported.Error())))
	})

	It("canonicalises preset names without validating the profile's isolated runtime", func(ctx SpecContext) {
		source := newMemSource("db", SourceDB, true)
		preset := source.presets.put("model", globalPreset("Model"))
		profile := source.profiles.put("review", ProfileInput{
			Name: "Review", Presets: []string{"model"},
			Spec: api.Spec{Permissions: api.Permissions{Mode: api.PermissionDontAsk}},
		})
		catalog, err := NewCatalog(source)
		Expect(err).NotTo(HaveOccurred())

		resolution, err := catalog.Layers(ctx, profile.ID)
		Expect(err).NotTo(HaveOccurred())
		profile.Presets = []string{preset.ID}
		Expect(resolution).To(Equal(Resolution{
			Profile: profile, Presets: []Preset{preset}, Layers: []api.SpecLayer{
				{ID: preset.ID, Name: "Model", Scope: api.SpecLayerGlobal, Source: api.SpecLayerSourcePreset,
					Spec: api.Spec{Model: api.Model{Name: "gpt-5", Mode: api.ModeAgent}}},
				{ID: profile.ID + ":spec", Name: "Review run spec", Scope: api.SpecLayerSurface, Source: api.SpecLayerSourceProfile,
					Spec: api.Spec{Permissions: api.Permissions{Mode: api.PermissionDontAsk}}},
			},
		}))
		resolution, err = catalog.Resolve(ctx, profile.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolution.Resolved.Warnings).To(Equal([]string{`permissions.mode "dontAsk" is not available for openai agent`}))
	})
})
