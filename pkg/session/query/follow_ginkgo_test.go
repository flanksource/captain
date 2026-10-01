package query

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	followWait  = 10 * time.Second
	followQuiet = 400 * time.Millisecond
)

var _ = Describe("Follow", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_follow"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	newSession := func(ctx context.Context) *database.Session {
		created, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: "follow-test",
		})
		Expect(err).NotTo(HaveOccurred())
		return created
	}
	putMessage := func(ctx context.Context, sessionID uuid.UUID, messageID, text string, replace bool) {
		parts, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
		Expect(err).NotTo(HaveOccurred())
		Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
			SessionID: sessionID, ProviderMessageID: messageID, Role: "assistant", Parts: parts, Replace: replace,
		})).To(Succeed())
	}
	setLifecycle := func(ctx context.Context, sessionID uuid.UUID, status database.SessionLifecycleStatus) {
		current, err := db.GetSession(ctx, sessionID)
		Expect(err).NotTo(HaveOccurred())
		_, err = db.UpdateSessionState(ctx, database.UpdateSessionStateInput{
			ID: sessionID, ExpectedVersion: current.StateVersion, LifecycleStatus: &status,
		})
		Expect(err).NotTo(HaveOccurred())
	}
	follow := func(ctx context.Context, id string, opts FollowOptions) <-chan FollowEvent {
		followCtx, cancel := context.WithCancel(ctx)
		events, err := Follow(followCtx, db, id, opts)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			cancel()
			Eventually(events, followWait).Should(BeClosed())
		})
		return events
	}

	It("replays stored messages, emits a new one once, and re-emits it when enriched", func(ctx SpecContext) {
		stored := newSession(ctx)
		putMessage(ctx, stored.ID, "m1", "stored before follow", false)

		events := follow(ctx, stored.ID.String(), FollowOptions{Replay: true})
		Expect(messageTexts(nextMessages(events, 1))).To(Equal([]string{"m1: stored before follow"}))

		putMessage(ctx, stored.ID, "m2", "ingested live", false)
		Expect(messageTexts(nextMessages(events, 1))).To(Equal([]string{"m2: ingested live"}))
		expectNoMessage(events)

		putMessage(ctx, stored.ID, "m2", "ingested live + tool output", true)
		Expect(messageTexts(nextMessages(events, 1))).To(Equal([]string{"m2: ingested live + tool output"}))
		expectNoMessage(events)
	})

	It("emits only changes without replay, a state frame per revision, and closes on terminal", func(ctx SpecContext) {
		followed := newSession(ctx)
		putMessage(ctx, followed.ID, "old", "already seen", false)

		events := follow(ctx, followed.ID.String(), FollowOptions{})
		Expect(withoutFacets(nextState(events))).To(Equal(FollowState{Revision: 0, LifecycleStatus: "created", ActivityState: "idle"}))

		setLifecycle(ctx, followed.ID, database.SessionLifecycleRunning)
		Expect(withoutFacets(nextState(events))).To(Equal(FollowState{Revision: 1, LifecycleStatus: "running", ActivityState: "idle"}))

		putMessage(ctx, followed.ID, "new", "after follow", false)
		Expect(messageTexts(nextMessages(events, 1))).To(Equal([]string{"new: after follow"}))

		setLifecycle(ctx, followed.ID, database.SessionLifecycleSucceeded)
		Expect(withoutFacets(nextState(events))).To(Equal(FollowState{Revision: 2, LifecycleStatus: "succeeded", ActivityState: "idle"}))
		Eventually(events, followWait).Should(BeClosed())
	})

	It("refuses an identity that names no session", func(ctx SpecContext) {
		_, err := Follow(ctx, db, uuid.NewString(), FollowOptions{Replay: true})
		Expect(err).To(MatchError(database.ErrSessionNotFound))
	})

	It("shares one LISTEN connection between followers and releases it after the last leaves", func(ctx SpecContext) {
		shared := newSession(ctx)
		firstCtx, cancelFirst := context.WithCancel(ctx)
		secondCtx, cancelSecond := context.WithCancel(ctx)
		first, err := Follow(firstCtx, db, shared.ID.String(), FollowOptions{})
		Expect(err).NotTo(HaveOccurred())
		second, err := Follow(secondCtx, db, shared.ID.String(), FollowOptions{})
		Expect(err).NotTo(HaveOccurred())
		nextState(first)
		nextState(second)

		Expect(listenerBackends(ctx, db)).To(Equal(1))
		putMessage(ctx, shared.ID, "fanout", "to both", false)
		Expect(messageTexts(nextMessages(first, 1))).To(Equal([]string{"fanout: to both"}))
		Expect(messageTexts(nextMessages(second, 1))).To(Equal([]string{"fanout: to both"}))

		cancelFirst()
		Eventually(first, followWait).Should(BeClosed())
		Expect(listenerBackends(ctx, db)).To(Equal(1))

		cancelSecond()
		Eventually(second, followWait).Should(BeClosed())
		Expect(listenerBackends(ctx, db)).To(Equal(0))
		hub, err := hubFor(db)
		Expect(err).NotTo(HaveOccurred())
		hub.mu.Lock()
		defer hub.mu.Unlock()
		Expect(hub.listener).To(BeNil())
		Expect(hub.subs).To(BeEmpty())
	})

	It("re-LISTENs after the listener backend is killed and catches up without losing a message", func(ctx SpecContext) {
		survivor := newSession(ctx)
		events := follow(ctx, survivor.ID.String(), FollowOptions{})
		nextState(events)
		Expect(listenerBackends(ctx, db)).To(Equal(1))

		var terminated bool
		Expect(db.Gorm().WithContext(ctx).Raw(`
			SELECT pg_terminate_backend(pid) FROM pg_stat_activity
			WHERE datname = current_database() AND query = 'LISTEN ` + SessionChangeChannel + `'`).
			Scan(&terminated).Error).To(Succeed())
		Expect(terminated).To(BeTrue())
		putMessage(ctx, survivor.ID, "while-down", "sent while the listener was gone", false)

		Expect(messageTexts(nextMessages(events, 1))).To(Equal([]string{"while-down: sent while the listener was gone"}))
		Eventually(func() int { return listenerBackends(ctx, db) }, followWait).Should(Equal(1))

		putMessage(ctx, survivor.ID, "after", "after re-LISTEN", false)
		Expect(messageTexts(nextMessages(events, 1))).To(Equal([]string{"after: after re-LISTEN"}))
	})
})

