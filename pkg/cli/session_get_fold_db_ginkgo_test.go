package cli

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("session get over a stored launcher run and its transcript", func() {
	It("shows what the transcript row stored when its log file cannot be read", func() {
		ctx := GinkgoT().Context()
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_fold_stored"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			setCaptainDBForTest(nil)
			Expect(db.Close()).To(Succeed())
		})
		setCaptainDBForTest(db)

		providerID := "0195c1de-4ab8-7000-8000-00000000f01d"
		run, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerID, Source: "gavel", Provider: "agent-claude", HostID: captainHostID(), CWD: "/work",
		})
		Expect(err).NotTo(HaveOccurred())
		transcript, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerID, Source: "claude", Provider: "anthropic", HostID: captainHostID(), CWD: "/work",
			ParentSessionID: &run.ID, ParentRelation: database.SessionParentRelationTranscript,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
			SessionID: transcript.ID, ProviderMessageID: "reply-1", Role: "assistant",
			Parts: json.RawMessage(`[{"type":"text","text":"Stored on the transcript row"}]`),
		})).To(Succeed())

		result, err := RunSessionGet(ctx, SessionGetOptions{ID: providerID})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Sessions).To(HaveLen(1))
		Expect(result.Sessions[0].CaptainID).To(Equal(run.ID.String()))
		Expect(result.Sessions[0].Detail).NotTo(BeNil())
		var texts []string
		for _, message := range result.Sessions[0].Detail.Messages {
			for _, part := range message.Parts {
				texts = append(texts, part.Text)
			}
		}
		Expect(texts).To(ContainElement("Stored on the transcript row"))
	})
})
