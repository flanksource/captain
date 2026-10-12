package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

// liveChatProviderStub is a provider a live chat can both interrupt and switch
// permission posture on, recording each call.
type liveChatProviderStub struct {
	interrupted bool
	modes       []api.PermissionMode
}

func (p *liveChatProviderStub) Execute(context.Context, api.Spec) (*api.Response, error) {
	return &api.Response{}, nil
}
func (p *liveChatProviderStub) GetModel() string { return "test-model" }
func (p *liveChatProviderStub) GetRuntime() api.Runtime {
	return api.RuntimeOf(api.Anthropic, api.ModeAgent)
}
func (p *liveChatProviderStub) Interrupt(context.Context) error {
	p.interrupted = true
	return nil
}
func (p *liveChatProviderStub) SetPermissionMode(_ context.Context, mode api.PermissionMode) error {
	p.modes = append(p.modes, mode)
	return nil
}

var _ = Describe("SessionHandler live chat controls", Ordered, func() {
	const liveRunID = "live-run"
	var (
		stored *database.Session
		server *httptest.Server
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_chat_handler"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		setCaptainDBForTest(db)
		DeferCleanup(func() {
			setCaptainDBForTest(nil)
			Expect(db.Close()).To(Succeed())
		})
		stored, err = db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: captainHostID(),
		})
		Expect(err).NotTo(HaveOccurred())

		mux := http.NewServeMux()
		mux.Handle("/api/captain/sessions/", http.StripPrefix("/api/captain/sessions", SessionHandler()))
		server = httptest.NewServer(mux)
		DeferCleanup(server.Close)
	})

	sessionURL := func(id, route string) string {
		return server.URL + "/api/captain/sessions/" + id + "/" + route
	}

	call := func(ctx context.Context, method, url, body string) (int, []byte) {
		GinkgoHelper()
		var response *http.Response
		if method == http.MethodGet {
			response = getSession(ctx, url, "application/json")
		} else {
			response = postSessionMessage(ctx, url, body)
		}
		defer response.Body.Close()
		text, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		return response.StatusCode, text
	}

	// attachLiveChat registers a running chat on the stored session's provider
	// session, as a resumed run does once its provider answers.
	attachLiveChat := func(provider *liveChatProviderStub) *chatSession {
		chat := newChatSession(liveRunID, claudeAgentRender(""), 0, newRunStream(), nil)
		chat.provider = provider
		chat.state.Status = "running"
		promptChats.register(chat)
		promptChats.bindSession(chat, stored.ProviderSessionID)
		DeferCleanup(func() {
			promptChats.mu.Lock()
			delete(promptChats.byRun, liveRunID)
			delete(promptChats.bySession, stored.ProviderSessionID)
			promptChats.mu.Unlock()
		})
		return chat
	}

	DescribeTable("answers a JSON error when the session has no live chat in this process",
		func(ctx SpecContext, method, route, body string, wantStatus int) {
			status, text := call(ctx, method, sessionURL(stored.ID.String(), route), body)

			Expect(status).To(Equal(wantStatus))
			var payload map[string]string
			Expect(json.Unmarshal(text, &payload)).To(Succeed(), string(text))
			Expect(payload).To(HaveKeyWithValue("error", ContainSubstring("no live chat")))
		},
		Entry("GET /{id}/chat", http.MethodGet, "chat", "", http.StatusNotFound),
		Entry("POST /{id}/interrupt", http.MethodPost, "interrupt", "", http.StatusConflict),
		Entry("POST /{id}/permission-mode", http.MethodPost, "permission-mode", `{"mode":"auto"}`, http.StatusConflict),
	)

	It("reports an unknown session id as a JSON 404", func(ctx SpecContext) {
		status, text := call(ctx, http.MethodGet, sessionURL(uuid.NewString(), "chat"), "")

		Expect(status).To(Equal(http.StatusNotFound))
		var payload map[string]string
		Expect(json.Unmarshal(text, &payload)).To(Succeed(), string(text))
		Expect(payload).To(HaveKeyWithValue("error", ContainSubstring("not found")))
	})

	It("answers a message to an unknown session id with a 404, not a server error", func(ctx SpecContext) {
		status, text := call(ctx, http.MethodPost, sessionURL(uuid.NewString(), "message"), `{"text":"continue"}`)

		Expect(status).To(Equal(http.StatusNotFound), string(text))
		Expect(string(text)).To(ContainSubstring("not found"))
	})

	DescribeTable("projects the live chat, resolved by captain or provider session id",
		func(ctx SpecContext, byProviderID bool) {
			attachLiveChat(&liveChatProviderStub{})
			id := stored.ID.String()
			if byProviderID {
				id = stored.ProviderSessionID
			}

			status, text := call(ctx, http.MethodGet, sessionURL(id, "chat"), "")

			Expect(status).To(Equal(http.StatusOK), string(text))
			var body SessionChat
			Expect(json.Unmarshal(text, &body)).To(Succeed())
			Expect(body).To(MatchAllFields(Fields{
				"RunID":           Equal(liveRunID),
				"Capabilities":    MatchFields(IgnoreExtras, Fields{"Interrupt": BeTrue(), "SetPermissionMode": BeTrue()}),
				"State":           MatchFields(IgnoreExtras, Fields{"RunID": Equal(liveRunID), "Status": Equal("running")}),
				"PermissionMode":  Equal(api.PermissionDefault),
				"PermissionModes": Equal(claudeAgentModes),
			}))
		},
		Entry("by captain session id", false),
		Entry("by provider session id", true),
	)

	It("answers a live chat by its provider session id before consulting the database", func(ctx SpecContext) {
		unrecorded := uuid.NewString()
		chat := newChatSession("unrecorded-run", claudeAgentRender(""), 0, newRunStream(), nil)
		promptChats.register(chat)
		promptChats.bindSession(chat, unrecorded)
		DeferCleanup(func() {
			promptChats.mu.Lock()
			delete(promptChats.byRun, "unrecorded-run")
			delete(promptChats.bySession, unrecorded)
			promptChats.mu.Unlock()
		})

		status, text := call(ctx, http.MethodGet, sessionURL(unrecorded, "chat"), "")

		Expect(status).To(Equal(http.StatusOK), string(text))
		Expect(string(text)).To(ContainSubstring(`"runId":"unrecorded-run"`))
	})

	It("resolves a captain id to its live chat without loading the session's messages", func(ctx SpecContext) {
		db, err := captainDB(ctx)
		Expect(err).NotTo(HaveOccurred())
		unreadable, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: captainHostID(),
		})
		Expect(err).NotTo(HaveOccurred())
		// Parts that are not a part list make loading the transcript fail loudly.
		Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
			SessionID: unreadable.ID, ProviderMessageID: "undecodable", Role: "assistant",
			Parts: json.RawMessage(`{"type":"text"}`),
		})).To(Succeed())
		_, readErr := RunSessionGet(ctx, SessionGetOptions{ID: unreadable.ID.String()})
		Expect(readErr).To(HaveOccurred(), "the full session read must fail on this session's messages")
		chat := newChatSession("unreadable-run", claudeAgentRender(""), 0, newRunStream(), nil)
		promptChats.register(chat)
		promptChats.bindSession(chat, unreadable.ProviderSessionID)
		DeferCleanup(func() {
			promptChats.mu.Lock()
			delete(promptChats.byRun, "unreadable-run")
			delete(promptChats.bySession, unreadable.ProviderSessionID)
			promptChats.mu.Unlock()
		})

		status, text := call(ctx, http.MethodGet, sessionURL(unreadable.ID.String(), "chat"), "")

		Expect(status).To(Equal(http.StatusOK), string(text))
		Expect(string(text)).To(ContainSubstring(`"runId":"unreadable-run"`))
	})

	// createSubagentThread records a root session with one subagent row, as the
	// monitor ingests a Claude session that spawned a Task agent: the subagent
	// has its own provider id and names the root as parent and root.
	createSubagentThread := func(ctx context.Context) *database.Session {
		GinkgoHelper()
		db, err := captainDB(ctx)
		Expect(err).NotTo(HaveOccurred())
		root, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: captainHostID(),
		})
		Expect(err).NotTo(HaveOccurred())
		// Claude agent ids are 17 hex characters, e.g. a1a5443b373b4e2d7.
		agentID := "a" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
		_, err = db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: agentID, Source: "claude", Provider: "anthropic", HostID: captainHostID(),
			ParentSessionID: &root.ID, RootSessionID: &root.ID, ParentRelation: database.SessionParentRelationAgent,
		})
		Expect(err).NotTo(HaveOccurred())
		result, err := RunSessionGet(ctx, SessionGetOptions{ID: root.ID.String()})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Sessions).To(HaveLen(2), "a root id resolves to its whole thread")
		return root
	}

	It("answers a root session with subagents as having no live chat, not as ambiguous", func(ctx SpecContext) {
		root := createSubagentThread(ctx)

		status, text := call(ctx, http.MethodGet, sessionURL(root.ID.String(), "chat"), "")

		Expect(status).To(Equal(http.StatusNotFound), string(text))
		Expect(string(text)).To(ContainSubstring("no live chat"))
	})

	It("finds the live chat of a root session with subagents by its captain id", func(ctx SpecContext) {
		root := createSubagentThread(ctx)
		chat := newChatSession("thread-run", claudeAgentRender(""), 0, newRunStream(), nil)
		promptChats.register(chat)
		promptChats.bindSession(chat, root.ProviderSessionID)
		DeferCleanup(func() {
			promptChats.mu.Lock()
			delete(promptChats.byRun, "thread-run")
			delete(promptChats.bySession, root.ProviderSessionID)
			promptChats.mu.Unlock()
		})

		status, text := call(ctx, http.MethodGet, sessionURL(root.ID.String(), "chat"), "")

		Expect(status).To(Equal(http.StatusOK), string(text))
		Expect(string(text)).To(ContainSubstring(`"runId":"thread-run"`))
	})

	It("resumes a root session with subagents by its captain id instead of calling it ambiguous", func(ctx SpecContext) {
		root := createSubagentThread(ctx)

		// Empty text is refused by resume validation, which only runs once the
		// root has been picked as the session to continue.
		status, text := call(ctx, http.MethodPost, sessionURL(root.ID.String(), "message"), `{"text":"  "}`)

		Expect(status).To(Equal(http.StatusBadRequest), string(text))
		Expect(string(text)).To(ContainSubstring("message text is required"))
	})

	It("interrupts the live chat's running turn", func(ctx SpecContext) {
		provider := &liveChatProviderStub{}
		chat := attachLiveChat(provider)

		status, text := call(ctx, http.MethodPost, sessionURL(stored.ID.String(), "interrupt"), "")

		Expect(status).To(Equal(http.StatusAccepted), string(text))
		Expect(string(text)).To(MatchJSON(`{"status":"interrupting"}`))
		Expect(provider.interrupted).To(BeTrue())
		Expect(chat.state.Status).To(Equal("interrupting"))
	})

	It("switches the live chat's permission mode", func(ctx SpecContext) {
		provider := &liveChatProviderStub{}
		chat := attachLiveChat(provider)

		status, text := call(ctx, http.MethodPost, sessionURL(stored.ID.String(), "permission-mode"), `{"mode":"auto"}`)

		Expect(status).To(Equal(http.StatusAccepted), string(text))
		Expect(string(text)).To(MatchJSON(`{"runId":"live-run","permissionMode":"auto"}`))
		Expect(provider.modes).To(Equal([]api.PermissionMode{api.PermissionAuto}))
		Expect(chat.currentPermissionMode()).To(Equal(api.PermissionAuto))
	})

	It("refuses an unrecognised permission mode as a JSON 400 without reaching the provider", func(ctx SpecContext) {
		provider := &liveChatProviderStub{}
		attachLiveChat(provider)

		status, text := call(ctx, http.MethodPost, sessionURL(stored.ID.String(), "permission-mode"), `{"mode":"yolo"}`)

		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(string(text)).To(MatchJSON(`{"error":"invalid permission mode \"yolo\""}`))
		Expect(provider.modes).To(BeEmpty())
	})
})

var _ = Describe("isThreadExpansion", func() {
	const root, other = "8a25dc85-29a0-402a-bd9e-f55377d2c8ad", "f26fb1d0-f4e6-480c-9359-e4909b020cf0"

	DescribeTable("tells a root's thread apart from an ambiguous id",
		func(id string, rows []sessionRowIdentity, want bool) {
			Expect(isThreadExpansion(id, rows)).To(Equal(want))
		},
		Entry("a root's captain id with its subagent", root,
			[]sessionRowIdentity{{ID: root}, {ID: other, RootID: root}}, true),
		Entry("the root's id in upper case", strings.ToUpper(root),
			[]sessionRowIdentity{{ID: root}, {ID: other, RootID: root}}, true),
		Entry("a single row", root, []sessionRowIdentity{{ID: root}}, false),
		Entry("a provider-id prefix matching two unrelated sessions", "4e34",
			[]sessionRowIdentity{{ID: root}, {ID: other}}, false),
		Entry("a captain id whose first row is not the root it names", root,
			[]sessionRowIdentity{{ID: other}, {ID: root}}, false),
		Entry("a row outside the root's thread", root,
			[]sessionRowIdentity{{ID: root}, {ID: other, RootID: other}}, false),
	)
})
