package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("Chat saved defaults", func() {
	request := ChatRequest{Messages: []UIMessage{{Role: "user", Parts: []UIPart{{Type: "text", Text: "Hello"}}}}}
	profile := func(saved captainconfig.AIDefaults, spec api.Spec) RuntimeProfile {
		return RuntimeProfile{Saved: &saved, Composed: api.ComposedSpec{Trace: []api.SpecLayer{
			{Name: "application", Scope: api.SpecLayerGlobal, Spec: spec},
		}}}
	}

	g.It("uses the same injected defaults for partial composition and final chat resolution", func() {
		saved := captainconfig.AIDefaults{DefaultModel: "api:sonnet:high,api:sol:medium", BudgetUSD: 4}
		service := NewService(ServiceOptions{Profile: RuntimeProfileProviderFunc(func(context.Context, ...RuntimeProfileOption) (RuntimeProfile, error) {
			return profile(saved, api.Spec{}), nil
		})})
		loaded, err := service.runtimeProfile(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Composed.Spec.Name).To(Equal("sonnet"))
		Expect(loaded.Composed.Spec.Budget.Cost).To(Equal(float64(4)))
		resolved, err := requestSpec(request, loaded, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Name).To(Equal("claude-sonnet-5"))
		Expect(resolved.Spec.Fallbacks).To(HaveLen(1))
		Expect(resolved.Spec.Fallbacks[0].Mode).To(Equal(api.ModeAPI))
		Expect(resolved.Trace).To(HaveLen(2))
		Expect(resolved.Trace[1].Spec.Name).To(BeEmpty())
		Expect(resolved.Provenance["/model"].Source.Key).To(Equal("ai.defaultModel"))
	})

	g.It("chooses saved mode defaults for the final provider after a bare request changes families", func() {
		saved := captainconfig.AIDefaults{DefaultModel: "agent:sonnet:high", Providers: map[string]captainconfig.ProviderDefaults{"openai": {Mode: "api", ReasoningEffort: "medium"}}}
		selected := request
		selected.Model = "sol"
		resolved, err := requestSpec(selected, profile(saved, api.Spec{}), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Mode).To(Equal(api.ModeAPI))
		Expect(resolved.Spec.Effort).To(Equal(api.EffortMedium))
		Expect(resolved.Provenance["/mode"].Source.Key).To(Equal("ai.providers.openai.mode"))
	})

	g.It("preserves explicit zero budgets, false cache and empty fallbacks through chat JSON", func() {
		var selected ChatRequest
		Expect(json.Unmarshal([]byte(`{"runtime":{"model":"api:sonnet","noCache":false,"fallbacks":[]},"budget":{"cost":0}}`), &selected)).To(Succeed())
		encoded, err := json.Marshal(selected)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(And(ContainSubstring(`"cost":0`), ContainSubstring(`"noCache":false`), ContainSubstring(`"fallbacks":[]`)))
		Expect(json.Unmarshal(encoded, &selected)).To(Succeed())
		selected.Messages = request.Messages
		resolved, err := requestSpec(selected, profile(captainconfig.AIDefaults{DefaultModel: "api:sonnet,api:sol", BudgetUSD: 4, NoCache: true}, api.Spec{Budget: api.Budget{Cost: 8}}), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Budget.Cost).To(BeZero())
		Expect(resolved.Spec.NoCache).To(BeFalse())
		Expect(resolved.Spec.Fallbacks).To(BeEmpty())
		Expect(resolved.Provenance["/budget/cost"].Source.Name).To(Equal("chat request"))
	})

	g.It("validates malformed saved settings before a missing requested profile changes ownership", func() {
		catalogCalls := 0
		provider, err := NewLayeredRuntimeProfileProvider(LayeredRuntimeProfileProviderOptions{
			Resolver: runtimeprofiles.NewResolver(func(context.Context) (*runtimeprofiles.Catalog, error) {
				catalogCalls++
				return nil, runtimeprofiles.ErrNotFound
			}),
			Base: func(context.Context) (RuntimeProfileBase, error) {
				return RuntimeProfileBase{Saved: &captainconfig.AIDefaults{Temperature: 3}}, nil
			},
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = provider.RuntimeProfile(context.Background(), WithRuntimeProfileRef("missing"))
		Expect(runtimeProfileStatus(err)).To(Equal(http.StatusInternalServerError))
		Expect(err).To(MatchError(ContainSubstring("ai.temperature")))
		Expect(catalogCalls).To(BeZero())
	})

	g.It("returns an actionable typed error for an unconfigured model", func() {
		_, err := requestSpec(request, RuntimeProfile{}, nil)
		Expect(errors.Is(err, aiflags.ErrUnconfigured)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("captain configure"))
	})
})
