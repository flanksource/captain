package aichat

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("Layered chat profile ownership", func() {
	g.It("validates malformed server base layers before selecting a missing request profile", func() {
		catalogCalls := 0
		provider, err := NewLayeredRuntimeProfileProvider(LayeredRuntimeProfileProviderOptions{
			Resolver: runtimeprofiles.NewResolver(func(context.Context) (*runtimeprofiles.Catalog, error) {
				catalogCalls++
				return nil, runtimeprofiles.ErrNotFound
			}),
			Base: func(context.Context) (RuntimeProfileBase, error) {
				return RuntimeProfileBase{Layers: []api.SpecLayer{{Name: "application", Scope: api.SpecLayerGlobal,
					Spec: api.Spec{Model: api.Model{Effort: "invalid"}},
				}}}, nil
			},
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = provider.RuntimeProfile(context.Background(), WithRuntimeProfileRef("missing"))
		Expect(runtimeProfileStatus(err)).To(Equal(http.StatusInternalServerError))
		Expect(err).To(MatchError(ContainSubstring("application")))
		Expect(catalogCalls).To(BeZero())
	})

	g.It("keeps a selected profile's missing preset as a server defect", func() {
		ctx := context.Background()
		source, err := runtimeprofiles.NewFileSource(runtimeprofiles.FileSourceOptions{
			Kind: runtimeprofiles.KindProfile, Dir: filepath.Join(g.GinkgoT().TempDir(), "profiles"), Label: "test profiles", Implicit: true,
		})
		Expect(err).NotTo(HaveOccurred())
		catalog, err := runtimeprofiles.NewCatalog(source)
		Expect(err).NotTo(HaveOccurred())
		_, err = catalog.CreateProfile(ctx, source.Info().ID, runtimeprofiles.ProfileInput{Name: "Broken", Presets: []string{"missing"}})
		Expect(err).NotTo(HaveOccurred())
		provider, err := NewLayeredRuntimeProfileProvider(LayeredRuntimeProfileProviderOptions{
			Resolver: runtimeprofiles.NewResolver(func(context.Context) (*runtimeprofiles.Catalog, error) { return catalog, nil }),
			Base:     func(context.Context) (RuntimeProfileBase, error) { return RuntimeProfileBase{}, nil },
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = provider.RuntimeProfile(ctx, WithRuntimeProfileRef("broken"))
		Expect(runtimeProfileStatus(err)).To(Equal(http.StatusInternalServerError))
		Expect(err).To(MatchError(ContainSubstring("missing")))
	})
})
