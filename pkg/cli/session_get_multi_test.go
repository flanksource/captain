package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/clicky"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

func TestSessionGetMulti(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Session Get Multi Suite")
}

var _ = Describe("session get multi-result output", func() {
	It("threads an opaque cursor through the live-session HTTP collection", func() {
		var received SessionLiveOptions
		handler := handleSessionsLiveWithRunner(func(_ context.Context, opts SessionLiveOptions) (SessionLiveResult, error) {
			received = opts
			return SessionLiveResult{Total: 3, NextCursor: "cursor-next"}, nil
		})
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/captain/sessions/live?source=codex&all=true&limit=2&cursor=cursor-current&from=2026-07-26T21%3A00%3A00Z&before=2026-07-28T21%3A00%3A00Z",
			nil,
		)
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(received).To(MatchFields(IgnoreExtras, Fields{
			"Source": Equal("codex"), "All": BeTrue(), "Limit": Equal(2), "Cursor": Equal("cursor-current"),
			"From":   Equal(time.Date(2026, time.July, 26, 21, 0, 0, 0, time.UTC)),
			"Before": Equal(time.Date(2026, time.July, 28, 21, 0, 0, 0, time.UTC)),
		}))
		var result SessionLiveResult
		Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
		Expect(result.Total).To(Equal(3))
		Expect(result.NextCursor).To(Equal("cursor-next"))
	})

	DescribeTable("rejects an invalid live-session activity timestamp before running the query", func(value string) {
		called := false
		handler := handleSessionsLiveWithRunner(func(_ context.Context, _ SessionLiveOptions) (SessionLiveResult, error) {
			called = true
			return SessionLiveResult{}, nil
		})
		request := httptest.NewRequest(http.MethodGet, "/api/captain/sessions/live?from="+value, nil)
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(called).To(BeFalse())
		Expect(response.Body.String()).To(ContainSubstring(fmt.Sprintf(`invalid from timestamp %q`, value)))
	},
		Entry("plain text", "not-a-time"),
		Entry("unknown datemath unit", "now-7q"),
	)

	It("resolves live-session datemath bounds from one reference time", func() {
		var received SessionLiveOptions
		handler := handleSessionsLiveWithRunner(func(_ context.Context, opts SessionLiveOptions) (SessionLiveResult, error) {
			received = opts
			return SessionLiveResult{}, nil
		})
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/captain/sessions/live?from=now-7d&before=now",
			nil,
		)
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(received.From).To(Equal(received.Before.AddDate(0, 0, -7)))
	})

	It("projects overview runtime data into the unified session detail", func() {
		detail := &session.Session{Turns: []session.Turn{{ID: "turn-1", Index: 1}}}
		summary := SessionRecord{
			Provider:        "openai",
			ModelMode:       "cmux",
			ReasoningEffort: "high",
			Live: &SessionLiveWire{
				PID: 4821, Status: "running", Active: true, CWD: "/repo", Command: "codex",
			},
		}

		enrichSessionDetail(detail, summary)

		Expect(detail.Provider).To(Equal("openai"))
		Expect(detail.ModelMode).To(Equal(api.ModeCmux))
		Expect(detail.ExecutionMode).To(Equal(api.ModeCmux))
		Expect(detail.ReasoningEffort).To(Equal("high"))
		Expect(detail.Live).To(Equal(&session.LiveProcess{
			PID: 4821, Status: "running", Active: true, CWD: "/repo", Command: "codex",
		}))
		// A turn carries both halves of its runtime. The mode alone no longer
		// names the family that ran it, so the projection fills the provider
		// beside it rather than leaving the turn ambiguous.
		Expect(detail.Turns).To(Equal([]session.Turn{{
			ID: "turn-1", Index: 1, Mode: "cmux", ModelProvider: "openai", ReasoningEffort: "high",
		}}))
	})

	DescribeTable("projects each runtime mode into session detail",
		func(mode api.RuntimeMode) {
			detail := &session.Session{}

			enrichSessionDetail(detail, SessionRecord{ModelMode: string(mode)})

			Expect(detail.ExecutionMode).To(Equal(mode))
		},
		Entry("api", api.ModeAPI),
		Entry("cli", api.ModeCLI),
		Entry("agent", api.ModeAgent),
		Entry("cmux", api.ModeCmux),
	)

	It("passes UUID-prefix searches through the bounded list filter", func() {
		providerID := "ad4c854e-cde6-4b99-99f3-667bf74112e3"
		store := &sessionGetOverviewStore{
			list: []database.SessionListSummary{
				{ID: uuid.New(), ProviderSessionID: &providerID, Source: "claude"},
				{ID: uuid.New(), ProviderSessionID: &providerID, Source: "gavel"},
			},
		}

		from := time.Date(2026, time.July, 26, 21, 0, 0, 0, time.UTC)
		before := from.Add(48 * time.Hour)
		page, err := dbSessionRecords(context.Background(), store, sessionRecordQuery{
			Source: "all", Query: "ad4c854e", ActivityFrom: &from, ActivityBefore: &before,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Records).To(HaveLen(2))
		Expect(store.listFilter).To(MatchFields(IgnoreExtras, Fields{
			"Query":          Equal("ad4c854e"),
			"RootsOnly":      BeTrue(),
			"ActivityFrom":   PointTo(Equal(from)),
			"ActivityBefore": PointTo(Equal(before)),
		}))
	})

	It("requires an activity range to increase", func() {
		from := time.Date(2026, time.July, 28, 21, 0, 0, 0, time.UTC)
		before := from.Add(-time.Second)

		activityFrom, activityBefore, err := sessionActivityRange(from, before)

		Expect(err).To(MatchError("session activity from must be earlier than before"))
		Expect(activityFrom).To(BeNil())
		Expect(activityBefore).To(BeNil())
	})

	It("loads every bounded page when full live output is explicit", func(ctx SpecContext) {
		store := &sessionGetOverviewStore{listPages: map[string]database.SessionListPage{
			"": {
				Rows:  []database.SessionListSummary{{ID: uuid.New()}, {ID: uuid.New()}},
				Total: 3, NextCursor: "cursor-2",
			},
			"cursor-2": {Rows: []database.SessionListSummary{{ID: uuid.New()}}, Total: 3},
		}}

		page, err := dbAllSessionRecords(ctx, store, sessionRecordQuery{Limit: 2})

		Expect(err).NotTo(HaveOccurred())
		Expect(page.Records).To(HaveLen(3))
		Expect(page.Total).To(Equal(3))
		Expect(store.listFilters).To(HaveLen(2))
		Expect(store.listFilters[0].Limit).To(Equal(2))
		Expect(store.listFilters[0].Cursor).To(BeEmpty())
		Expect(store.listFilters[1].Limit).To(Equal(2))
		Expect(store.listFilters[1].Cursor).To(Equal("cursor-2"))
	})

	It("keeps live summaries and database coverage scoped to the returned page", func() {
		free := 30
		page := sessionRecordPage{
			Records: []SessionRecord{
				{Tokens: &SessionTokensWire{InputTokens: 10, TotalTokens: 10}, Context: &SessionContextWire{FreePercent: free}, CostUSD: 0.25},
				{Tokens: &SessionTokensWire{OutputTokens: 5, TotalTokens: 5}},
			},
			Total: 9,
		}

		result := buildSessionLiveResult(sessionLiveResultOptions{
			Page: page, Source: "all", Scope: "all", ReadAt: time.Date(2026, time.July, 16, 16, 0, 0, 0, time.UTC),
		})

		Expect(result.Total).To(Equal(9))
		Expect(result.Summary.TotalSessions).To(Equal(2))
		Expect(result.Summary.TotalTokens).To(Equal(15))
		Expect(result.Summary.LowestContextFree).To(PointTo(Equal(free)))
		Expect(result.Database.Coverage).To(Equal("page"))
	})

	It("reports only records analyzed by a bounded throughput page", func() {
		startedAt := time.Date(2026, time.July, 16, 16, 0, 0, 0, time.UTC)
		endedAt := startedAt.Add(10 * time.Second)
		page := sessionRecordPage{
			Records: []SessionRecord{{
				ID: "analyzed", Source: "codex", Model: "gpt-5", StartedAt: &startedAt, EndedAt: &endedAt,
				Tokens: &SessionTokensWire{OutputTokens: 5, TotalTokens: 5},
			}},
			Total: 9,
		}

		result := buildSessionThroughputResult(sessionThroughputResultOptions{Page: page, Source: "all", Scope: "all"})

		Expect(result.Total).To(Equal(1))
		Expect(result.Groups).To(HaveLen(1))
	})

	It("expands an exact root session ID into its complete thread", func() {
		rootID := uuid.MustParse("055781c7-360a-4eb2-80be-452b3937fcfe")
		childID := uuid.MustParse("7ca78c55-e280-50ff-a19a-9f355a6fc55e")
		store := &sessionGetOverviewStore{
			identity: []database.SessionOverview{{ID: rootID, Source: "captain"}},
			thread: []database.SessionOverview{
				{ID: rootID, Source: "captain"},
				{ID: childID, ParentSessionID: &rootID, RootSessionID: &rootID, Source: "codex"},
			},
		}

		overviews, err := resolveOverviewsByIdentity(context.Background(), store, rootID.String())
		Expect(err).NotTo(HaveOccurred())
		Expect(overviews).To(Equal(store.thread))
		Expect(store.threadRoots).To(Equal([]uuid.UUID{rootID}))
	})

	It("hydrates transcript-less API sessions from persisted prompt runs", func(ctx SpecContext) {
		rootID := uuid.MustParse("055781c7-360a-4eb2-80be-452b3937fcfe")
		childID := uuid.MustParse("7ca78c55-e280-50ff-a19a-9f355a6fc55e")
		runID := uuid.MustParse("293b06b4-f6b7-4f69-a531-7499bd5a473a")
		agentType := "batch"
		startedAt := time.Date(2026, time.July, 19, 9, 30, 0, 0, time.UTC)
		finishedAt := startedAt.Add(8 * time.Second)
		store := &sessionGetOverviewStore{
			identity: []database.SessionOverview{{ID: rootID, Source: "captain", AgentType: &agentType}},
			thread: []database.SessionOverview{
				{ID: rootID, Source: "captain", AgentType: &agentType},
				{
					ID: childID, ParentSessionID: &rootID, RootSessionID: &rootID, Source: "captain",
					Provider: "google", PromptRunCount: 1,
				},
			},
			promptRuns: map[uuid.UUID][]database.PromptRun{
				childID: {{
					ID: runID, SessionID: childID, RootSessionID: rootID,
					RenderedSpec: map[string]any{
						"name":         "structured-ui-review",
						"outputSchema": map[string]any{"type": "object"},
					},
					PromptMarkdown: "Review the attached form screenshot.",
					ResultText:     `{"summary":"Use a single-column form layout."}`,
					Runtime: database.PromptRunRuntime{Resolved: database.PromptRunRuntimeSelection{
						Provider: "google", Mode: "api", Model: "gemini-2.5-pro", Effort: "high",
					}},
					State: database.PromptRunStateSucceeded, Phase: database.PromptRunPhaseFinished,
					StartedAt: &startedAt, FinishedAt: &finishedAt,
				}},
			},
		}

		result, err := runSessionGet(ctx, store, SessionGetOptions{ID: rootID.String()})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Sessions).To(HaveLen(2))
		Expect(result.Sessions[0].Aggregate).To(BeTrue())
		Expect(result.Pretty().String()).To(ContainSubstring("Aggregate session; child results are shown below"))
		Expect(result.Pretty().String()).NotTo(ContainSubstring("Transcript: unavailable"))
		child := result.Sessions[1]
		Expect(child.DetailAvailable).To(BeTrue())
		Expect(child.Summary.DetailAvailable).To(BeTrue())
		Expect(child.Summary.Messages).To(Equal(2))
		Expect(child.Summary.Model).To(Equal("gemini-2.5-pro"))
		Expect(child.Summary.ModelMode).To(Equal("api"))
		Expect(child.Summary.Provider).To(Equal("google"))
		Expect(child.Detail).NotTo(BeNil())
		Expect(child.Detail.Messages).To(Equal([]session.Message{
			{
				ID: runID.String() + "-user", Role: "user",
				Parts: []session.Part{{Type: session.PartText, Text: "Review the attached form screenshot."}},
			},
			{
				ID: runID.String() + "-assistant", Role: "assistant",
				Parts: []session.Part{{Type: session.PartText, Text: `{"summary":"Use a single-column form layout."}`}},
			},
		}))
		Expect(child.Detail.Model).To(Equal("gemini-2.5-pro"))
		Expect(child.Detail.ModelMode).To(Equal(api.ModeAPI))
		Expect(child.Detail.Provider).To(Equal("google"))
		Expect(child.Detail.StartedAt).To(PointTo(Equal(startedAt)))
		Expect(child.Detail.EndedAt).To(PointTo(Equal(finishedAt)))
		Expect(child.Detail.Prompt).To(MatchJSON(`{
			"name":"structured-ui-review",
			"outputSchema":{"type":"object"}
		}`))
		Expect(child.Detail.StructuredOutput).To(Equal(map[string]any{
			"summary": "Use a single-column form layout.",
		}))
	})

	It("prefers persisted structured output and ignores non-schema JSON text", func() {
		runID := uuid.MustParse("293b06b4-f6b7-4f69-a531-7499bd5a473a")
		overview := database.SessionOverview{ID: uuid.New(), Source: "captain"}
		detail, err := sessionFromPromptRun(overview, database.PromptRun{
			ID: runID,
			RenderedSpec: map[string]any{
				"outputSchema": map[string]any{"type": "object"},
			},
			ResultText: `{"source":"text"}`,
			ResultJSON: map[string]any{"source": "stored"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.StructuredOutput).To(Equal(map[string]any{"source": "stored"}))
		Expect(detail.Messages).To(HaveLen(1))
		Expect(detail.Messages[0].Parts[0].Text).To(Equal(`{"source":"text"}`))

		detail, err = sessionFromPromptRun(overview, database.PromptRun{
			ID: runID, ResultText: `{"source":"plain-text-prompt"}`,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.StructuredOutput).To(BeNil())
	})

	It("renders every match sequentially and preserves metadata-only sessions", func() {
		result := SessionGetResult{
			Sessions: []SessionGetItem{
				{
					CaptainID:         "055781c7-360a-4eb2-80be-452b3937fcfe",
					ProviderSessionID: "ad4c854e-cde6-4b99-99f3-667bf74112e3",
					Host:              "MacBook-Pro.local",
					DetailAvailable:   true,
					Summary: SessionRecord{
						ID: "ad4c854e-cde6-4b99-99f3-667bf74112e3", Source: "claude", Project: "flanksource",
					},
					Detail: &session.Session{ID: "ad4c854e-cde6-4b99-99f3-667bf74112e3", Source: "claude"},
				},
				{
					CaptainID:         "7ca78c55-e280-50ff-a19a-9f355a6fc55e",
					ProviderSessionID: "ad4c854e-cde6-4b99-99f3-667bf74112e3",
					Host:              "local",
					Summary: SessionRecord{
						ID: "ad4c854e-cde6-4b99-99f3-667bf74112e3", Source: "gavel", Project: "xero-cli",
					},
				},
			},
			Total: 2,
		}

		plain := result.Pretty().String()
		Expect(plain).To(ContainSubstring("055781c7-360a-4eb2-80be-452b3937fcfe"))
		Expect(plain).To(ContainSubstring("7ca78c55-e280-50ff-a19a-9f355a6fc55e"))
		Expect(plain).To(ContainSubstring("Transcript: unavailable"))
		Expect(plain).To(MatchRegexp("055781c7[\\s\\S]*7ca78c55"))

		markdown := result.Pretty().Markdown()
		Expect(markdown).To(MatchRegexp("055781c7[\\s\\S]*7ca78c55"))

		html := result.Pretty().HTML()
		Expect(html).NotTo(MatchRegexp(`text-gray-(600|700)`))
		Expect(html).To(ContainSubstring("text-muted"))
		formattedHTML, err := clicky.Format(result, clicky.FormatOptions{Format: "html"})
		Expect(err).NotTo(HaveOccurred())
		Expect(formattedHTML).NotTo(MatchRegexp(`text-gray-(600|700)`))
		Expect(formattedHTML).To(ContainSubstring("text-muted"))

		wire, err := json.Marshal(result)
		Expect(err).NotTo(HaveOccurred())
		Expect(wire).To(MatchJSON(`{
			"sessions": [
				{
					"captainId": "055781c7-360a-4eb2-80be-452b3937fcfe",
					"providerSessionId": "ad4c854e-cde6-4b99-99f3-667bf74112e3",
					"host": "MacBook-Pro.local",
					"detailAvailable": true,
					"summary": {"key":"","id":"ad4c854e-cde6-4b99-99f3-667bf74112e3","source":"claude","project":"flanksource","toolCalls":0,"messages":0,"detailAvailable":false},
					"detail": {"id":"ad4c854e-cde6-4b99-99f3-667bf74112e3","revision":0,"source":"claude","git":{},"usage":{"inputTokens":0,"outputTokens":0},"cost":{"inputTokens":0,"outputTokens":0,"totalTokens":0,"inputCost":0,"outputCost":0},"capabilities":{},"files":{},"approvals":{"approved":0,"denied":0}}
				},
				{
					"captainId": "7ca78c55-e280-50ff-a19a-9f355a6fc55e",
					"providerSessionId": "ad4c854e-cde6-4b99-99f3-667bf74112e3",
					"host": "local",
					"detailAvailable": false,
					"summary": {"key":"","id":"ad4c854e-cde6-4b99-99f3-667bf74112e3","source":"gavel","project":"xero-cli","toolCalls":0,"messages":0,"detailAvailable":false}
				}
			],
			"total": 2
		}`))
	})
})

type sessionGetOverviewStore struct {
	identity    []database.SessionOverview
	thread      []database.SessionOverview
	list        []database.SessionListSummary
	listFilter  database.SessionListFilter
	listFilters []database.SessionListFilter
	listPages   map[string]database.SessionListPage
	identities  []string
	threadRoots []uuid.UUID
	listCalls   int
	promptRuns  map[uuid.UUID][]database.PromptRun
}

func (s *sessionGetOverviewStore) ListSessionSummaries(_ context.Context, filter database.SessionListFilter) (database.SessionListPage, error) {
	s.listFilter = filter
	s.listFilters = append(s.listFilters, filter)
	if s.listPages != nil {
		return s.listPages[filter.Cursor], nil
	}
	return database.SessionListPage{Rows: s.list, Total: int64(len(s.list))}, nil
}

func (s *sessionGetOverviewStore) ListSessionOverviewsByIdentity(_ context.Context, identity string) ([]database.SessionOverview, error) {
	s.identities = append(s.identities, identity)
	return s.identity, nil
}

func (s *sessionGetOverviewStore) ListSessionOverviews(context.Context, database.SessionOverviewFilter) ([]database.SessionOverview, error) {
	s.listCalls++
	return nil, nil
}

func (s *sessionGetOverviewStore) ListThreadSessionOverviews(_ context.Context, rootID uuid.UUID) ([]database.SessionOverview, error) {
	s.threadRoots = append(s.threadRoots, rootID)
	return s.thread, nil
}

func (s *sessionGetOverviewStore) ListPromptRuns(_ context.Context, filter database.PromptRunFilter) ([]database.PromptRun, error) {
	if filter.SessionID == nil {
		return nil, nil
	}
	return s.promptRuns[*filter.SessionID], nil
}
