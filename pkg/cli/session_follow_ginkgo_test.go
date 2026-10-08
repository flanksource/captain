package cli

import (
	"context"
	"encoding/json"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("sessions get --follow", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_get_follow"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	putReply := func(ctx context.Context, sessionID uuid.UUID, id, text string) {
		parts, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
		Expect(err).NotTo(HaveOccurred())
		Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
			SessionID: sessionID, ProviderMessageID: id, Role: "assistant", Parts: parts,
		})).To(Succeed())
	}

	It("prints the transcript, then each message as it arrives, and returns when the session ends", func(ctx SpecContext) {
		followed, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: "follow-cli",
		})
		Expect(err).NotTo(HaveOccurred())
		putReply(ctx, followed.ID, "first", "the first reply")

		var output lockedBuffer
		done := make(chan error, 1)
		go func() {
			done <- followSessionGet(ctx, db, SessionGetOptions{ID: followed.ID.String(), Follow: true}, &output)
		}()
		Eventually(output.String, "10s").Should(ContainSubstring("the first reply"))

		putReply(ctx, followed.ID, "second", "a reply that arrived later")
		Eventually(output.String, "10s").Should(ContainSubstring("a reply that arrived later"))

		status := database.SessionLifecycleSucceeded
		_, err = db.UpdateSessionState(ctx, database.UpdateSessionStateInput{
			ID: followed.ID, ExpectedVersion: followed.StateVersion, LifecycleStatus: &status,
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(done, "10s").Should(Receive(BeNil()))
		Expect(output.String()).To(MatchRegexp(`-- session succeeded \(idle, revision 1, facets [0-9a-f]{16}\)\n`))
	})

	It("refuses transcript filters it cannot apply to a live stream", func(ctx SpecContext) {
		err := followSessionGet(ctx, db, SessionGetOptions{ID: uuid.NewString(), Follow: true, Tools: []string{"Bash"}}, &lockedBuffer{})
		Expect(err).To(MatchError(ContainSubstring("--follow")))
	})
})
