package aichat_test

import (
	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Database terminal execution", func() {
	It("rolls back terminal state when the assistant message cannot be committed", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_terminal_atomicity"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		execution, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: uuid.NewString(), RequestID: "terminal-atomicity", Title: "Atomic terminal",
			Spec: api.Spec{Model: withCaps(api.Model{Name: "sonnet", Mode: api.ModeAgent})},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(execution.Close)
		committer, ok := execution.(aichat.TerminalExecution)
		Expect(ok).To(BeTrue())

		Expect(db.Gorm().WithContext(ctx).Exec(`
			ALTER TABLE captain_messages
			ADD CONSTRAINT reject_test_assistant_message CHECK (role <> 'assistant')
		`).Error).To(Succeed())
		err = committer.CommitTerminal(ctx, aichat.TerminalCommit{
			Event: api.Event{Kind: api.EventResult, Success: true, SessionID: "provider-terminal-atomicity"},
			Message: aichat.UIMessage{
				ID: execution.TurnID() + "-assistant", TurnID: execution.TurnID(), Role: "assistant",
				Parts: []aichat.UIPart{{Type: "text", Text: "Committed response"}},
			},
		})
		Expect(err).To(HaveOccurred())
		Expect(execution.Close(ctx)).To(Succeed())

		run, err := db.GetPromptRun(ctx, uuid.MustParse(execution.PromptRunID()))
		Expect(err).NotTo(HaveOccurred())
		Expect(run.State).To(Equal(database.PromptRunStateRunning))
		turn, err := db.GetChatTurn(ctx, uuid.MustParse(execution.TurnID()))
		Expect(err).NotTo(HaveOccurred())
		Expect(turn.Status).To(Equal(database.TurnStatusOpen))
		sessionRecord, err := db.GetSession(ctx, uuid.MustParse(execution.CaptainSessionID()))
		Expect(err).NotTo(HaveOccurred())
		Expect(sessionRecord.LifecycleStatus).To(Equal(database.SessionLifecycleRunning))
		messages, err := db.ListTranscriptMessages(ctx, database.TranscriptPage{SessionID: sessionRecord.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(messages).To(BeEmpty())
	})
})
