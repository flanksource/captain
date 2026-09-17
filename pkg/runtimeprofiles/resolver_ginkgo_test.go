package runtimeprofiles

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Runtime preset resolver", func() {
	It("warns and ignores a deprecated profile selection", func(ctx SpecContext) {
		source := newMemSource("db", SourceDB, true)
		profile := source.profiles.put("review", ProfileInput{
			Name: "Review", Spec: api.Spec{Permissions: api.Permissions{Mode: api.PermissionDontAsk}},
		})
		catalog, err := NewCatalog(source)
		Expect(err).NotTo(HaveOccurred())
		resolver := NewResolver(func(context.Context) (*Catalog, error) { return catalog, nil })
		base := api.SpecLayer{Name: "defaults", Scope: api.SpecLayerGlobal, Spec: api.Spec{Budget: api.Budget{MaxTurns: 5}}}
		surface := api.PromptSpecLayer("prompt", api.Spec{Budget: api.Budget{Cost: 2}})
		request := api.RequestSpecLayer("request", api.Spec{Budget: api.Budget{Timeout: "1m"}})
		result, err := resolver.Layers(ctx, ResolveOptions{
			BaseLayers: []api.SpecLayer{base}, RequestedProfile: profile.ID,
			SurfaceLayers: []api.SpecLayer{surface}, RequestLayers: []api.SpecLayer{request},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Profile).To(BeNil())
		Expect(result.Presets).To(BeNil())
		Expect(result.Warnings).To(Equal([]string{api.RuntimeProfileDeprecationWarning}))
		Expect(result.Layers).To(Equal([]api.SpecLayer{
			base, surface, request,
		}))
	})

	It("retains the selected origin when the preset catalog is unavailable", func(ctx SpecContext) {
		_, err := NewResolver(nil).Layers(ctx, ResolveOptions{RequestedPresets: []string{" requested "}, RequestedPresetsSet: true})
		Expect(err).To(MatchError(&SelectionError{Origin: SelectionRequested, Ref: "requested", Err: ErrCatalogUnavailable}))
	})

	It("places user-scope presets after surface layers and before the request", func(ctx SpecContext) {
		source := newMemSource("db", SourceDB, true)
		preset := globalPreset("User model")
		preset.Scope = api.SpecLayerUser
		record := source.presets.put("user-model", preset)
		catalog, err := NewCatalog(source)
		Expect(err).NotTo(HaveOccurred())
		resolver := NewResolver(func(context.Context) (*Catalog, error) { return catalog, nil })
		surface := api.PromptSpecLayer("prompt", api.Spec{Model: api.Model{Name: "haiku"}})
		request := api.RequestSpecLayer("request", api.Spec{Model: api.Model{Name: "sonnet"}})
		result, err := resolver.Layers(ctx, ResolveOptions{
			RequestedPresets: []string{record.ID}, RequestedPresetsSet: true,
			SurfaceLayers: []api.SpecLayer{surface}, RequestLayers: []api.SpecLayer{request},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Layers).To(HaveLen(3))
		Expect(result.Layers[0]).To(Equal(surface))
		Expect(result.Layers[1].Name).To(Equal("User model"))
		Expect(result.Layers[2]).To(Equal(request))
	})

	DescribeTable("assembles the full stack before resolving the preset runtime",
		func(ctx SpecContext, presetModel api.Model) {
			source := newMemSource("db", SourceDB, true)
			preset := source.presets.put("review", PresetInput{
				Name: "Review", Scope: api.SpecLayerSurface,
				Spec: api.RuntimePresetSpec(api.Spec{Model: presetModel, Permissions: api.Permissions{Mode: api.PermissionDontAsk}}),
			})
			catalog, err := NewCatalog(source)
			Expect(err).NotTo(HaveOccurred())
			resolver := NewResolver(func(context.Context) (*Catalog, error) { return catalog, nil })
			request := api.RequestSpecLayer("request", api.Spec{
				Model: api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeCLI},
			})

			result, err := resolver.Resolve(ctx, ResolveOptions{
				RequestedPresets: []string{preset.ID}, RequestedPresetsSet: true, RequestLayers: []api.SpecLayer{request},
			})
			Expect(err).NotTo(HaveOccurred())
			expected, err := api.ResolveModel(request.Spec.Model)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Resolved.Spec.Model).To(Equal(expected))
			Expect(result.Resolved.Spec.Permissions.Mode).To(Equal(api.PermissionDontAsk))
			Expect(result.Resolved.Trace).To(HaveLen(2))
			Expect(result.Resolved.Trace[0].Spec.Model).To(Equal(presetModel))
			Expect(result.Resolved.Trace[1]).To(Equal(request))
		},
		Entry("without a profile model", api.Model{}),
		Entry("when the request changes an incompatible profile runtime", api.Model{Name: "gpt-5", Mode: api.ModeAgent}),
	)

	It("lets an explicit empty request clear pinned and default presets", func(ctx SpecContext) {
		result, err := NewResolver(nil).Layers(ctx, ResolveOptions{
			RequestedPresetsSet: true,
			PinnedPresets:       []string{"pin"}, PinnedPresetsSet: true,
			DefaultPresets: []string{"default"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Layers).To(BeEmpty())
	})
})
