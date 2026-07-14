package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/clicky"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSessionGetMulti(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Session Get Multi Suite")
}

var _ = Describe("session get multi-result output", func() {
	It("uses targeted identity lookup for UUID-prefix list searches", func() {
		providerID := "ad4c854e-cde6-4b99-99f3-667bf74112e3"
		store := &sessionGetOverviewStore{
			identity: []database.SessionOverview{
				{ProviderSessionID: &providerID, Source: "claude"},
				{ProviderSessionID: &providerID, Source: "gavel"},
			},
		}

		records, err := dbSessionRecords(context.Background(), store, sessionRecordQuery{
			Source: "all", Query: "ad4c854e",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(records).To(HaveLen(2))
		Expect(store.identities).To(Equal([]string{"ad4c854e"}))
		Expect(store.listCalls).To(BeZero())
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
	identity   []database.SessionOverview
	identities []string
	listCalls  int
}

func (s *sessionGetOverviewStore) ListSessionOverviewsByIdentity(_ context.Context, identity string) ([]database.SessionOverview, error) {
	s.identities = append(s.identities, identity)
	return s.identity, nil
}

func (s *sessionGetOverviewStore) ListSessionOverviews(context.Context, database.SessionOverviewFilter) ([]database.SessionOverview, error) {
	s.listCalls++
	return nil, nil
}
