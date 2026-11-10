package database

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordModelCall records one finished model call in a session titled title, with usage, its
// list-price cost, and the cost its provider reported, and returns the call's ID.
func recordModelCall(ctx context.Context, db *DB, title string, usage api.Usage, cost *api.Cost, providerCostUSD float64) uuid.UUID {
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
		Event: api.Event{Usage: &usage, CostUSD: providerCostUSD}, Cost: cost,
	})).To(Succeed())
	return callID
}

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

		record := func(title string, usage api.Usage, providerCostUSD float64) {
			recordModelCall(ctx, db, title, usage, &api.Cost{}, providerCostUSD)
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

	It("prices each call by ModelCallCostUSD: its provider's cost, else its list price, and nothing outside USD", func(ctx SpecContext) {
		const schema = "agent_call_cost_context"
		db := openSchemaScopedDatabases(ctx, "captain_model_call_cost", schema)[0]
		providerPriced := recordModelCall(ctx, db, "provider priced", api.Usage{InputTokens: 10}, &api.Cost{InputCost: 1}, 0.04)
		listPriced := recordModelCall(ctx, db, "list priced", api.Usage{InputTokens: 10, OutputTokens: 5}, &api.Cost{InputCost: 0.01, OutputCost: 0.02}, 0)
		inEuros := recordModelCall(ctx, db, "in euros", api.Usage{InputTokens: 10}, &api.Cost{InputCost: 5}, 0)
		Expect(db.Gorm().Table(schema+".captain_model_calls").Where("id = ?", inEuros).Update("currency", "EUR").Error).To(Succeed())

		cost := func(id uuid.UUID) float64 {
			var usd float64
			Expect(db.Gorm().Table(schema+".captain_model_calls AS calls").Where("calls.id = ?", id).
				Select(ModelCallCostUSD).Scan(&usd).Error).To(Succeed())
			return usd
		}
		Expect(cost(providerPriced)).To(BeNumerically("~", 0.04, 0.000000001))
		Expect(cost(listPriced)).To(BeNumerically("~", 0.03, 0.000000001))
		Expect(cost(inEuros)).To(BeZero())
	})
})
