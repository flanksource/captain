package query

import (
	"context"
	"encoding/json"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A session read taken before the agent writes its first transcript line
// holds only the prompt run's placeholder messages. A reader that resumes
// following after one of them must connect, and receive the real transcript.
var _ = Describe("Follow after a prompt-run placeholder", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_follow_placeholder"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	startRun := func(ctx context.Context) (*database.Session, *database.PromptRun) {
		created, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: "follow-placeholder-test",
		})
		Expect(err).NotTo(HaveOccurred())
		run, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{SessionID: created.ID})
		Expect(err).NotTo(HaveOccurred())
		return created, run
	}
	putMessage := func(ctx context.Context, sessionID uuid.UUID, messageID, text string) {
		parts, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
		Expect(err).NotTo(HaveOccurred())
		Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
			SessionID: sessionID, ProviderMessageID: messageID, Role: "user", Parts: parts,
		})).To(Succeed())
	}
	follow := func(ctx context.Context, id, after string) <-chan FollowEvent {
		followCtx, cancel := context.WithCancel(ctx)
		events, err := Follow(followCtx, db, id, FollowOptions{Replay: true, After: after})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			cancel()
			Eventually(events, followWait).Should(BeClosed())
		})
		return events
	}

	It("connects while the transcript is still empty and emits its first message", func(ctx SpecContext) {
		started, run := startRun(ctx)

		events := follow(ctx, started.ID.String(), run.ID.String()+"-user")
		putMessage(ctx, started.ID, "first", "the prompt as the agent saw it")

		Expect(messageTexts(nextMessages(events, 1))).To(Equal([]string{"first: the prompt as the agent saw it"}))
	})

	It("replays the whole transcript once it has replaced the placeholders", func(ctx SpecContext) {
		started, run := startRun(ctx)
		putMessage(ctx, started.ID, "first", "written between the read and the follow")
		putMessage(ctx, started.ID, "second", "also written meanwhile")

		events := follow(ctx, started.ID.String(), run.ID.String()+"-assistant")

		Expect(messageTexts(nextMessages(events, 2))).To(Equal([]string{
			"first: written between the read and the follow",
			"second: also written meanwhile",
		}))
	})

	It("still refuses a cursor that names neither a message nor a placeholder of this session", func(ctx SpecContext) {
		started, _ := startRun(ctx)
		_, other := startRun(ctx)

		_, err := Follow(ctx, db, started.ID.String(), FollowOptions{Replay: true, After: other.ID.String() + "-user"})

		Expect(err).To(MatchError(database.ErrInvalidSession))
	})
})
