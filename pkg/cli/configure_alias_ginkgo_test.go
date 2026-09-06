package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("provider alias configuration", Serial, func() {
	configurePath := func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
	}
	stubGoogleModels := func() {
		previous := configureDefaultsModels
		configureDefaultsModels = func(_ context.Context, provider *api.ModelProvider, mode api.RuntimeMode) ([]ai.ModelDef, error) {
			Expect(provider).To(BeIdenticalTo(api.Google))
			Expect(mode).To(Equal(api.ModeCLI))
			return []ai.ModelDef{{ID: "gemini-3.5-flash"}}, nil
		}
		DeferCleanup(func() { configureDefaultsModels = previous })
	}
	seedAlias := func() {
		Expect(captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{
			Providers: map[string]captainconfig.ProviderDefaults{
				"gemini": {Mode: "cli", Model: "gemini-3.5-flash", ReasoningEffort: "high"},
			},
		}})).To(Succeed())
	}

	It("updates an existing agent-named provider block from the CLI", func() {
		configurePath()
		stubGoogleModels()
		seedAlias()

		_, err := runProviderDefaultsConfigure(context.Background(), api.Google, ConfigureOptions{Effort: "default"})
		Expect(err).NotTo(HaveOccurred())

		config, _, err := captainconfig.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(config.AI.Providers).To(HaveKey("gemini"))
		Expect(config.AI.Providers).NotTo(HaveKey("google"))
		Expect(config.AI.Providers["gemini"].ReasoningEffort).To(BeEmpty())
		Expect(config.AI.Validate()).To(Succeed())
	})

	It("updates an existing agent-named provider block from the HTTP API", func() {
		configurePath()
		stubGoogleModels()
		seedAlias()

		mux := http.NewServeMux()
		registerProviderDefaultsHandlers(mux)
		request := httptest.NewRequest(http.MethodPut, "/api/captain/ai/providers/gemini/defaults",
			strings.NewReader(`{"mode":"cli","model":"gemini-3.5-flash","effort":"medium"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://localhost:9020")
		request.Host = "localhost:9020"
		request.RemoteAddr = "127.0.0.1:1234"
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())

		config, _, err := captainconfig.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(config.AI.Providers).To(HaveKey("gemini"))
		Expect(config.AI.Providers).NotTo(HaveKey("google"))
		Expect(config.AI.Providers["gemini"].ReasoningEffort).To(Equal("medium"))
		Expect(config.AI.Validate()).To(Succeed())
	})

	It("merges the form selection by provider identity", func() {
		merged, err := mergeConfiguredProvider(providerDefaultsMergeOptions{
			Current: captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{
				"anthropic": {Mode: "agent", Model: "claude-existing"},
				"gemini":    {Mode: "cli", Model: "gemini-existing"},
			}},
			Next: captainconfig.AIDefaults{
				DefaultProvider: "google",
				BudgetUSD:       2,
				Providers: map[string]captainconfig.ProviderDefaults{
					"google": {Mode: "cli", Model: "gemini-3.5-flash", ReasoningEffort: "medium"},
				},
			},
			Provider: api.Google,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(merged.Providers).To(Equal(map[string]captainconfig.ProviderDefaults{
			"anthropic": {Mode: "agent", Model: "claude-existing"},
			"gemini":    {Mode: "cli", Model: "gemini-3.5-flash", ReasoningEffort: "medium"},
		}))
		Expect(merged.BudgetUSD).To(Equal(2.0))
		Expect(merged.Validate()).To(Succeed())
	})

	It("installs an agent-named provider opt-out under its provider identity", func() {
		configurePath()
		api.SetDisabled(api.DisabledSet{})
		DeferCleanup(func() { api.SetDisabled(api.DisabledSet{}) })

		mux := http.NewServeMux()
		registerDisabledHandlers(mux)
		request := httptest.NewRequest(http.MethodPut, "/api/captain/ai/disabled",
			strings.NewReader(`{"modes":[],"providers":["gemini"],"runtimes":[],"models":[],"efforts":[]}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://localhost:9020")
		request.Host = "localhost:9020"
		request.RemoteAddr = "127.0.0.1:1234"
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(api.Disabled().Provider(api.Google)).To(BeTrue())
	})

	It("counts provider aliases by identity when checking that one remains enabled", func() {
		configurePath()
		api.SetDisabled(api.DisabledSet{})
		DeferCleanup(func() { api.SetDisabled(api.DisabledSet{}) })

		mux := http.NewServeMux()
		registerDisabledHandlers(mux)
		request := httptest.NewRequest(http.MethodPut, "/api/captain/ai/disabled",
			strings.NewReader(`{"modes":[],"providers":["google","gemini","anthropic","openai"],"runtimes":[],"models":[],"efforts":[]}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://localhost:9020")
		request.Host = "localhost:9020"
		request.RemoteAddr = "127.0.0.1:1234"
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(api.Disabled().Provider(api.Google)).To(BeTrue())
		Expect(api.Disabled().Provider(api.DeepSeek)).To(BeFalse())
	})
})
