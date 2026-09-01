package aichat_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Database chat history recovery", func() {
	It("recovers an incomplete terminal chat without filling the message cache", func(ctx SpecContext) {
		db, store, thread, root := incompleteTerminalChat(ctx, incompleteTerminalChatOptions{
			DatabaseName: "captain_aichat_history_recovery", ProviderSessionID: "provider-recovery",
		})
		transcriptPath := filepath.Join("testdata", "recovery-claude.jsonl")
		_, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: "provider-recovery", Source: "claude", Provider: "anthropic", HostID: root.HostID,
			ParentSessionID: &root.ID, ParentRelation: database.SessionParentRelationTranscript, Path: transcriptPath,
		})
		Expect(err).NotTo(HaveOccurred())

		aggregate, err := store.GetSession(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(aggregate.Messages).To(HaveLen(2))
		Expect(aggregate.Messages[0].Role).To(Equal("user"))
		Expect(aggregate.Messages[1].Role).To(Equal("assistant"))
		Expect(aggregate.Messages[1].Parts).To(ContainElement(HaveField("Text", "Recovered from provider history")))
		cached, err := db.ListTranscriptMessages(ctx, database.TranscriptPage{SessionID: root.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(cached).To(HaveLen(1))
		sources, err := db.ListSessionSources(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(sources).To(HaveKey(transcriptPath))
		Expect(sources[transcriptPath].ParserVersion).To(Equal(session.TranscriptParserVersion))

		provider := &fakeStreamingProvider{events: []api.Event{
			{Kind: api.EventText, Text: "OK"},
			{Kind: api.EventResult, Success: true, Model: "test-model"},
		}}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider}, Threads: aichat.FixedThreadStore(store),
		})
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "gpt-5.6-sol", Mode: api.ModeAPI},
			Messages: []aichat.UIMessage{{
				ID: "safe-follow-up", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Reply with OK"}},
			}},
		}))

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(provider.specs).To(HaveLen(1))
		Expect(provider.specs[0].Messages).To(HaveLen(3))
		Expect(provider.specs[0].Messages[1].Role).To(Equal(api.RoleAssistant))
	})

	It("reports missing provider history as an HTTP conflict", func(ctx SpecContext) {
		_, store, thread, _ := incompleteTerminalChat(ctx, incompleteTerminalChatOptions{
			DatabaseName: "captain_aichat_history_unavailable", ProviderSessionID: "provider-history-missing",
		})

		_, err := store.GetSession(ctx, thread.ID)
		Expect(err).To(MatchError(MatchRegexp("provider history unavailable.*provider-history-missing")))
		Expect(errors.Is(err, aichat.ErrHistoryUnavailable)).To(BeTrue())
		expectHistoryConflict(store, thread.ID)
	})

	It("reports mismatched provider history as an HTTP conflict", func(ctx SpecContext) {
		db, store, thread, root := incompleteTerminalChat(ctx, incompleteTerminalChatOptions{
			DatabaseName: "captain_aichat_history_mismatch", ProviderSessionID: "provider-history-mismatch",
		})
		_, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: "provider-history-mismatch", Source: "claude", Provider: "anthropic", HostID: root.HostID,
			ParentSessionID: &root.ID, ParentRelation: database.SessionParentRelationTranscript,
			Path: filepath.Join("testdata", "recovery-claude.jsonl"),
		})
		Expect(err).NotTo(HaveOccurred())

		_, err = store.GetSession(ctx, thread.ID)
		Expect(err).To(MatchError(ContainSubstring("claude transcript identifies provider-recovery")))
		Expect(errors.Is(err, aichat.ErrHistoryUnavailable)).To(BeTrue())
		expectHistoryConflict(store, thread.ID)
	})
})

type incompleteTerminalChatOptions struct {
	DatabaseName      string
	ProviderSessionID string
}

func incompleteTerminalChat(ctx SpecContext, options incompleteTerminalChatOptions) (
	*database.DB,
	*aichat.DatabaseThreadStore,
	*aichat.Thread,
	*database.Session,
) {
	GinkgoHelper()
	testDB := dbtest.ForGinkgo(dbtest.Options{Name: options.DatabaseName})
	db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(db.Close)
	store, err := aichat.NewDatabaseThreadStore(db)
	Expect(err).NotTo(HaveOccurred())
	thread, err := store.Create(ctx, "Incomplete history")
	Expect(err).NotTo(HaveOccurred())
	root, err := db.GetSession(ctx, uuid.MustParse(thread.ID))
	Expect(err).NotTo(HaveOccurred())
	root, err = db.UpdateSessionState(ctx, database.UpdateSessionStateInput{
		ID: root.ID, ExpectedVersion: root.StateVersion, ProviderSessionID: &options.ProviderSessionID,
	})
	Expect(err).NotTo(HaveOccurred())
	turn, created, err := db.CreateChatTurn(ctx, database.CreateChatTurnInput{
		SessionID: root.ID, ProviderTurnID: "incomplete-turn",
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(created).To(BeTrue())
	parts, err := json.Marshal([]aichat.UIPart{{Type: "text", Text: "Recover this answer"}})
	Expect(err).NotTo(HaveOccurred())
	Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
		SessionID: root.ID, TurnID: turn.ID, ProviderMessageID: "incomplete-user", Role: "user", Parts: parts,
	})).To(Succeed())
	Expect(db.FinishChatTurn(ctx, turn.ID, database.TurnStatusEnded, "stop")).To(Succeed())
	lifecycle, activity := database.SessionLifecycleSucceeded, database.SessionActivityIdle
	root, err = db.UpdateSessionState(ctx, database.UpdateSessionStateInput{
		ID: root.ID, ExpectedVersion: root.StateVersion, LifecycleStatus: &lifecycle, ActivityState: &activity,
	})
	Expect(err).NotTo(HaveOccurred())
	return db, store, thread, root
}

func expectHistoryConflict(store *aichat.DatabaseThreadStore, threadID string) {
	GinkgoHelper()
	service := aichat.NewService(aichat.ServiceOptions{Threads: aichat.FixedThreadStore(store)})
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/sessions/"+threadID, nil))
	Expect(response.Code).To(Equal(http.StatusConflict))
}
