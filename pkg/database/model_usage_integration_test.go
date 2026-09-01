package database

import (
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Model usage aggregation", func() {
	It("counts only the sessions an owner's subquery selects", func(ctx SpecContext) {
		const (
			tenantTitle       = "tenant usage session"
			otherTenantTitle  = "other tenant usage session"
			tenantInputTokens = 25
			tenantOutputToken = 7
			// The tenant's model carries no catalog price, so the run's whole
			// cost is what the provider reported. Summing the list-price
			// buckets alone reports $0 for it.
			tenantProviderCostUSD = 0.04
			otherProviderCostUSD  = 9.99
		)
		db := openSchemaScopedDatabases(ctx, "captain_model_usage", "agent_usage_scope_context")[0]
		since := time.Now().Add(-time.Hour)

		record := func(title string, usage api.Usage, providerCostUSD float64) uuid.UUID {
			session, err := db.CreateOrGetSession(ctx, CreateSessionInput{
				ProviderSessionID: title, Source: "codex", Provider: "openai",
				HostID: "usage-scope-test", Title: title,
			})
			Expect(err).NotTo(HaveOccurred())
			turn, _, err := db.CreateChatTurn(ctx, CreateChatTurnInput{
				SessionID: session.ID, ProviderTurnID: title + " turn",
			})
			Expect(err).NotTo(HaveOccurred())
			run, err := db.CreatePromptRun(ctx, CreatePromptRunInput{SessionID: session.ID, TurnID: &turn.ID})
			Expect(err).NotTo(HaveOccurred())
			callID, err := db.CreateChatModelCall(ctx, CreateChatModelCallInput{
				TurnID: turn.ID, PromptRunID: run.ID, Model: "model-usage-scope", Provider: "openai", Mode: "cli",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(db.FinishChatModelCall(ctx, FinishChatModelCallInput{
				ID: callID, Status: ModelCallStatusSucceeded, StopReason: "end_turn",
				Event: api.Event{Usage: &usage, CostUSD: providerCostUSD}, Cost: &api.Cost{},
			})).To(Succeed())
			return session.ID
		}

		record(tenantTitle, api.Usage{InputTokens: tenantInputTokens, OutputTokens: tenantOutputToken}, tenantProviderCostUSD)
		record(otherTenantTitle, api.Usage{InputTokens: 500, OutputTokens: 500}, otherProviderCostUSD)

		// Stands in for an embedding application's ownership table: it knows
		// which sessions belong to the tenant, Captain knows what they cost.
		owned := db.Gorm().Table("agent_usage_scope_context.captain_sessions").
			Select("id").Where("title = ?", tenantTitle)

		usage, err := db.ModelUsage(ctx, ModelUsageQuery{
			Since: since, Sessions: owned, Schemas: []string{"agent_usage_scope_context"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(usage.TotalTokens).To(Equal(tenantInputTokens + tenantOutputToken))
		Expect(usage.TotalCostUSD).To(BeNumerically("~", tenantProviderCostUSD, 0.000000001))

		unscoped, err := db.ModelUsage(ctx, ModelUsageQuery{
			Since: since, Schemas: []string{"agent_usage_scope_context"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(unscoped.TotalTokens).To(Equal(tenantInputTokens + tenantOutputToken + 1000))
		Expect(unscoped.TotalCostUSD).To(BeNumerically("~", tenantProviderCostUSD+otherProviderCostUSD, 0.000000001))
	})
})
