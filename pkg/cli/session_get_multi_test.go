package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
		request := httptest.NewRequest(http.MethodGet, "/api/captain/sessions/live?source=codex&all=true&limit=2&cursor=cursor-current", nil)
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(received).To(MatchFields(IgnoreExtras, Fields{
			"Source": Equal("codex"), "All": BeTrue(), "Limit": Equal(2), "Cursor": Equal("cursor-current"),
		}))
		var result SessionLiveResult
		Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
		Expect(result.Total).To(Equal(3))
		Expect(result.NextCursor).To(Equal("cursor-next"))
	})

	It("projects overview runtime data into the unified session detail", func() {
		detail := &session.Session{Turns: []session.Turn{{ID: "turn-1", Index: 1}}}
		summary := SessionRecord{
			Backend:         "codex-cmux",
			ReasoningEffort: "high",
			Live: &SessionLiveWire{
				PID: 4821, Status: "running", Active: true, CWD: "/repo", Command: "codex",
			},
		}

		enrichSessionDetail(detail, summary)

		Expect(detail.Backend).To(Equal("codex-cmux"))
		Expect(detail.ReasoningEffort).To(Equal("high"))
		Expect(detail.Live).To(Equal(&session.LiveProcess{
			PID: 4821, Status: "running", Active: true, CWD: "/repo", Command: "codex",
		}))
		Expect(detail.Turns).To(Equal([]session.Turn{{
			ID: "turn-1", Index: 1, Backend: "codex-cmux", ReasoningEffort: "high",
		}}))
	})

	It("passes UUID-prefix searches through the bounded list filter", func() {
		providerID := "ad4c854e-cde6-4b99-99f3-667bf74112e3"
		store := &sessionGetOverviewStore{
			list: []database.SessionListSummary{
				{ID: uuid.New(), ProviderSessionID: &providerID, Source: "claude"},
				{ID: uuid.New(), ProviderSessionID: &providerID, Source: "gavel"},
			},
		}

		page, err := dbSessionRecords(context.Background(), store, sessionRecordQuery{
			Source: "all", Query: "ad4c854e",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Records).To(HaveLen(2))
		Expect(store.listFilter.Query).To(Equal("ad4c854e"))
		Expect(store.listFilter.RootsOnly).To(BeTrue())
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

		overviews, err := resolveOverviewsByAnyID(context.Background(), store, rootID.String())
		Expect(err).NotTo(HaveOccurred())
		Expect(overviews).To(Equal(store.thread))
		Expect(store.threadRoots).To(Equal([]uuid.UUID{rootID}))
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
					"detail": {"id":"ad4c854e-cde6-4b99-99f3-667bf74112e3","source":"claude","git":{},"usage":{"inputTokens":0,"outputTokens":0},"cost":{"inputTokens":0,"outputTokens":0,"totalTokens":0,"inputCost":0,"outputCost":0},"capabilities":{},"files":{},"approvals":{"approved":0,"denied":0}}
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