func listenerBackends(ctx context.Context, db *database.DB) int {
	GinkgoHelper()
	var count int
	Expect(db.Gorm().WithContext(ctx).Raw(`
		SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND query = 'LISTEN ` + SessionChangeChannel + `'`).
		Scan(&count).Error).To(Succeed())
	return count
}

// nextMessages returns the next n message events, skipping state frames and
// failing on an error event or a closed stream.
func nextMessages(events <-chan FollowEvent, n int) []FollowEvent {
	GinkgoHelper()
	var messages []FollowEvent
	for len(messages) < n {
		event := nextEvent(events)
		if event.Message != nil {
			messages = append(messages, event)
		}
	}
	return messages
}

func nextState(events <-chan FollowEvent) FollowState {
	GinkgoHelper()
	for {
		if event := nextEvent(events); event.State != nil {
			return *event.State
		}
	}
}

func nextEvent(events <-chan FollowEvent) FollowEvent {
	GinkgoHelper()
	select {
	case event, open := <-events:
		Expect(open).To(BeTrue(), "follow stream closed early")
		Expect(event.Err).NotTo(HaveOccurred())
		return event
	case <-time.After(followWait):
		Fail(fmt.Sprintf("no follow event within %s", followWait))
		return FollowEvent{}
	}
}

func expectNoMessage(events <-chan FollowEvent) {
	GinkgoHelper()
	deadline := time.After(followQuiet)
	for {
		select {
		case event, open := <-events:
			Expect(open).To(BeTrue(), "follow stream closed early")
			Expect(event.Err).NotTo(HaveOccurred())
			Expect(event.Message).To(BeNil(), "unexpected message %v", event.Message)
		case <-deadline:
			return
		}
	}
}

func messageTexts(events []FollowEvent) []string {
	texts := make([]string, 0, len(events))
	for _, event := range events {
		text := ""
		for _, part := range event.Message.Parts {
			text += part.Text
		}
		texts = append(texts, event.Message.ID+": "+text)
	}
	return texts
}
