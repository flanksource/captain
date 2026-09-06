package runtimeprofiles

import (
	"context"
	"errors"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Owned runtime layer validation", func() {
	It("retains catalog ownership when an existing profile references a missing preset", func(ctx SpecContext) {
		source := newMemSource("db", SourceDB, true)
		profile := source.profiles.put("review", ProfileInput{Name: "Review", Presets: []string{"missing"}})
		catalog, err := NewCatalog(source)
		Expect(err).NotTo(HaveOccurred())
		_, err = catalog.Layers(ctx, profile.ID)
		var owned *OwnedLayersError
		Expect(errors.As(err, &owned)).To(BeTrue())
		Expect(owned.Ref).To(Equal(profile.ID))
		Expect(errors.Is(err, ErrNotFound)).To(BeTrue())
		_, err = catalog.Layers(ctx, "missing-profile")
		Expect(errors.As(err, &owned)).To(BeFalse())
		Expect(errors.Is(err, ErrNotFound)).To(BeTrue())
	})

	It("rejects malformed catalog fields before a request can overwrite them", func(ctx SpecContext) {
		source := newMemSource("db", SourceDB, true)
		profile := source.profiles.put("review", ProfileInput{Name: "Review", Spec: api.Spec{Budget: api.Budget{Timeout: "invalid"}}})
		catalog, err := NewCatalog(source)
		Expect(err).NotTo(HaveOccurred())
		resolver := NewResolver(func(context.Context) (*Catalog, error) { return catalog, nil })
		_, err = resolver.Layers(ctx, ResolveOptions{RequestedProfile: profile.ID,
			RequestLayers: []api.SpecLayer{api.RequestSpecLayer("request", api.Spec{Budget: api.Budget{Timeout: "1m"}})},
		})
		var owned *OwnedLayersError
		var structural *api.LayerValidationError
		var selected *SelectionError
		Expect(errors.As(err, &owned)).To(BeTrue())
		Expect(errors.As(err, &structural)).To(BeTrue())
		Expect(errors.As(err, &selected)).To(BeTrue())
		Expect(selected.Origin).To(Equal(SelectionRequested))
		Expect(structural.Layer).To(Equal("Review run spec"))
	})

	It("rejects invalid caller layers without claiming catalog ownership", func(ctx SpecContext) {
		_, err := NewResolver(nil).Layers(ctx, ResolveOptions{RequestLayers: []api.SpecLayer{
			api.RequestSpecLayer("request", api.Spec{Budget: api.Budget{Cost: -1}}),
		}})
		var owned *OwnedLayersError
		var structural *api.LayerValidationError
		Expect(errors.As(err, &owned)).To(BeFalse())
		Expect(errors.As(err, &structural)).To(BeTrue())
		Expect(structural.Layer).To(Equal("request"))
	})

	It("allows partial reusable presets and rejects malformed profile writes", func() {
		Expect((PresetInput{Name: "Effort", Scope: api.SpecLayerGlobal,
			Spec: api.RuntimePresetSpec{Model: api.Model{Mode: api.ModeAgent, Effort: api.EffortHigh}},
		}).validate()).To(Succeed())
		err := (ProfileInput{Name: "Review", Spec: api.Spec{Model: api.Model{Effort: "invalid"}}}).validate()
		var structural *api.LayerValidationError
		Expect(errors.As(err, &structural)).To(BeTrue())
		Expect(errors.Is(err, ErrInvalid)).To(BeTrue())
	})
})
