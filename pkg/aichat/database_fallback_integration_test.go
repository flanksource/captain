package aichat_test

import (
	"encoding/json"
	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net/http"
	"net/http/httptest"
)

var _ = Describe("Database chat sessions", func() {
	It("binds and accounts for the provider candidate selected after fallback", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_selected_fallback"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		store, err := aichat.NewDatabaseThreadStore(db)
		Expect(err).NotTo(HaveOccurred())
		thread, err := store.Create(ctx, "Fallback")
		Expect(err).NotTo(HaveOccurred())
		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		provider := &fakeStreamingProvider{
			model: "gemini-2.5-pro", runtime: api.RuntimeOf(api.Google, api.ModeAPI),
			events: []api.Event{
				{Kind: api.EventSystem, SessionID: "fallback-session", Model: "gemini-2.5-pro"},
				{Kind: api.EventText, Text: "Fallback answer", Model: "gemini-2.5-pro"},
				{Kind: api.EventResult, Success: true, Model: "gemini-2.5-pro", CostUSD: 0.25,
					Usage: &api.Usage{InputTokens: 12, OutputTokens: 4}},
			},
		}
		resolver := &fakeResolver{provider: provider}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver, Threads: aichat.FixedThreadStore(store), Authority: authority,
		})
		submit := func(id string, runtime api.Model) *httptest.ResponseRecorder {
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
				ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message", Runtime: &runtime,
				Messages: []aichat.UIMessage{{
					ID: id, Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Use the selected runtime"}},
				}},
			}))
			return response
		}

		primary := api.Model{
			Name: "gpt-5.6-sol", Mode: api.ModeAPI,
			Fallbacks: []api.Model{{Name: "gemini-2.5-pro", Mode: api.ModeAPI, Effort: api.EffortHigh}},
		}
		first := submit("fallback-user-1", primary)
		Expect(first.Code).To(Equal(http.StatusOK), first.Body.String())
		stored, err := store.Get(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Runtime).To(Equal(&api.RuntimeIdentity{
			Model: "gemini-2.5-pro", Provider: api.Google.Name, Mode: api.ModeAPI,
		}), "the identity records the fallback candidate that actually ran — model and runtime, not effort")
		Expect(stored.ProviderSessionID).To(Equal("fallback-session"))

		aggregate, err := store.GetSession(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(aggregate.Model).To(Equal("gemini-2.5-pro"))
		Expect(aggregate.ModelMode).To(Equal(api.ModeAPI))
		Expect(aggregate.Usage.InputTokens).To(Equal(12))
		Expect(aggregate.Cost.Total()).To(BeNumerically("~", 0.25, 0.000001))
		sessionID := uuid.MustParse(thread.ID)
		runs, err := db.ListPromptRuns(ctx, database.PromptRunFilter{SessionID: &sessionID})
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(HaveLen(1))
		Expect(runs[0].Runtime.Requested.Model).To(Equal(primary.Name))
		Expect(runs[0].Runtime.Resolved.Model).To(Equal("gemini-2.5-pro"))
		Expect(runs[0].Runtime.Resolved.Effort).To(BeEmpty(), "the catalog declares no effort tiers for this model")
		resolutionJSON, err := json.Marshal(runs[0].RenderedSpec["resolution"])
		Expect(err).NotTo(HaveOccurred())
		var resolution struct {
			Trace []api.SpecLayer `json:"trace"`
		}
		Expect(json.Unmarshal(resolutionJSON, &resolution)).To(Succeed())
		Expect(resolution.Trace[0].Spec.Fallbacks[0].Effort).To(Equal(api.EffortHigh), "raw authored effort survives final catalog normalization")

		conflict := submit("fallback-user-conflict", primary)
		Expect(conflict.Code).To(Equal(http.StatusConflict), conflict.Body.String())
		selected := api.Model{Name: "gemini-2.5-pro", Mode: api.ModeAPI}
		second := submit("fallback-user-2", selected)
		Expect(second.Code).To(Equal(http.StatusOK), second.Body.String())
		Expect(resolver.configs).To(HaveLen(2))
		Expect(resolver.configs[1].SessionID).To(Equal("fallback-session"))
	})

})
