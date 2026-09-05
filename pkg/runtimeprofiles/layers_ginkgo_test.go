package runtimeprofiles

import (
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Catalog layers", func() {
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
		_, err = catalog.Resolve(ctx, profile.ID)
		Expect(err).To(MatchError(ContainSubstring(`permissions.mode "dontAsk" is not available for openai agent`)))
	})
})
