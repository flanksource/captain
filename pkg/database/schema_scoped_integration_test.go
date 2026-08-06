package database

import (
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Schema-scoped Captain storage", func() {
	It("isolates identical session identities and usage aggregates between schemas", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_schema_scoped"})
		schemaNames := []string{"agent_tenant_a_context", "agent_tenant_b_context"}
		titles := []string{"Tenant A context", "Tenant B context"}
		inputTokens := []int{17, 29}
		sessionID := uuid.MustParse("00000000-0000-0000-0000-000000000501")
		promptRunID := uuid.MustParse("00000000-0000-0000-0000-000000001501")
		databases := make([]*DB, len(schemaNames))

		for index, schemaName := range schemaNames {
			db, err := Open(ctx, WithDSN(handle.DSN()), WithSchema(schemaName), WithMigrations())
			Expect(err).NotTo(HaveOccurred())
			databases[index] = db
			DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

			session, err := db.CreateOrGetSession(ctx, CreateSessionInput{
				ID: sessionID, ProviderSessionID: "shared-provider-session", Source: "codex",
				Provider: "openai", HostID: "schema-test", Title: titles[index],
			})
			Expect(err).NotTo(HaveOccurred())

			turn, created, err := db.CreateChatTurn(ctx, CreateChatTurnInput{
				SessionID: session.ID, ProviderTurnID: "shared-provider-turn",
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
				Event: api.Event{Usage: &api.Usage{InputTokens: inputTokens[index], OutputTokens: 3}},
			})).To(Succeed())

			costs, err := db.ListThreadCosts(ctx, session.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(costs).To(ConsistOf(SatisfyAll(
				HaveField("SessionID", sessionID),
				HaveField("InputTokens", int64(inputTokens[index])),
				HaveField("TotalTokens", int64(inputTokens[index]+3)),
			)))
		}

		for index, db := range databases {
			session, err := db.GetSession(ctx, sessionID)
			Expect(err).NotTo(HaveOccurred())
			Expect(session.Title).To(Equal(titles[index]))
		}

		usageSchemas := append([]string{}, schemaNames...)
		usageSchemas = append(usageSchemas, "agent_context_not_opened", schemaNames[0])
		usage, err := databases[0].ModelUsageSince(ctx, time.Now().Add(-time.Hour), usageSchemas...)
		Expect(err).NotTo(HaveOccurred())
		Expect(usage.TotalTokens).To(Equal(52))
		Expect(usage.TotalCostUSD).To(Equal(0.0))

		Expect(databases[0].Gorm().WithContext(ctx).Exec("CREATE SCHEMA agent_context_incomplete").Error).NotTo(HaveOccurred())
		_, err = databases[0].ModelUsageSince(ctx, time.Now().Add(-time.Hour), "agent_context_incomplete")
		Expect(err).To(MatchError(ContainSubstring("missing captain_model_calls")))
	})
})
