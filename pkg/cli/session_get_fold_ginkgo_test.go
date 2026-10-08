package cli

import (
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

// A Gavel run as Captain stores it: a TODO root, the run row the launcher
// booked, and the claude row its transcript was ingested into.
var _ = Describe("session get over a launcher run and its transcript", func() {
	const providerID = "6c8440dd-5fad-43c5-b8f8-8047940ca5e5"
	var (
		todoID, runID, transcriptID uuid.UUID
		todo, run, transcript       database.SessionOverview
		worktree                    string
	)

	BeforeEach(func() {
		todoID = uuid.MustParse("5d3f5670-fc35-4257-936a-193b2ec0cb7d")
		runID = uuid.MustParse("010d861b-ea19-5d5a-8303-a01072803dd2")
		transcriptID = uuid.MustParse("613e86fd-9a21-4d1b-9cd0-c60e7485d0e5")
		worktree = "/Users/dev/oipa-cli/.shell/worktrees/shell-d5de"
		provider, title, repoRoot := providerID, "PermissionUsersTab - UI updates · run", "/Users/dev/oipa-cli"
		model, version, path := "claude-opus-5", "2.1.210", "/Users/dev/.claude/projects/worktree/"+providerID+".jsonl"
		todo = database.SessionOverview{ID: todoID, Source: "gavel", Provider: "todos"}
		run = database.SessionOverview{
			ID: runID, Source: "gavel", Provider: "agent-claude", ProviderSessionID: &provider,
			ParentSessionID: &todoID, RootSessionID: &todoID, ParentRelation: database.SessionParentRelationAgent,
			Title: &title, CWD: &repoRoot, Model: &model,
		}
		transcript = database.SessionOverview{
			ID: transcriptID, Source: "claude", Provider: "anthropic", ProviderSessionID: &provider,
			ParentSessionID: &runID, RootSessionID: &todoID, ParentRelation: database.SessionParentRelationTranscript,
			CWD: &worktree, Model: &model, CLIVersion: &version, HistoryFile: &path,
			ToolCallCount: 11, MessageCount: 0, TotalTokens: 1200, InputTokens: 1000, OutputTokens: 200, CostUSD: 0.87692925,
		}
	})

	expectFoldedRun := func(item SessionGetItem) {
		GinkgoHelper()
		Expect(item.CaptainID).To(Equal(runID.String()))
		Expect(item.Summary.Source).To(Equal("gavel"))
		Expect(item.Summary.Title).To(Equal("PermissionUsersTab - UI updates · run"))
		Expect(item.Summary.ToolCalls).To(Equal(11))
		Expect(item.Summary.CostUSD).To(Equal(0.87692925))
		Expect(item.Summary.Tokens).To(PointTo(Equal(SessionTokensWire{InputTokens: 1000, OutputTokens: 200, TotalTokens: 1200})))
		Expect(item.Summary.CWD).To(Equal(worktree))
		Expect(item.Summary.Version).To(Equal("2.1.210"))
		Expect(item.Execution).To(PointTo(Equal(SessionExecution{
			CaptainID: transcriptID.String(), Source: "claude", CWD: worktree, Model: "claude-opus-5",
		})))
		Expect(item.Chat).To(PointTo(HaveField("Resume", BeTrue())))
		Expect(item.PermissionModes).NotTo(BeEmpty())
	}

	It("shows one session when a provider id names both rows", func(ctx SpecContext) {
		store := &sessionGetOverviewStore{
			identity: []database.SessionOverview{transcript, run},
			children: []database.SessionOverview{transcript},
		}

		result, err := runSessionGet(ctx, store, SessionGetOptions{ID: providerID})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Total).To(Equal(1))
		Expect(result.Sessions).To(HaveLen(1))
		expectFoldedRun(result.Sessions[0])
	})

	It("shows the launcher session when the transcript row's own UUID is requested", func(ctx SpecContext) {
		store := &sessionGetOverviewStore{
			identity:   []database.SessionOverview{transcript},
			byIdentity: map[string][]database.SessionOverview{runID.String(): {run}},
			children:   []database.SessionOverview{transcript},
		}

		result, err := runSessionGet(ctx, store, SessionGetOptions{ID: transcriptID.String()})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Sessions).To(HaveLen(1))
		expectFoldedRun(result.Sessions[0])
	})

	It("enriches the launcher session requested by its own UUID", func(ctx SpecContext) {
		store := &sessionGetOverviewStore{
			identity: []database.SessionOverview{run},
			children: []database.SessionOverview{transcript},
		}

		result, err := runSessionGet(ctx, store, SessionGetOptions{ID: runID.String()})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Sessions).To(HaveLen(1))
		expectFoldedRun(result.Sessions[0])
	})

	It("lists each run of a TODO thread once, enriched from its transcript", func(ctx SpecContext) {
		store := &sessionGetOverviewStore{
			identity: []database.SessionOverview{todo},
			thread:   []database.SessionOverview{todo, run},
			children: []database.SessionOverview{transcript},
		}

		result, err := runSessionGet(ctx, store, SessionGetOptions{ID: todoID.String()})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.RootSessionID).To(Equal(todoID.String()))
		Expect(result.Sessions).To(HaveLen(2))
		Expect(result.Sessions[0].CaptainID).To(Equal(todoID.String()))
		Expect(result.Sessions[0].Execution).To(BeNil())
		expectFoldedRun(result.Sessions[1])
	})

	Describe("when the run ended by asking", func() {
		var store *sessionGetOverviewStore

		BeforeEach(func() {
			store = &sessionGetOverviewStore{
				identity: []database.SessionOverview{transcript, run},
				children: []database.SessionOverview{transcript},
				promptRuns: map[uuid.UUID][]database.PromptRun{runID: {{
					ID: uuid.MustParse("7a1c2f7e-0000-4000-8000-000000000001"), SessionID: runID, State: "succeeded",
					PromptMarkdown: "Implement the todo",
					ResultJSON: map[string]any{"summary": "Blocked on scope.", "endStatus": "ask", "questions": []any{
						map[string]any{"text": "Which work should I implement?", "options": []any{"The plan", "The todo"}},
					}},
				}}},
			}
		})

		It("reports the questions on the one folded session", func(ctx SpecContext) {
			result, err := runSessionGet(ctx, store, SessionGetOptions{ID: providerID})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Sessions).To(HaveLen(1))
			Expect(result.Sessions[0].Detail.AwaitingInput).To(PointTo(MatchFields(IgnoreExtras, Fields{
				"Summary":   Equal("Blocked on scope."),
				"Questions": HaveLen(1),
			})))
		})

		registerChat := func(chat *chatSession, sessionID string) {
			promptChats.register(chat)
			promptChats.bindSession(chat, sessionID)
			DeferCleanup(func() {
				promptChats.mu.Lock()
				delete(promptChats.byRun, chat.runID)
				delete(promptChats.bySession, sessionID)
				promptChats.mu.Unlock()
			})
		}

		It("reports none while a resumed chat is still running on the provider session", func(ctx SpecContext) {
			registerChat(newChatSession("resume-run", claudeAgentRender(""), 0, newRunStream(), nil), providerID)

			result, err := runSessionGet(ctx, store, SessionGetOptions{ID: providerID})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Sessions[0].ActiveRunID).To(Equal("resume-run"))
			Expect(result.Sessions[0].Detail.AwaitingInput).To(BeNil())
		})

		It("still reports them after the chat that ran as the session has finished", func(ctx SpecContext) {
			chat := newChatSession(runID.String(), claudeAgentRender(""), 0, newRunStream(), nil)
			chat.terminal = true
			registerChat(chat, "unrelated-provider-session")

			result, err := runSessionGet(ctx, store, SessionGetOptions{ID: providerID})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Sessions[0].Detail.AwaitingInput).NotTo(BeNil())
		})

		It("reports none while the agent's process is still running", func(ctx SpecContext) {
			status := "running"
			for _, row := range []*database.SessionOverview{&store.children[0], &store.identity[0]} {
				row.ProcessActive, row.ProcessStatus = true, &status
			}

			result, err := runSessionGet(ctx, store, SessionGetOptions{ID: providerID})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Sessions[0].Summary.Live).To(PointTo(HaveField("Active", BeTrue())))
			Expect(result.Sessions[0].Detail.AwaitingInput).To(BeNil())
		})
	})

	It("leaves a session without a transcript row unchanged", func(ctx SpecContext) {
		transcript.ParentSessionID, transcript.RootSessionID, transcript.ParentRelation = nil, nil, ""
		store := &sessionGetOverviewStore{identity: []database.SessionOverview{transcript}}

		result, err := runSessionGet(ctx, store, SessionGetOptions{ID: transcriptID.String()})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Sessions).To(HaveLen(1))
		Expect(result.Sessions[0].CaptainID).To(Equal(transcriptID.String()))
		Expect(result.Sessions[0].Execution).To(BeNil())
		Expect(result.Sessions[0].Chat).To(PointTo(HaveField("Resume", BeTrue())))
	})
})
