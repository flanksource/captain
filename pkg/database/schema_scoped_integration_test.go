package database

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func openSchemaScopedDatabases(ctx context.Context, databaseName string, schemaNames ...string) []*DB {
	handle := dbtest.ForGinkgo(dbtest.Options{Name: databaseName})
	databases := make([]*DB, len(schemaNames))
	for index, schemaName := range schemaNames {
		db, err := Open(ctx, WithDSN(handle.DSN()), WithSchema(schemaName), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		databases[index] = db
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	}
	return databases
}

var _ = Describe("Schema-scoped Captain storage", func() {
	It("isolates identical session identities and storage statistics between schemas", func(ctx SpecContext) {
		fixtures := []struct {
			schema                  string
			title                   string
			extraProviderSessionIDs []string
			expectedLiveRows        int64
		}{
			{schema: "agent_tenant_a_context", title: "Tenant A context", expectedLiveRows: 1},
			{
				schema: "agent_tenant_b_context", title: "Tenant B context",
				extraProviderSessionIDs: []string{"tenant-b-extra-session"}, expectedLiveRows: 2,
			},
		}
		schemaNames := []string{fixtures[0].schema, fixtures[1].schema}
		databases := openSchemaScopedDatabases(ctx, "captain_schema_storage", schemaNames...)
		sharedSessionID := uuid.MustParse("00000000-0000-0000-0000-000000000501")

		for index, fixture := range fixtures {
			db := databases[index]
			_, err := db.CreateOrGetSession(ctx, CreateSessionInput{
				ID: sharedSessionID, ProviderSessionID: "shared-provider-session", Source: "codex",
				Provider: "openai", HostID: "schema-test", Title: fixture.title,
			})
			Expect(err).NotTo(HaveOccurred())
			for _, providerSessionID := range fixture.extraProviderSessionIDs {
				_, err = db.CreateOrGetSession(ctx, CreateSessionInput{
					ProviderSessionID: providerSessionID, Source: "codex", Provider: "openai",
					HostID: "schema-test", Title: fixture.title + " extra",
				})
				Expect(err).NotTo(HaveOccurred())
			}

			session, err := db.GetSession(ctx, sharedSessionID)
			Expect(err).NotTo(HaveOccurred())
			Expect(session.Title).To(Equal(fixture.title))

			stats, err := db.SessionStorageStats(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(stats.LiveRows).To(Equal(fixture.expectedLiveRows))
		}
	})

	It("aggregates every token bucket and canonical USD costs across schemas", func(ctx SpecContext) {
		fixtures := []struct {
			schema              string
			usage               api.Usage
			cost                api.Cost
			providerCostUSD     float64
			expectedCostUSD     float64
			includeNonUSDCall   bool
			expectedSchemaTotal int
		}{
			{
				schema:          "agent_usage_a_context",
				usage:           api.Usage{InputTokens: 11, OutputTokens: 7, ReasoningTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 2},
				cost:            api.Cost{InputCost: 0.11, OutputCost: 0.07, ReasoningCost: 0.05, CacheReadCost: 0.03, CacheWriteCost: 0.02},
				providerCostUSD: 0.42, expectedCostUSD: 0.42, expectedSchemaTotal: 28,
			},
			{
				schema:          "agent_usage_b_context",
				usage:           api.Usage{InputTokens: 13, OutputTokens: 8, ReasoningTokens: 4, CacheReadTokens: 6, CacheWriteTokens: 1},
				cost:            api.Cost{InputCost: 0.13, OutputCost: 0.08, ReasoningCost: 0.04, CacheReadCost: 0.06, CacheWriteCost: 0.01},
				expectedCostUSD: 0.32, includeNonUSDCall: true, expectedSchemaTotal: 35,
			},
		}
		schemaNames := []string{fixtures[0].schema, fixtures[1].schema}
		databases := openSchemaScopedDatabases(ctx, "captain_schema_usage", schemaNames...)
		sessionID := uuid.MustParse("00000000-0000-0000-0000-000000000601")
		promptRunID := uuid.MustParse("00000000-0000-0000-0000-000000001601")
		since := time.Now().Add(-time.Hour)

		for index, fixture := range fixtures {
			db := databases[index]
			session, err := db.CreateOrGetSession(ctx, CreateSessionInput{
				ID: sessionID, ProviderSessionID: "shared-usage-session", Source: "codex",
				Provider: "openai", HostID: "schema-test", Title: fixture.schema,
			})
			Expect(err).NotTo(HaveOccurred())
			turn, created, err := db.CreateChatTurn(ctx, CreateChatTurnInput{
				SessionID: session.ID, ProviderTurnID: "shared-usage-turn",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(created).To(BeTrue())
			run, err := db.CreatePromptRun(ctx, CreatePromptRunInput{
				ID: promptRunID, SessionID: session.ID, TurnID: &turn.ID,
			})
			Expect(err).NotTo(HaveOccurred())

			callID, err := db.CreateChatModelCall(ctx, CreateChatModelCallInput{
				TurnID: turn.ID, PromptRunID: run.ID, Model: "model-schema-test", Backend: "codex",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(db.FinishChatModelCall(ctx, FinishChatModelCallInput{
				ID: callID, Status: ModelCallStatusSucceeded, StopReason: "end_turn",
				Event: api.Event{Usage: &fixture.usage, CostUSD: fixture.providerCostUSD}, Cost: &fixture.cost,
			})).To(Succeed())

			if fixture.includeNonUSDCall {
				nonUSDCallID, err := db.CreateChatModelCall(ctx, CreateChatModelCallInput{
					TurnID: turn.ID, PromptRunID: run.ID, Model: "model-schema-test", Backend: "codex",
				})
				Expect(err).NotTo(HaveOccurred())
				nonUSDUsage := api.Usage{InputTokens: 2, CacheReadTokens: 1}
				nonUSDCost := api.Cost{InputCost: 0.99}
				Expect(db.FinishChatModelCall(ctx, FinishChatModelCallInput{
					ID: nonUSDCallID, Status: ModelCallStatusSucceeded, StopReason: "end_turn",
					Event: api.Event{Usage: &nonUSDUsage}, Cost: &nonUSDCost,
				})).To(Succeed())
				Expect(db.Gorm().WithContext(ctx).Table("captain_model_calls").
					Where("id = ?", nonUSDCallID).Update("currency", "EUR").Error).NotTo(HaveOccurred())
			}

			usage, err := db.ModelUsageSince(ctx, since, fixture.schema)
			Expect(err).NotTo(HaveOccurred())
			Expect(usage.TotalTokens).To(Equal(fixture.expectedSchemaTotal))
			Expect(usage.TotalCostUSD).To(BeNumerically("~", fixture.expectedCostUSD, 0.000000001))
		}

		usageSchemas := append([]string{}, schemaNames...)
		usageSchemas = append(usageSchemas, "agent_context_not_opened", schemaNames[0])
		usage, err := databases[0].ModelUsageSince(ctx, since, usageSchemas...)
		Expect(err).NotTo(HaveOccurred())
		Expect(usage.TotalTokens).To(Equal(63))
		Expect(usage.TotalCostUSD).To(BeNumerically("~", 0.74, 0.000000001))

		Expect(databases[0].Gorm().WithContext(ctx).Exec("CREATE SCHEMA agent_context_incomplete").Error).NotTo(HaveOccurred())
		_, err = databases[0].ModelUsageSince(ctx, since, "agent_context_incomplete")
		Expect(err).To(MatchError(ContainSubstring("missing captain_model_calls")))
	})
})
