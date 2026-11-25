package query

import (
	"encoding/json"
	"path/filepath"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A monitored Claude session is its own transcript row: it has no transcript
// child, so recovery must read the row's own history rather than bind one.
var _ = Describe("Recovering a provider transcript row's missing answer", func() {
	It("reads the row's own transcript without binding a child to it", func(ctx SpecContext) {
		isolatedHistoryHome()
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_recovery_self"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		transcript, err := filepath.Abs(recoveryFixture)
		Expect(err).NotTo(HaveOccurred())
		row, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: recoveryFixtureID, Source: "claude", Provider: "anthropic", HostID: "recovery-self-test",
			Path: transcript,
		})
		Expect(err).NotTo(HaveOccurred())
		turn, _, err := db.CreateChatTurn(ctx, database.CreateChatTurnInput{SessionID: row.ID, ProviderTurnID: "incomplete-turn"})
		Expect(err).NotTo(HaveOccurred())
		parts, err := json.Marshal([]map[string]string{{"type": "text", "text": "Recover this answer"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
			SessionID: row.ID, TurnID: turn.ID, ProviderMessageID: "incomplete-user", Role: "user", Parts: parts,
		})).To(Succeed())
		Expect(db.FinishChatTurn(ctx, turn.ID, database.TurnStatusEnded, "stop")).To(Succeed())
		succeeded := database.SessionLifecycleSucceeded
		_, err = db.UpdateSessionState(ctx, database.UpdateSessionStateInput{
			ID: row.ID, ExpectedVersion: row.StateVersion, LifecycleStatus: &succeeded,
		})
		Expect(err).NotTo(HaveOccurred())
		overview, err := db.GetSessionOverviewByIdentity(ctx, row.ID.String())
		Expect(err).NotTo(HaveOccurred())
		loader, err := New(db)
		Expect(err).NotTo(HaveOccurred())

		aggregate, err := loader.StoredAggregate(ctx, *overview)

		Expect(err).NotTo(HaveOccurred())
		Expect(aggregate.Messages).To(ContainElement(SatisfyAll(
			HaveField("Role", "assistant"),
			HaveField("Parts", ContainElement(HaveField("Text", "Recovered from provider history"))),
		)))
		sources, err := db.ListSessionSources(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(sources).To(HaveKey(transcript))
		Expect(sources[transcript].SessionID).To(Equal(row.ID))
	})
})
