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

	g.It("reports a caller-selected missing preset as a request error", func() {
		ctx := context.Background()
		source, err := runtimeprofiles.NewFileSource(runtimeprofiles.FileSourceOptions{
			Kind: runtimeprofiles.KindPreset, Dir: filepath.Join(g.GinkgoT().TempDir(), "presets"), Label: "test presets", Implicit: true,
		})
		Expect(err).NotTo(HaveOccurred())
		catalog, err := runtimeprofiles.NewCatalog(source)
		Expect(err).NotTo(HaveOccurred())
		provider, err := NewLayeredRuntimeProfileProvider(LayeredRuntimeProfileProviderOptions{
			Resolver: runtimeprofiles.NewResolver(func(context.Context) (*runtimeprofiles.Catalog, error) { return catalog, nil }),
			Base:     func(context.Context) (RuntimeProfileBase, error) { return RuntimeProfileBase{}, nil },
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = provider.RuntimeProfile(ctx, WithRuntimePresets([]string{"missing"}))
		Expect(runtimeProfileStatus(err)).To(Equal(http.StatusBadRequest))
		Expect(err).To(MatchError(ContainSubstring("missing")))
	})
})
