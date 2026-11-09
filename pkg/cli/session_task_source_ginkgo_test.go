package cli

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	sessionquery "github.com/flanksource/captain/pkg/session/query"
	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("captain session source", func() {
	message := session.Message{ID: "m1", Role: "assistant", Parts: []session.Part{{Type: session.PartText, Text: "hello"}}}
	state := sessionquery.FollowState{Revision: 3, LifecycleStatus: "running", ActivityState: "working", Facets: "abc"}

	It("relays a message as the event's entry and a state as its state, as the JSON a session follow serves", func() {
		entry := sessionTaskEvent(sessionquery.FollowEvent{Message: &message})
		Expect(entry.Err).NotTo(HaveOccurred())
		Expect(entry.State).To(BeNil())
		Expect(entry.Entry).To(MatchJSON(`{"id":"m1","role":"assistant","parts":[{"type":"text","text":"hello"}]}`))

		changed := sessionTaskEvent(sessionquery.FollowEvent{State: &state})
		Expect(changed.Err).NotTo(HaveOccurred())
		Expect(changed.Entry).To(BeNil())
		Expect(changed.State).To(MatchJSON(`{"revision":3,"lifecycleStatus":"running","activityState":"working","facets":"abc"}`))
	})

	It("passes a follow error through and refuses an empty follow event", func() {
		Expect(sessionTaskEvent(sessionquery.FollowEvent{Err: errors.New("listen lost")}).Err).To(MatchError("listen lost"))
		Expect(sessionTaskEvent(sessionquery.FollowEvent{}).Err).To(MatchError(ContainSubstring("empty event")))
	})

	It("follows only what the session gains after the follow opens", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_task_source"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		followed, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: captainHostID(),
		})
		Expect(err).NotTo(HaveOccurred())
		putMessage := func(id, text string) {
			GinkgoHelper()
			parts, err := json.Marshal([]session.Part{{Type: session.PartText, Text: text}})
			Expect(err).NotTo(HaveOccurred())
			Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
				SessionID: followed.ID, ProviderMessageID: id, Role: "assistant", Parts: parts,
			})).To(Succeed())
		}
		putMessage("before-open", "already held")

		followCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		source := captainSessionSource{open: func(context.Context) (*database.DB, error) { return db, nil }}
		events, err := source.Follow(followCtx, task.SessionRef{Type: "captain", ID: followed.ID.String()})
		Expect(err).NotTo(HaveOccurred())

		var first task.SessionEvent
		Eventually(events).Should(Receive(&first))
		Expect(first.Entry).To(BeNil(), "a message the session held before the follow opened is never relayed")
		Expect(first.State).To(ContainSubstring(`"lifecycleStatus":"created"`))

		putMessage("after-open", "streamed live")
		var next task.SessionEvent
		Eventually(events, 10*time.Second).Should(Receive(&next))
		Expect(next.Err).NotTo(HaveOccurred())
		Expect(next.Entry).To(ContainSubstring(`"id":"after-open"`))
	})
})
